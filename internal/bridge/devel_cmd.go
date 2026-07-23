package bridge

import (
	"context"
	"strings"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

// handleDevel implements the hidden /.devel command (absent from /help): it
// toggles the opt-in prerelease (rc) update channel. Admin-only, since flipping
// it lets this bridge upgrade to unreleased rc builds. State is persisted so it
// survives the restart an rc upgrade triggers.
//
//	/.devel        show current developer mode
//	/.devel 1      enable prerelease channel  (also: on / true / yes)
//	/.devel 0      disable                    (also: off / false / no)
func (s *Service) handleDevel(ctx context.Context, msg Message, cmd Command, preference config.RuntimePreference) error {
	if !s.canRunAdminCommand(msg.Sender) {
		s.Audit.Record(msg.Sender, "admin_denied", msg.ChatID, "devel")
		return s.renderTextWithMode("devel", msg.ID, card.SegmentError, "❌ 只有管理员可以切换开发者模式。", preference.ConversationMode)
	}
	if s.DevMode == nil {
		return s.renderTextWithMode("devel", msg.ID, card.SegmentError, "开发者模式存储不可用。", preference.ConversationMode)
	}

	arg := strings.ToLower(strings.TrimSpace(cmd.Text))
	if arg == "" {
		return s.renderTextWithMode("devel", msg.ID, card.SegmentText, s.develStatusText(), preference.ConversationMode)
	}

	var on bool
	switch arg {
	case "1", "on", "true", "yes", "enable":
		on = true
	case "0", "off", "false", "no", "disable":
		on = false
	default:
		return s.renderTextWithMode("devel", msg.ID, card.SegmentError, "用法：`/.devel 0|1`（1 开启预发布升级通道，0 关闭）", preference.ConversationMode)
	}

	if _, err := s.DevMode.SetPrerelease(on); err != nil {
		return s.renderTextWithMode("devel", msg.ID, card.SegmentError, "写入开发者模式失败："+err.Error(), preference.ConversationMode)
	}
	action := "prerelease_disabled"
	if on {
		action = "prerelease_enabled"
	}
	s.Audit.Record(msg.Sender, "devmode_"+action, msg.ChatID, "")
	return s.renderTextWithMode("devel", msg.ID, card.SegmentText, s.develStatusText(), preference.ConversationMode)
}

func (s *Service) develStatusText() string {
	on := s.DevMode.Prerelease()
	var b strings.Builder
	if on {
		b.WriteString("🛠️ 开发者模式：**已开启**\n升级通道：**预发布（rc）**。`/help` 检查更新时可看到并升级到 rc 版本。")
		if strings.TrimSpace(s.PrereleaseManifestURL) == "" {
			b.WriteString("\n\n⚠️ 未配置预发布 manifest（`LAB_UPDATE_PRERELEASE_MANIFEST_URL`），当前仍会回落到 stable 通道。")
		}
	} else {
		b.WriteString("🛠️ 开发者模式：**已关闭**\n升级通道：**stable（正式版）**。用 `/.devel 1` 开启预发布通道。")
	}
	return b.String()
}
