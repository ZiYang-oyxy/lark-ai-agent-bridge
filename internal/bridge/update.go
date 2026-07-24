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
	s.storeResumeContext(sessionID, sessionKeyForMode(kind, msg, preference.ConversationMode), session.CatalogIdentity{Agent: kind}, now)
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
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "当前 Bridge 未启用自升级。")
	}
	result, err := s.Updates.Check(ctx, buildinfo.Version)
	if err != nil || !result.UpdateAvailable || result.Manifest.Version != strings.TrimSpace(req.Value) {
		if err != nil {
			s.Audit.Record(req.Actor, "update_check_failed", req.SessionID, err.Error())
		}
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "该版本已不可用，请返回帮助重新检查。")
	}
	notes, err := s.Updates.AggregatedReleaseNotes(ctx, buildinfo.Version, result.Manifest, func(version string, skipErr error) {
		s.Audit.Record(req.Actor, "update_notes_skipped", req.SessionID, "version="+version+" err="+skipErr.Error())
	})
	if err != nil {
		s.Audit.Record(req.Actor, "update_notes_failed", req.SessionID, err.Error())
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "Release note 暂时无法读取。")
	}
	actions := []card.Action{{ID: "update.help", Label: "返回帮助"}}
	if s.canRunAdminCommand(req.Actor) {
		actions = append(actions, card.Action{
			ID: "update.install", Label: "立即升级", Value: result.Manifest.Version,
			Confirm: &card.ActionConfirm{Title: "确认升级？", Text: fmt.Sprintf("Bridge 将从 v%s 升级到 v%s，并短暂重启。", buildinfo.Version, result.Manifest.Version)},
		})
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
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "当前 Bridge 未启用自升级。")
	}
	if !s.startUpgradeAttempt() {
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "另一项升级正在进行。")
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
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "目标版本已变化，请重新检查更新。")
	}
	prepared, err := s.Updates.Prepare(ctx, result.Asset)
	if err != nil {
		s.Audit.Record(req.Actor, "update_download_failed", req.SessionID, err.Error())
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "升级包下载或校验失败，Bridge 未被替换。")
	}
	s.setUpgradeMaintenance(true)
	s.upgradeGate.Lock()
	if s.hasUpgradeBlockingWork() {
		prepared.Abort()
		s.endUpgradeAttempt(true)
		keepAttempt = true
		s.Audit.Record(req.Actor, "update_busy", req.SessionID, "active or queued work")
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "当前有任务正在运行或排队，请稍后重试。")
	}
	if err := prepared.Replace(); err != nil {
		prepared.Abort()
		s.endUpgradeAttempt(true)
		keepAttempt = true
		s.Audit.Record(req.Actor, "update_replace_failed", req.SessionID, err.Error())
		return s.renderUpdateMessage(req.SessionID, card.SegmentError, "替换 binary 失败，Bridge 继续使用当前版本。")
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

func (s *Service) renderUpdateMessage(sessionID string, kind card.SegmentKind, text string) (ActionResult, error) {
	return s.renderActionEvent(card.Event{Type: "update_message", SessionID: sessionID, Segments: []card.Segment{{Kind: kind, Text: text}}})
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
