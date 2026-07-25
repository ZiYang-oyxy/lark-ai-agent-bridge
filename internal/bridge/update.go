package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/buildinfo"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
	bridgeupdate "lark-agent-bridge/internal/update"
)

type PreparedUpdate = bridgeupdate.PreparedUpdate

type UpdateManager interface {
	Check(context.Context, string) (bridgeupdate.CheckResult, error)
	Refresh(context.Context, string) (bridgeupdate.CheckResult, error)
	ReleaseNotes(context.Context, bridgeupdate.Manifest) (string, error)
	AggregatedReleaseNotes(ctx context.Context, currentVersion string, manifest bridgeupdate.Manifest, onSkip func(version string, err error)) (string, error)
	Prepare(context.Context, bridgeupdate.Asset) (bridgeupdate.PreparedUpdate, error)
	// PeekManifest returns the cached latest manifest without issuing any network
	// request. Used by the developer status-bar row on the streaming hot path so
	// it can show "最新 v..." only when a previous /help (or other live Check
	// call) has already warmed the cache.
	PeekManifest() (bridgeupdate.Manifest, bool)
}

func (s *Service) handleHelpCommand(ctx context.Context, msg Message, preference config.RuntimePreference) error {
	sessionID := runID("help", msg.ID)
	help := HelpCardData()
	if msg.IsGroup {
		help.ChatID = msg.ChatID
	}
	event := s.helpUpdateEvent(ctx, sessionID, msg.ID, preference.ConversationMode, help)
	if err := s.Cards.Render(event); err != nil {
		return err
	}
	now := time.Now()
	if help.ChatID != "" {
		s.storeHelpContext(sessionID, help.ChatID, now)
	}
	// Store the session key behind this help card so the 状态 button can render
	// the same detailed /status card the /status command would (via the shared
	// status.refresh/help.status callback), instead of a degraded summary.
	kind, ok := agent.ParseKind(preference.Agent)
	if !ok {
		kind = agent.Claude
	}
	s.storeResumeContext(sessionID, s.keyForMessage(kind, msg, preference.ConversationMode), session.CatalogIdentity{Agent: kind}, now)
	return nil
}

func (s *Service) helpUpdateEvent(ctx context.Context, sessionID, replyToMessageID string, mode config.ConversationMode, help card.HelpCard) card.Event {
	version := buildinfo.Version
	display := version
	if buildinfo.IsRelease() {
		display = "v" + version
	}
	status := &card.HelpVersionStatus{CurrentVersion: display}
	help.VersionStatus = status
	event := card.Event{
		Type: "help", SessionID: sessionID, ReplyToMessageID: replyToMessageID,
		ReplyInThread: mode == config.ConversationModeTopic,
		HelpCard:      &help,
	}
	if s.Updates == nil {
		return event
	}
	if !buildinfo.IsRelease() {
		status.Status = "开发构建不可自升级"
		return event
	}
	result, err := s.Updates.Check(ctx, version)
	if err != nil {
		s.Audit.Record("system", "update_check_failed", sessionID, err.Error())
		status.Status = "暂时无法检查更新"
	} else if result.UnsupportedPlatform {
		status.Status = "不支持当前平台"
	} else if result.UpdateAvailable {
		status.Status = "发现新版本"
		status.LatestVersion = "v" + result.Manifest.Version
		status.UpdateAvailable = true
		status.DetailsAction = card.Action{ID: "update.details", Label: "查看更新 →", Value: result.Manifest.Version}
	} else {
		status.Status = "已是最新版本"
	}
	return event
}

func (s *Service) handleUpdateDetails(ctx context.Context, req ActionRequest) (ActionResult, error) {
	if s.Updates == nil || !buildinfo.IsRelease() {
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "当前 Bridge 未启用自升级。", "red", "❌ 升级不可用")
	}
	result, err := s.Updates.Check(ctx, buildinfo.Version)
	if err != nil || !result.UpdateAvailable || result.Manifest.Version != strings.TrimSpace(req.Value) {
		if err != nil {
			s.Audit.Record(req.Actor, "update_check_failed", req.SessionID, err.Error())
		}
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "该版本已不可用，请返回帮助重新检查。", "orange", "⚠️ 目标版本已变化")
	}
	notes, err := s.Updates.AggregatedReleaseNotes(ctx, buildinfo.Version, result.Manifest, func(version string, skipErr error) {
		s.Audit.Record(req.Actor, "update_notes_skipped", req.SessionID, "version="+version+" err="+skipErr.Error())
	})
	if err != nil {
		s.Audit.Record(req.Actor, "update_notes_failed", req.SessionID, err.Error())
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "Release note 暂时无法读取。", "orange", "⚠️ 暂时无法读取 Release note")
	}
	actions := []card.Action{{ID: "update.help", Label: "返回帮助"}}
	if s.canRunAdminCommand(req.Actor) {
		actions = append(actions, card.Action{
			ID: "update.install", Label: "立即升级", Value: result.Manifest.Version,
			Confirm: &card.ActionConfirm{Title: "确认升级？", Text: fmt.Sprintf("Bridge 将从 v%s 升级到 v%s，并短暂重启。", buildinfo.Version, result.Manifest.Version)},
		})
	}
	// Release history is a read-only external link, so it is offered to every
	// user (not gated on admin like 立即升级). The base host is derived from the
	// manifest's release-notes URL, which is same-origin with the TOS bucket.
	prerelease := s.DevMode != nil && s.DevMode.Prerelease()
	if historyURL, ok := bridgeupdate.ReleaseHistoryURL(result.Manifest.ReleaseNotesURL, prerelease); ok {
		actions = append(actions, card.Action{ID: "update.history", Label: "发布历史", URL: historyURL})
	}
	title := "v" + result.Manifest.Version + " Release note"
	if buildinfo.Version != result.Manifest.Version {
		title = "v" + buildinfo.Version + " → v" + result.Manifest.Version + " Release note"
	}
	return s.renderActionEvent(card.Event{
		Type: "update_details", SessionID: req.SessionID,
		HeaderTitle: title, HeaderTemplate: "blue",
		Segments: []card.Segment{{Kind: card.SegmentText, Text: notes}}, Actions: actions,
	})
}

func (s *Service) handleUpdateInstall(ctx context.Context, req ActionRequest) (ActionResult, error) {
	if s.Updates == nil || !buildinfo.IsRelease() {
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "当前 Bridge 未启用自升级。", "red", "❌ 升级不可用")
	}
	if !s.startUpgradeAttempt() {
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "另一项升级正在进行，请稍后重试。", "orange", "⚠️ 升级已在进行")
	}
	keepAttempt := false
	defer func() {
		if !keepAttempt {
			s.endUpgradeAttempt(false)
		}
	}()
	result, err := s.Updates.Refresh(ctx, buildinfo.Version)
	if err != nil || !result.UpdateAvailable || result.Manifest.Version != strings.TrimSpace(req.Value) {
		if err != nil {
			s.Audit.Record(req.Actor, "update_check_failed", req.SessionID, err.Error())
		}
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "目标版本已变化，请重新检查更新。", "orange", "⚠️ 目标版本已变化")
	}
	prepared, err := s.Updates.Prepare(ctx, result.Asset)
	if err != nil {
		s.Audit.Record(req.Actor, "update_download_failed", req.SessionID, err.Error())
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "升级包下载或校验失败，Bridge 未被替换。", "red", "❌ 下载或校验失败")
	}
	s.setUpgradeMaintenance(true)
	s.upgradeGate.Lock()
	if s.hasUpgradeBlockingWork() {
		// 快照占用会话清单,让用户知道具体是哪些会话在挡升级。快照必须在 Abort/end 之前采
		// (Sessions.HasWork 判过后,活跃 batch 完成会自然从 List 里消失,这段窗口小但有必要拿准)。
		blocking := s.snapshotBlockingWork()
		prepared.Abort()
		s.endUpgradeAttempt(true)
		keepAttempt = true
		s.Audit.Record(req.Actor, "update_busy", req.SessionID,
			fmt.Sprintf("active or queued work: %d session(s)", len(blocking)))
		var extras []card.Segment
		if md := blockingSessionsMarkdown(blocking); md != "" {
			extras = append(extras, card.Segment{Kind: card.SegmentText, Text: md})
		}
		return s.renderUpdateMessage(req.SessionID, card.SegmentError,
			"当前有任务正在运行或排队，升级已暂停。等这些会话空闲后可在 /help 重试。",
			"orange", "⚠️ 升级已暂缓：有会话在跑", extras...)
	}
	if err := prepared.Replace(); err != nil {
		prepared.Abort()
		s.endUpgradeAttempt(true)
		keepAttempt = true
		s.Audit.Record(req.Actor, "update_replace_failed", req.SessionID, err.Error())
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "替换 binary 失败，Bridge 继续使用当前版本。", "red", "❌ 替换 binary 失败")
	}
	keepAttempt = true
	s.Audit.Record(req.Actor, "update_restart_scheduled", req.SessionID, "target_version="+result.Manifest.Version)
	event := card.Event{
		Type: "update_restarting", SessionID: req.SessionID,
		HeaderTitle: "Bridge 正在升级", HeaderTemplate: "orange",
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "升级包已验证，Bridge 正在重启。重连后可通过 /help 确认版本。"}},
	}
	actionResult, renderErr := s.renderActionEvent(event)
	go func() {
		// On success the current process exits inside Restart (detach launcher),
		// so anything past this call only runs when the restart genuinely failed.
		restartErr := prepared.Restart(os.Args, os.Environ())
		if restartErr != nil {
			s.Audit.Record(req.Actor, "update_exec_failed", req.SessionID, restartErr.Error())
			// The "正在重启" card is now a lie — the process did not restart and the
			// binary was rolled back to the previous version. Tell the user the
			// truth and clear maintenance so a retry is possible, instead of
			// silently leaving a stale process running under a "升级中" banner.
			s.setUpgradeMaintenance(false)
			_ = s.Cards.Render(card.Event{
				Type: "update_message", SessionID: req.SessionID,
				HeaderTitle: "Bridge 升级失败", HeaderTemplate: "red",
				Segments: []card.Segment{{Kind: card.SegmentError, Text: fmt.Sprintf(
					"重启失败，已回滚到当前版本 v%s，请稍后在 /help 重试。", buildinfo.Version)}},
			})
		}
		s.endUpgradeAttempt(true)
	}()
	return actionResult, renderErr
}

// renderUpdateMessage 渲染升级流程的失败/中断消息卡片。
// template 决定 header 颜色语义:
//   - "orange" 用户可自救(等一下重试、目标版本已变化、任务在跑挡住升级)——**这是 busy 场景**
//   - "red"    真出错(下载/校验失败、替换失败、未启用自升级)
//
// title 是 header 标题(如"⚠️ 升级已暂缓" / "❌ Bridge 升级失败")。
// extraSegments 追加到 kind/text 之后,用于 busy 场景附会话清单;通常为 nil。
func (s *Service) renderUpdateMessage(sessionID string, kind card.SegmentKind, text, template, title string, extraSegments ...card.Segment) (ActionResult, error) {
	segments := append([]card.Segment{{Kind: kind, Text: text}}, extraSegments...)
	return s.renderActionEvent(card.Event{
		Type: "update_message", SessionID: sessionID,
		HeaderTemplate: template, HeaderTitle: title,
		Segments: segments,
	})
}

// blockingSession 是升级 busy 分支要展示给用户的一条占用会话摘要:标识 Agent 和工作目录,
// 附上活跃/排队状态,让 admin 知道具体是哪个上下文在挡升级。chat_id 做前 4/后 4 缩略脱敏
// (admin 上下文可显示但避免把完整 id 泄漏进日志/截图),thread 不显示(会带 topic 语义)。
type blockingSession struct {
	Agent      string
	ChatBrief  string
	WorkDir    string
	State      string
	LastActive time.Time
}

func (s *Service) snapshotBlockingWork() []blockingSession {
	// Sessions.List 内部已加锁;bridge 本地 activeRuns/pendingRuns 用 s.mu 保护。
	// 先按 Sessions.List 采一份 busy session(有 ActiveBatch 或 Queue 非空或 StateRunning),
	// 再补充只在 pendingRuns 里的场景(pending workdir 确认但还没起 batch)。key 去重按 session ID。
	sessions := s.Sessions.List()
	seen := make(map[string]bool)
	out := make([]blockingSession, 0)
	for _, sess := range sessions {
		if sess.State != session.StateRunning && sess.ActiveBatch == nil && len(sess.Queue) == 0 {
			continue
		}
		seen[sess.ID] = true
		state := string(sess.State)
		if sess.ActiveBatch != nil && sess.State != session.StateRunning {
			state = "running"
		}
		if len(sess.Queue) > 0 {
			state = fmt.Sprintf("%s (queued=%d)", state, len(sess.Queue))
		}
		out = append(out, blockingSession{
			Agent:      string(sess.Key.Agent),
			ChatBrief:  briefChatID(sess.Key.ChatID),
			WorkDir:    sess.WorkDir,
			State:      state,
			LastActive: sess.LastActive,
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pr := range s.pendingRuns {
		if seen[pr.SessionID] {
			continue
		}
		out = append(out, blockingSession{
			Agent:     string(pr.Command.Agent),
			ChatBrief: briefChatID(pr.Message.ChatID),
			WorkDir:   pr.WorkDir,
			State:     "pending confirm",
		})
	}
	return out
}

// briefChatID 缩略 chat_id 便于卡片可读,并降低隐私暴露面。太短的直接返回原值。
func briefChatID(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if len(chatID) <= 12 {
		return chatID
	}
	return chatID[:4] + "…" + chatID[len(chatID)-4:]
}

// blockingSessionsMarkdown 把占用会话清单渲成 markdown 表格。空列表返回空字符串。
func blockingSessionsMarkdown(sessions []blockingSession) string {
	if len(sessions) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**占用中的会话（共 ")
	fmt.Fprintf(&b, "%d", len(sessions))
	b.WriteString(" 个）**\n\n")
	b.WriteString("| # | Agent | Chat | 工作目录 | 状态 | 最近活跃 |\n")
	b.WriteString("|---|-------|------|----------|------|----------|\n")
	for i, sess := range sessions {
		last := "-"
		if !sess.LastActive.IsZero() {
			last = sess.LastActive.Format("15:04:05")
		}
		workdir := sess.WorkDir
		if workdir == "" {
			workdir = "-"
		}
		chat := sess.ChatBrief
		if chat == "" {
			chat = "-"
		}
		fmt.Fprintf(&b, "| %d | %s | `%s` | `%s` | %s | %s |\n",
			i+1, sess.Agent, chat, workdir, sess.State, last)
	}
	return b.String()
}

func (s *Service) startUpgradeAttempt() bool {
	s.upgradeStateMu.Lock()
	defer s.upgradeStateMu.Unlock()
	if s.upgradeInProgress {
		return false
	}
	s.upgradeInProgress = true
	return true
}

func (s *Service) setUpgradeMaintenance(value bool) {
	s.upgradeStateMu.Lock()
	s.upgradeMaintenance = value
	s.upgradeStateMu.Unlock()
}

func (s *Service) isUpgradeMaintenance() bool {
	s.upgradeStateMu.Lock()
	defer s.upgradeStateMu.Unlock()
	return s.upgradeMaintenance
}

func (s *Service) endUpgradeAttempt(unlockGate bool) {
	s.upgradeStateMu.Lock()
	s.upgradeInProgress = false
	s.upgradeMaintenance = false
	s.upgradeStateMu.Unlock()
	if unlockGate {
		s.upgradeGate.Unlock()
	}
}

func (s *Service) hasUpgradeBlockingWork() bool {
	if s.Sessions.HasWork() {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.activeRuns) != 0 || len(s.pendingRuns) != 0
}

func (s *Service) withUpgradeReadGate(fn func() error) error {
	if s.isUpgradeMaintenance() {
		return errors.New("bridge update maintenance")
	}
	s.upgradeGate.RLock()
	defer s.upgradeGate.RUnlock()
	if s.isUpgradeMaintenance() {
		return errors.New("bridge update maintenance")
	}
	return fn()
}
