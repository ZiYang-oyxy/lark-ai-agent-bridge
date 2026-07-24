package bridge

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
	"lark-agent-bridge/internal/workspace"
)

// workspaceScope derives the topic-level scope key for the workspace store,
// matching session key granularity (chatID or chatID:threadID).
func (s *Service) workspaceScope(key session.Key) string {
	if key.Thread != "" {
		return key.ChatID + ":" + key.Thread
	}
	return key.ChatID
}

// switchWorkDir performs the "interrupt run + store realpath + reset session"
// trio shared by /cd and /ws use. Callers must have already validated admin
// permission and resolved realpath through workspace.ResolveWorkingDirectory.
func (s *Service) switchWorkDir(key session.Key, scope, realpath string) error {
	// 1. interrupt any active run on this key (best effort; ok=false when idle)
	s.requestActiveRunStopByKey(key)
	// 2. store realpath as the authoritative cwd for this scope
	if err := s.Workspaces.SetCwd(scope, realpath); err != nil {
		return err
	}
	// 3. reset session — switching directory means switching context
	s.Sessions.Reset(key, realpath)
	return nil
}

func (s *Service) handleCd(ctx context.Context, msg Message, cmd Command, preference config.RuntimePreference) error {
	key := s.keyForMessage(cmd.Agent, msg, preference.ConversationMode)
	scope := s.workspaceScope(key)

	// bare /cd — read-only, show current effective workdir. Allowed for any
	// authorized user (the message access gate already ran upstream).
	if strings.TrimSpace(cmd.WorkDir) == "" {
		return s.renderTextWithMode("cd", msg.ID, card.SegmentText,
			fmt.Sprintf("当前工作目录：`%s`", s.effectiveWorkDir(key, Command{})), preference.ConversationMode)
	}

	// mutating — admin only
	if !s.canRunAdminCommand(msg.Sender) {
		s.Audit.Record(msg.Sender, "admin_denied", msg.ChatID, "cd")
		return s.renderTextWithMode("cd", msg.ID, card.SegmentError, "❌ 只有管理员可以切换工作目录。", preference.ConversationMode)
	}
	if s.Workspaces == nil {
		return s.renderTextWithMode("cd", msg.ID, card.SegmentError, "工作区存储不可用。", preference.ConversationMode)
	}

	// home is best-effort: when empty, ResolveWorkingDirectory rejects ~ paths
	// with a user-visible error; absolute paths are unaffected.
	home, _ := os.UserHomeDir()
	res := workspace.ResolveWorkingDirectory(cmd.WorkDir, home)
	if !res.OK {
		return s.renderTextWithMode("cd", msg.ID, card.SegmentError, res.UserVisible, preference.ConversationMode)
	}
	if !res.Exists {
		// Hand off to the existing create-confirm card flow. Store a CdSwitch
		// pending keyed by the same sessionID the card carries, so the
		// create_workdir callback switches into the directory once created
		// (create-and-switch semantics) instead of forcing a second /cd.
		pendingID := runID(key.ID(), msg.ID)
		asked, err := s.ensureWorkDirOrAsk(res.Realpath, pendingID, msg.ID, preference.ConversationMode)
		if err != nil {
			return err
		}
		if asked {
			s.storePendingRun(pendingID, pendingRun{WorkDir: res.Realpath, CdSwitch: true, CdKey: key, CdScope: scope, Preference: preference})
		}
		return nil
	}
	if err := s.switchWorkDir(key, scope, res.Realpath); err != nil {
		return s.renderTextWithMode("cd", msg.ID, card.SegmentError, "切换失败："+err.Error(), preference.ConversationMode)
	}
	return s.renderTextWithMode("cd", msg.ID, card.SegmentText,
		fmt.Sprintf("📁 已切换工作目录：`%s`（已重置会话）", res.Realpath), preference.ConversationMode)
}

func (s *Service) handleWs(ctx context.Context, msg Message, cmd Command, preference config.RuntimePreference) error {
	key := s.keyForMessage(cmd.Agent, msg, preference.ConversationMode)
	scope := s.workspaceScope(key)

	if s.Workspaces == nil {
		return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "工作区存储不可用。", preference.ConversationMode)
	}

	switch cmd.WsSub {
	case "list", "":
		aliases := s.Workspaces.ListNamed(scope)
		var b strings.Builder
		fmt.Fprintf(&b, "📁 当前工作目录：`%s`\n", s.effectiveWorkDir(key, Command{}))
		if len(aliases) == 0 {
			b.WriteString("（暂无命名工作区，用 `/ws save <name>` 保存）")
		} else {
			names := make([]string, 0, len(aliases))
			for n := range aliases {
				names = append(names, n)
			}
			sort.Strings(names)
			b.WriteString("命名工作区：\n")
			for _, n := range names {
				fmt.Fprintf(&b, "· `%s` → `%s`\n", n, aliases[n])
			}
		}
		return s.renderTextWithMode("ws", msg.ID, card.SegmentText, b.String(), preference.ConversationMode)

	case "save":
		if !s.canRunAdminCommand(msg.Sender) {
			s.Audit.Record(msg.Sender, "admin_denied", msg.ChatID, "ws save")
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "❌ 只有管理员可以保存工作区。", preference.ConversationMode)
		}
		if strings.TrimSpace(cmd.WsName) == "" {
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "用法：/ws save <name>", preference.ConversationMode)
		}
		cwd := s.effectiveWorkDir(key, Command{})
		if err := s.Workspaces.SaveNamed(scope, cmd.WsName, cwd); err != nil {
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "保存失败："+err.Error(), preference.ConversationMode)
		}
		return s.renderTextWithMode("ws", msg.ID, card.SegmentText,
			fmt.Sprintf("已保存 `%s` → `%s`", cmd.WsName, cwd), preference.ConversationMode)

	case "use":
		if !s.canRunAdminCommand(msg.Sender) {
			s.Audit.Record(msg.Sender, "admin_denied", msg.ChatID, "ws use")
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "❌ 只有管理员可以切换工作区。", preference.ConversationMode)
		}
		if strings.TrimSpace(cmd.WsName) == "" {
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "用法：/ws use <name>", preference.ConversationMode)
		}
		target, ok := s.Workspaces.UseNamed(scope, cmd.WsName)
		if !ok {
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError,
				fmt.Sprintf("未找到命名工作区 `%s`", cmd.WsName), preference.ConversationMode)
		}
		home, _ := os.UserHomeDir()
		res := workspace.ResolveWorkingDirectory(target, home)
		if !res.OK {
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError,
				fmt.Sprintf("工作区 `%s` 指向的目录不可用：%s", cmd.WsName, res.UserVisible), preference.ConversationMode)
		}
		if !res.Exists {
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError,
				fmt.Sprintf("工作区 `%s` 指向的目录已不存在：`%s`", cmd.WsName, res.Realpath), preference.ConversationMode)
		}
		if err := s.switchWorkDir(key, scope, res.Realpath); err != nil {
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "切换失败："+err.Error(), preference.ConversationMode)
		}
		return s.renderTextWithMode("ws", msg.ID, card.SegmentText,
			fmt.Sprintf("📁 已切换到 `%s`：`%s`（已重置会话）", cmd.WsName, res.Realpath), preference.ConversationMode)

	case "del", "delete", "remove", "rm":
		if !s.canRunAdminCommand(msg.Sender) {
			s.Audit.Record(msg.Sender, "admin_denied", msg.ChatID, "ws del")
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "❌ 只有管理员可以删除工作区。", preference.ConversationMode)
		}
		if strings.TrimSpace(cmd.WsName) == "" {
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "用法：/ws del <name>", preference.ConversationMode)
		}
		if err := s.Workspaces.RemoveNamed(scope, cmd.WsName); err != nil {
			return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "删除失败："+err.Error(), preference.ConversationMode)
		}
		return s.renderTextWithMode("ws", msg.ID, card.SegmentText,
			fmt.Sprintf("已删除命名工作区 `%s`", cmd.WsName), preference.ConversationMode)

	default:
		return s.renderTextWithMode("ws", msg.ID, card.SegmentError, "用法：/ws list|save|use|del [name]", preference.ConversationMode)
	}
}
