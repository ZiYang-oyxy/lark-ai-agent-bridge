package bridge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
	"lark-agent-bridge/internal/tmux"
)

type Service struct {
	Config   config.Config
	Sessions *session.Manager
	Cards    card.Renderer
	Tmux     *tmux.Manager
	Audit    *audit.Recorder

	watchersMu           sync.Mutex
	watchers             map[string]*tmux.PaneWatcher
	quietPolls           map[string]int
	stopOutputMu         sync.Mutex
	stopOutputSuppresses map[string]time.Time
	pendingMu            sync.Mutex
	pendingRuns          map[string]pendingRun
	pendingInteractions  map[string]pendingInteraction
	pendingInitialInputs map[string]session.Input
	handledInteractions  map[string]handledInteraction
	topicMu              sync.Mutex
	topicModes           map[string]bool
}

const activeSessionPollStartupGrace = 2 * time.Second
const stopOutputSuppressDuration = 20 * time.Second
const handledInteractionSuppressDuration = 5 * time.Minute

type ActionRequest struct {
	SessionID string
	ActionID  string
	Value     string
	Actor     string
}

type pendingRun struct {
	SessionID string
	Command   Command
	Message   Message
	WorkDir   string
	Resume    bool
	ExpiresAt time.Time
}

type pendingInteraction struct {
	SessionID  string
	WindowName string
	Kind       agent.InteractionKind
	ActionID   string
	Value      string
	Signature  string
	ExpiresAt  time.Time
}

type handledInteraction struct {
	Signature string
	ExpiresAt time.Time
}

func NewService(cfg config.Config, renderer card.Renderer, tmuxManager *tmux.Manager, recorder *audit.Recorder) *Service {
	if renderer == nil {
		renderer = card.NewFakeRenderer()
	}
	if tmuxManager == nil {
		tmuxManager = tmux.NewManager(cfg.TmuxSession, nil)
	}
	if recorder == nil {
		recorder = audit.NewRecorder()
	}
	renderer = card.NewLimitRenderer(renderer, cfg.CardMaxChars)
	return &Service{
		Config:               cfg,
		Sessions:             session.NewManager(),
		Cards:                renderer,
		Tmux:                 tmuxManager,
		Audit:                recorder,
		watchers:             map[string]*tmux.PaneWatcher{},
		quietPolls:           map[string]int{},
		stopOutputSuppresses: map[string]time.Time{},
		pendingRuns:          map[string]pendingRun{},
		pendingInteractions:  map[string]pendingInteraction{},
		pendingInitialInputs: map[string]session.Input{},
		handledInteractions:  map[string]handledInteraction{},
		topicModes:           map[string]bool{},
	}
}

func (s *Service) HandleMessage(ctx context.Context, msg Message) error {
	defaultKind, ok := agent.ParseKind(s.Config.DefaultAgent)
	if !ok {
		defaultKind = agent.Claude
	}
	cmd := ParseCommand(msg, defaultKind, agent.ApprovalDefault)
	switch cmd.Type {
	case CommandIgnored:
		return nil
	case CommandHelp:
		return s.renderText("help", msg.ID, card.SegmentText, HelpText())
	case CommandUnknown:
		return s.renderText("command", msg.ID, card.SegmentError, cmd.Text)
	case CommandSessions:
		return s.renderText("sessions", msg.ID, card.SegmentText, s.sessionsText())
	case CommandStatus:
		return s.renderText("status", msg.ID, card.SegmentText, s.statusText(cmd.Agent, msg))
	case CommandAttach:
		return s.renderText("attach", msg.ID, card.SegmentText, s.attachText(cmd.Agent, msg))
	case CommandInterrupt:
		return s.interrupt(ctx, cmd.Agent, msg)
	case CommandStop:
		return s.stop(ctx, cmd.Agent, msg)
	case CommandHistory:
		return s.renderText("history", msg.ID, card.SegmentText, s.historyText(cmd.Agent, msg))
	case CommandResume:
		return s.resume(ctx, cmd, msg)
	case CommandTopic:
		return s.renderText("topic", msg.ID, card.SegmentText, s.topicText(msg.ChatID, cmd.Text))
	case CommandRun:
		return s.run(ctx, cmd, msg)
	default:
		return s.renderText("command", msg.ID, card.SegmentError, "unsupported command")
	}
}

func (s *Service) run(ctx context.Context, cmd Command, msg Message) error {
	if cmd.Agent == "" {
		cmd.Agent = agent.Claude
	}
	text := strings.TrimSpace(cmd.Text)
	key := s.sessionKey(cmd.Agent, msg)
	workDir := s.Config.DefaultWorkDir
	if cmd.WorkDir != "" {
		workDir = cmd.WorkDir
	}
	asked, err := s.ensureWorkDirOrAsk(workDir, key.ID(), msg.ID)
	if err != nil {
		return err
	}
	if asked {
		s.storePendingRun(key.ID(), pendingRun{Command: cmd, Message: msg, WorkDir: workDir})
		return nil
	}
	input := session.Input{Sender: msg.Sender, Text: text, Time: msg.Time}
	sess, queued := s.Sessions.Enqueue(key, input, workDir)
	if queued {
		s.Audit.Record(msg.Sender, "queue_input", sess.ID, text)
		return s.Cards.Render(card.Event{Type: "reaction", SessionID: sess.ID, ReplyToMessageID: msg.ID, Message: "queued"})
	}
	s.Audit.Record(msg.Sender, "run_input", sess.ID, text)
	s.clearStopOutputSuppression(sess.ID)
	newWindow := !sess.WindowStarted
	if newWindow {
		if err := s.ensureAgentWindow(ctx, sess, cmd); err != nil {
			updated := s.Sessions.MarkCrashed(key, workDir)
			_ = s.Cards.Render(card.Event{
				Type:             "error",
				SessionID:        updated.ID,
				ReplyToMessageID: msg.ID,
				Segments:         []card.Segment{{Kind: card.SegmentError, Text: err.Error()}},
				Actions:          card.RestartActions(),
				Meta:             metaFromSession(updated),
			})
			return err
		}
		sess = s.Sessions.MarkWindowStarted(key, workDir, cmd.ApprovalMode)
	}
	deferInitialInput := newWindow && cmd.Agent == agent.Codex && text != ""
	if deferInitialInput {
		s.storePendingInitialInput(sess.ID, input)
	} else if text != "" {
		if err := s.Tmux.SendLine(ctx, sess.WindowName, text); err != nil {
			return err
		}
	}
	return s.renderStream(card.Event{
		Type:             "stream",
		SessionID:        sess.ID,
		ReplyToMessageID: msg.ID,
		Segments:         []card.Segment{{Kind: card.SegmentText, Text: text}},
		Meta:             metaFromSession(sess),
		StopButton: card.StopButton{
			Visible: true,
		},
	})
}

func (s *Service) HandleAction(ctx context.Context, req ActionRequest) error {
	s.Audit.Record(req.Actor, "card_action", req.SessionID, req.ActionID+" "+req.Value)
	switch req.ActionID {
	case "stop", "interrupt":
		s.clearPendingInteraction(req.SessionID)
		s.popPendingInitialInput(req.SessionID)
		sess := s.findSession(req.SessionID)
		if sess == nil {
			return s.Cards.Render(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "session not found"}}})
		}
		if err := s.interruptActiveRun(ctx, *sess); err != nil {
			return err
		}
		updated, next, err := s.finishCurrentRun(ctx, *sess)
		if err != nil {
			return err
		}
		if next == nil {
			s.suppressStopOutput(updated.ID)
		}
		if err := s.Cards.Render(card.Event{Type: "stop_button", SessionID: updated.ID, StopButton: card.StopButton{Visible: true, Disabled: true}, Message: "stopped", Meta: metaFromSession(updated)}); err != nil {
			return err
		}
		if next != nil {
			return s.renderDequeuedInput(updated, *next)
		}
		return nil
	case "restart_session":
		s.clearPendingInteraction(req.SessionID)
		return s.restartSession(ctx, req)
	case "terminate_session":
		s.clearPendingInteraction(req.SessionID)
		return s.terminateSession(ctx, req)
	case "allow_once", "allow_tool_session", "allow_all_session", "reject":
		s.suppressPendingInteraction(req.SessionID)
		return s.sendActionValue(ctx, req)
	case "create_workdir":
		pending, hasPending := s.popPendingRun(req.SessionID)
		workDir := req.Value
		if hasPending && pending.WorkDir != "" {
			workDir = pending.WorkDir
		}
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return err
		}
		if err := s.Cards.Render(card.Event{Type: "action", SessionID: req.SessionID, Message: "workdir created: " + workDir}); err != nil {
			return err
		}
		if hasPending {
			if pending.Resume {
				return s.resume(ctx, pending.Command, pending.Message)
			}
			return s.run(ctx, pending.Command, pending.Message)
		}
		return nil
	case "cancel_workdir":
		s.popPendingRun(req.SessionID)
		return s.Cards.Render(card.Event{Type: "action", SessionID: req.SessionID, Message: "workdir creation cancelled"})
	default:
		if strings.HasPrefix(req.ActionID, "choice_") || strings.HasPrefix(req.ActionID, "resume_") {
			s.suppressPendingInteraction(req.SessionID)
			return s.sendActionValue(ctx, req)
		}
		return s.Cards.Render(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "unknown action: " + req.ActionID}}})
	}
}

func (s *Service) PollSessionOutput(ctx context.Context, sessionID string) error {
	sess := s.findSession(sessionID)
	if sess == nil {
		return s.Cards.Render(card.Event{Type: "error", SessionID: sessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "session not found"}}})
	}
	return s.pollSessionOutput(ctx, sess, false)
}

func (s *Service) pollSessionOutput(ctx context.Context, sess *session.Session, skipTransientStartupMissing bool) error {
	if current := s.findSession(sess.ID); current != nil {
		sess = current
	}
	watcher := s.watcherFor(sess.ID, sess.WindowName)
	if sess.State != session.StateRunning && s.stopOutputSuppressed(sess.ID, time.Now()) {
		_, _ = watcher.Poll(ctx)
		return nil
	}
	delta, err := watcher.Poll(ctx)
	if err != nil {
		if skipTransientStartupMissing && isRecentWindowMissing(*sess, err, time.Now()) {
			return nil
		}
		return s.renderCrashed(sess, err)
	}
	if strings.TrimSpace(delta) == "" {
		return s.dispatchQueuedIfReady(ctx, *sess, watcher.Last, true)
	}
	s.resetQuietPolls(sess.ID)
	if touched := s.Sessions.Touch(sess.ID, time.Now()); touched.ID != "" {
		sess = &touched
	}
	sess = s.applyRuntimeMeta(sess, watcher.Last)
	interaction := agent.DetectInteraction(delta)
	if s.isHandledInteractionSuppressed(sess.ID, interaction, time.Now()) {
		interaction = agent.Interaction{Kind: agent.InteractionNone, Raw: delta}
	}
	switch interaction.Kind {
	case agent.InteractionAuthorization:
		s.storePendingInteraction(*sess, interaction)
		return s.Cards.Render(card.Event{
			Type:      "authorization",
			SessionID: sess.ID,
			Segments:  []card.Segment{{Kind: card.SegmentTool, Text: delta}},
			Actions:   card.AuthorizationActions(),
			Meta:      metaFromSession(*sess),
		})
	case agent.InteractionChoice:
		s.storePendingInteraction(*sess, interaction)
		return s.Cards.Render(card.Event{
			Type:      "choice",
			SessionID: sess.ID,
			Segments:  []card.Segment{{Kind: card.SegmentText, Text: delta}},
			Actions:   card.ChoiceActions(interaction.Options),
			Meta:      metaFromSession(*sess),
		})
	case agent.InteractionResume:
		s.storePendingInteraction(*sess, interaction)
		return s.Cards.Render(card.Event{
			Type:      "resume",
			SessionID: sess.ID,
			Segments:  []card.Segment{{Kind: card.SegmentText, Text: delta}},
			Actions:   card.ResumeActions(interaction.Options),
			Meta:      metaFromSession(*sess),
		})
	default:
		if err := s.renderStream(card.Event{
			Type:      "stream",
			SessionID: sess.ID,
			Segments:  segmentOutput(delta),
			Meta:      metaFromSession(*sess),
			StopButton: card.StopButton{
				Visible: true,
			},
		}); err != nil {
			return err
		}
		return s.dispatchQueuedIfReady(ctx, *sess, watcher.Last, false)
	}
}

func isRecentWindowMissing(sess session.Session, err error, now time.Time) bool {
	if err == nil || sess.CreatedAt.IsZero() || now.Sub(sess.CreatedAt) > activeSessionPollStartupGrace {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "can't find session") ||
		strings.Contains(msg, "can't find window") ||
		strings.Contains(msg, "can't find pane")
}

func (s *Service) applyRuntimeMeta(sess *session.Session, output string) *session.Session {
	meta := agent.ParseRuntimeMeta(output)
	if meta.Model == "" && meta.Tokens == 0 {
		return sess
	}
	updated := s.Sessions.UpdateMeta(sess.ID, meta.Model, meta.Tokens)
	if updated.ID == "" {
		return sess
	}
	return &updated
}

func (s *Service) dispatchQueuedIfReady(ctx context.Context, sess session.Session, snapshot string, quietPoll bool) error {
	if sess.State != session.StateRunning {
		return nil
	}
	ready := agent.DetectReady(snapshot)
	if !ready && !s.quietPollReady(sess.ID, quietPoll) {
		return nil
	}
	if pending, ok := s.peekPendingInitialInput(sess.ID); ok {
		if !ready {
			return nil
		}
		s.popPendingInitialInput(sess.ID)
		s.resetQuietPolls(sess.ID)
		s.Audit.Record(pending.Sender, "replay_initial_input", sess.ID, pending.Text)
		if err := s.Tmux.SendLine(ctx, sess.WindowName, pending.Text); err != nil {
			return err
		}
		return s.renderDequeuedInput(sess, pending)
	}
	updated, next, err := s.finishCurrentRun(ctx, sess)
	if err != nil {
		return err
	}
	if next == nil {
		return s.Cards.Render(card.Event{
			Type:      "status",
			SessionID: updated.ID,
			Meta:      metaFromSession(updated),
			Message:   "session idle",
		})
	}
	return s.renderDequeuedInput(updated, *next)
}

func (s *Service) finishCurrentRun(ctx context.Context, sess session.Session) (session.Session, *session.Input, error) {
	updated, next := s.Sessions.Complete(sess.Key, sess.WorkDir)
	s.resetQuietPolls(sess.ID)
	if next == nil {
		return updated, nil, nil
	}
	s.Audit.Record(next.Sender, "dequeue_input", updated.ID, next.Text)
	if err := s.Tmux.SendLine(ctx, updated.WindowName, next.Text); err != nil {
		return updated, next, err
	}
	return updated, next, nil
}

func (s *Service) renderDequeuedInput(updated session.Session, next session.Input) error {
	s.clearStopOutputSuppression(updated.ID)
	return s.Cards.Render(card.Event{
		Type:      "dequeue",
		SessionID: updated.ID,
		Segments:  []card.Segment{{Kind: card.SegmentText, Text: next.Text}},
		Meta:      metaFromSession(updated),
		StopButton: card.StopButton{
			Visible: true,
		},
		Message: "dequeued next prompt",
	})
}

func (s *Service) quietPollReady(sessionID string, quietPoll bool) bool {
	if !quietPoll || s.Config.QueueQuietPolls <= 0 {
		return false
	}
	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()
	s.quietPolls[sessionID]++
	return s.quietPolls[sessionID] >= s.Config.QueueQuietPolls
}

func (s *Service) resetQuietPolls(sessionID string) {
	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()
	delete(s.quietPolls, sessionID)
}

func (s *Service) suppressStopOutput(sessionID string) {
	if sessionID == "" {
		return
	}
	s.stopOutputMu.Lock()
	defer s.stopOutputMu.Unlock()
	s.stopOutputSuppresses[sessionID] = time.Now().Add(stopOutputSuppressDuration)
}

func (s *Service) clearStopOutputSuppression(sessionID string) {
	if sessionID == "" {
		return
	}
	s.stopOutputMu.Lock()
	defer s.stopOutputMu.Unlock()
	delete(s.stopOutputSuppresses, sessionID)
}

func (s *Service) stopOutputSuppressed(sessionID string, now time.Time) bool {
	s.stopOutputMu.Lock()
	defer s.stopOutputMu.Unlock()
	until, ok := s.stopOutputSuppresses[sessionID]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(s.stopOutputSuppresses, sessionID)
	return false
}

func (s *Service) renderCrashed(sess *session.Session, cause error) error {
	updated := s.Sessions.MarkCrashed(sess.Key, sess.WorkDir)
	s.resetQuietPolls(sess.ID)
	s.popPendingInitialInput(sess.ID)
	s.watchersMu.Lock()
	delete(s.watchers, sess.ID)
	s.watchersMu.Unlock()
	s.Audit.Record("system", "session_crashed", updated.ID, cause.Error())
	return s.Cards.Render(card.Event{
		Type:      "error",
		SessionID: updated.ID,
		Segments:  []card.Segment{{Kind: card.SegmentError, Text: "agent session crashed or tmux pane is unavailable: " + cause.Error()}},
		Actions:   card.RestartActions(),
		Meta:      metaFromSession(updated),
	})
}

func (s *Service) storePendingRun(sessionID string, pending pendingRun) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	pending.SessionID = sessionID
	if s.Config.InteractionTimeout > 0 && pending.ExpiresAt.IsZero() {
		pending.ExpiresAt = time.Now().Add(s.Config.InteractionTimeout)
	}
	s.pendingRuns[sessionID] = pending
}

func (s *Service) popPendingRun(sessionID string) (pendingRun, bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	pending, ok := s.pendingRuns[sessionID]
	if ok {
		delete(s.pendingRuns, sessionID)
	}
	return pending, ok
}

func (s *Service) duePendingRuns(now time.Time) []pendingRun {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	var due []pendingRun
	for sessionID, pending := range s.pendingRuns {
		if pending.ExpiresAt.IsZero() {
			continue
		}
		if !now.Before(pending.ExpiresAt) {
			due = append(due, pending)
			delete(s.pendingRuns, sessionID)
		}
	}
	return due
}

func (s *Service) RenderPendingRunTimeouts(now time.Time) error {
	for _, pending := range s.duePendingRuns(now) {
		s.Audit.Record("system", "pending_run_timeout", pending.SessionID, pending.WorkDir)
		if err := s.Cards.Render(card.Event{
			Type:      "action",
			SessionID: pending.SessionID,
			Message:   "workdir creation timed out: cancelled",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) storePendingInteraction(sess session.Session, interaction agent.Interaction) {
	if s.Config.InteractionTimeout <= 0 {
		return
	}
	actionID, value := defaultInteractionAction(interaction.Kind)
	if actionID == "" {
		return
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pendingInteractions[sess.ID] = pendingInteraction{
		SessionID:  sess.ID,
		WindowName: sess.WindowName,
		Kind:       interaction.Kind,
		ActionID:   actionID,
		Value:      value,
		Signature:  interactionSignature(interaction),
		ExpiresAt:  time.Now().Add(s.Config.InteractionTimeout),
	}
}

func (s *Service) clearPendingInteraction(sessionID string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	delete(s.pendingInteractions, sessionID)
}

func (s *Service) suppressPendingInteraction(sessionID string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if pending, ok := s.pendingInteractions[sessionID]; ok && pending.Signature != "" {
		s.handledInteractions[sessionID] = handledInteraction{
			Signature: pending.Signature,
			ExpiresAt: time.Now().Add(handledInteractionSuppressDuration),
		}
	}
	delete(s.pendingInteractions, sessionID)
}

func (s *Service) isHandledInteractionSuppressed(sessionID string, interaction agent.Interaction, now time.Time) bool {
	signature := interactionSignature(interaction)
	if signature == "" {
		return false
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	suppressed, ok := s.handledInteractions[sessionID]
	if !ok {
		return false
	}
	if !now.Before(suppressed.ExpiresAt) {
		delete(s.handledInteractions, sessionID)
		return false
	}
	return suppressed.Signature == signature
}

func interactionSignature(interaction agent.Interaction) string {
	if interaction.Kind == agent.InteractionNone {
		return ""
	}
	if len(interaction.Options) == 0 {
		return string(interaction.Kind)
	}
	return string(interaction.Kind) + "|" + strings.Join(interaction.Options, "\x1f")
}

func (s *Service) storePendingInitialInput(sessionID string, input session.Input) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pendingInitialInputs[sessionID] = input
}

func (s *Service) peekPendingInitialInput(sessionID string) (session.Input, bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	input, ok := s.pendingInitialInputs[sessionID]
	return input, ok
}

func (s *Service) popPendingInitialInput(sessionID string) (session.Input, bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	input, ok := s.pendingInitialInputs[sessionID]
	delete(s.pendingInitialInputs, sessionID)
	return input, ok
}

func (s *Service) duePendingInteractions(now time.Time) []pendingInteraction {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	var due []pendingInteraction
	for sessionID, pending := range s.pendingInteractions {
		if !now.Before(pending.ExpiresAt) {
			due = append(due, pending)
			if pending.Signature != "" {
				s.handledInteractions[sessionID] = handledInteraction{
					Signature: pending.Signature,
					ExpiresAt: now.Add(handledInteractionSuppressDuration),
				}
			}
			delete(s.pendingInteractions, sessionID)
		}
	}
	return due
}

func defaultInteractionAction(kind agent.InteractionKind) (string, string) {
	switch kind {
	case agent.InteractionAuthorization:
		return "reject", "reject"
	case agent.InteractionChoice:
		return "choice_timeout", "cancel"
	case agent.InteractionResume:
		return "resume_cancel", "cancel"
	default:
		return "", ""
	}
}

func (s *Service) RenderInteractionTimeouts(ctx context.Context, now time.Time) error {
	for _, pending := range s.duePendingInteractions(now) {
		if err := s.Tmux.SendLine(ctx, pending.WindowName, pending.Value); err != nil {
			return err
		}
		detail := pending.ActionID + " " + pending.Value
		s.Audit.Record("system", "interaction_timeout", pending.SessionID, detail)
		if err := s.Cards.Render(card.Event{
			Type:      "action",
			SessionID: pending.SessionID,
			Message:   "interaction timed out: sent " + pending.Value,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) RenderIdleReminders(now time.Time) error {
	for _, sess := range s.Sessions.IdleReminderDue(now, s.Config.IdleReminderAfter) {
		idleAfter := roundDuration(s.Config.IdleReminderAfter)
		if err := s.Cards.Render(card.Event{
			Type:      "idle_reminder",
			SessionID: sess.ID,
			Segments:  []card.Segment{{Kind: card.SegmentText, Text: fmt.Sprintf("Session has been idle for %s. Use /stop to terminate it when no longer needed.", idleAfter)}},
			Actions:   card.TerminateSessionActions(false),
			Meta:      metaFromSession(sess),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) PollActiveSessions(ctx context.Context) error {
	for _, sess := range s.Sessions.List() {
		if sess.State == session.StateStopped || sess.State == session.StateCrashed {
			continue
		}
		if !sess.WindowStarted {
			continue
		}
		if err := s.pollSessionOutput(ctx, &sess, true); err != nil {
			_ = s.Cards.Render(card.Event{
				Type:      "error",
				SessionID: sess.ID,
				Segments:  []card.Segment{{Kind: card.SegmentError, Text: err.Error()}},
				Meta:      metaFromSession(sess),
			})
		}
	}
	return nil
}

func (s *Service) StartBackgroundLoops(ctx context.Context, outputEvery, idleEvery time.Duration) {
	if outputEvery <= 0 {
		outputEvery = time.Second
	}
	if idleEvery <= 0 {
		idleEvery = time.Hour
	}
	go func() {
		ticker := time.NewTicker(outputEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.PollActiveSessions(ctx)
				_ = s.RenderInteractionTimeouts(ctx, time.Now())
				_ = s.RenderPendingRunTimeouts(time.Now())
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(idleEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				_ = s.RenderIdleReminders(now)
			}
		}
	}()
}

func (s *Service) watcherFor(sessionID, windowName string) *tmux.PaneWatcher {
	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()
	if w, ok := s.watchers[sessionID]; ok {
		return w
	}
	w := &tmux.PaneWatcher{Manager: s.Tmux, WindowName: windowName}
	s.watchers[sessionID] = w
	return w
}

func classifySegment(output string) card.SegmentKind {
	return classifySegmentLine(strings.TrimSpace(output))
}

func segmentOutput(output string) []card.Segment {
	if output == "" {
		return nil
	}
	lines := strings.SplitAfter(output, "\n")
	segments := make([]card.Segment, 0, len(lines))
	var b strings.Builder
	var current card.SegmentKind
	hasCurrent := false
	flush := func() {
		if !hasCurrent {
			return
		}
		segments = append(segments, card.Segment{Kind: current, Text: b.String()})
		b.Reset()
		hasCurrent = false
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		kind := classifySegmentLine(strings.TrimSpace(line))
		if strings.TrimSpace(line) == "" && hasCurrent {
			kind = current
		}
		if !hasCurrent {
			current = kind
			hasCurrent = true
		}
		if kind != current {
			flush()
			current = kind
			hasCurrent = true
		}
		b.WriteString(line)
	}
	flush()
	return segments
}

func classifySegmentLine(line string) card.SegmentKind {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "thinking") || strings.Contains(lower, "thought") || strings.Contains(lower, "reasoning") || strings.Contains(line, "思考"):
		return card.SegmentThought
	case strings.Contains(lower, "tool") ||
		strings.Contains(lower, "tool_use") ||
		strings.Contains(lower, "function_call") ||
		strings.Contains(lower, "mcp__") ||
		strings.Contains(line, "工具") ||
		looksLikeToolInvocation(line):
		return card.SegmentTool
	default:
		return card.SegmentText
	}
}

func looksLikeToolInvocation(line string) bool {
	for _, marker := range []string{
		"Bash(",
		"Read(",
		"Write(",
		"Edit(",
		"MultiEdit(",
		"Grep(",
		"Glob(",
		"WebFetch(",
		"TodoWrite(",
	} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

func (s *Service) sendActionValue(ctx context.Context, req ActionRequest) error {
	sess := s.findSession(req.SessionID)
	if sess == nil {
		return s.Cards.Render(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "session not found"}}})
	}
	value := req.Value
	if value == "" {
		value = req.ActionID
	}
	if err := s.Tmux.SendLine(ctx, sess.WindowName, value); err != nil {
		return err
	}
	return s.Cards.Render(card.Event{Type: "action", SessionID: sess.ID, Message: "sent action: " + req.ActionID})
}

func (s *Service) restartSession(ctx context.Context, req ActionRequest) error {
	sess := s.findSession(req.SessionID)
	if sess == nil {
		return s.Cards.Render(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "session not found"}}})
	}
	mode := sess.ApprovalMode
	if mode == "" {
		mode = agent.ApprovalDefault
	}
	cmd := Command{Agent: sess.Key.Agent, ApprovalMode: mode}
	if err := s.ensureAgentWindow(ctx, *sess, cmd); err != nil {
		updated := s.Sessions.MarkCrashed(sess.Key, sess.WorkDir)
		return s.Cards.Render(card.Event{
			Type:      "error",
			SessionID: updated.ID,
			Segments:  []card.Segment{{Kind: card.SegmentError, Text: err.Error()}},
			Actions:   card.RestartActions(),
			Meta:      metaFromSession(updated),
		})
	}
	updated := s.Sessions.MarkRestarted(sess.Key, sess.WorkDir, mode)
	s.Audit.Record(req.Actor, "restart_session", updated.ID, string(mode))
	return s.Cards.Render(card.Event{
		Type:      "status",
		SessionID: updated.ID,
		Message:   "session restarted",
		Meta:      metaFromSession(updated),
	})
}

func (s *Service) terminateSession(ctx context.Context, req ActionRequest) error {
	sess := s.findSession(req.SessionID)
	if sess == nil {
		return s.Cards.Render(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "session not found"}}})
	}
	updated := s.Sessions.Stop(sess.Key, sess.WorkDir)
	s.Audit.Record(req.Actor, "terminate_session", updated.ID, "")
	if err := s.Tmux.KillWindow(ctx, updated.WindowName); err != nil {
		return err
	}
	return s.Cards.Render(card.Event{
		Type:      "terminate_session",
		SessionID: updated.ID,
		Message:   "session terminated",
		Actions:   card.TerminateSessionActions(true),
		Meta:      metaFromSession(updated),
	})
}

func (s *Service) resume(ctx context.Context, cmd Command, msg Message) error {
	if cmd.Agent == "" {
		cmd.Agent = agent.Claude
	}
	key := s.sessionKey(cmd.Agent, msg)
	workDir := s.Config.DefaultWorkDir
	if cmd.WorkDir != "" {
		workDir = cmd.WorkDir
	}
	asked, err := s.ensureWorkDirOrAsk(workDir, key.ID(), msg.ID)
	if err != nil {
		return err
	}
	if asked {
		s.storePendingRun(key.ID(), pendingRun{Command: cmd, Message: msg, WorkDir: workDir, Resume: true})
		return nil
	}
	sess := s.Sessions.GetOrCreate(key, workDir)
	if sess.WindowStarted {
		return s.Cards.Render(card.Event{
			Type:             "error",
			SessionID:        sess.ID,
			ReplyToMessageID: msg.ID,
			Segments:         []card.Segment{{Kind: card.SegmentError, Text: "session already has an active tmux window; use /stop before /resume"}},
			Meta:             metaFromSession(*sess),
		})
	}
	adapter, err := agent.AdapterFor(cmd.Agent)
	if err != nil {
		return err
	}
	command, err := adapter.BuildCommand(agent.LaunchConfig{
		Kind:         cmd.Agent,
		WorkDir:      workDir,
		ApprovalMode: cmd.ApprovalMode,
		Resume:       true,
		ResumeTarget: cmd.ResumeTarget,
		ResumeLast:   cmd.ResumeLast,
	})
	if err != nil {
		return err
	}
	if err := s.Tmux.NewWindow(ctx, tmux.WindowSpec{Name: sess.WindowName, WorkDir: workDir, Command: command}); err != nil {
		updated := s.Sessions.MarkCrashed(key, workDir)
		_ = s.Cards.Render(card.Event{
			Type:             "error",
			SessionID:        updated.ID,
			ReplyToMessageID: msg.ID,
			Segments:         []card.Segment{{Kind: card.SegmentError, Text: err.Error()}},
			Actions:          card.RestartActions(),
			Meta:             metaFromSession(updated),
		})
		return err
	}
	updated := s.Sessions.MarkWindowStarted(key, workDir, cmd.ApprovalMode)
	detail := "picker"
	if cmd.ResumeLast {
		detail = "last"
	} else if cmd.ResumeTarget != "" {
		detail = cmd.ResumeTarget
	}
	s.Audit.Record(msg.Sender, "resume_agent_session", updated.ID, detail)
	return s.Cards.Render(card.Event{
		Type:             "status",
		SessionID:        updated.ID,
		ReplyToMessageID: msg.ID,
		Segments:         []card.Segment{{Kind: card.SegmentText, Text: "resume session launched: " + detail}},
		Meta:             metaFromSession(updated),
		Message:          "resume session launched",
		StopButton:       card.StopButton{Visible: true},
	})
}

func (s *Service) ensureWorkDirOrAsk(workDir, sessionID, replyToMessageID string) (bool, error) {
	if workDir == "" {
		return false, fmt.Errorf("workdir is empty")
	}
	info, err := os.Stat(workDir)
	if err == nil && info.IsDir() {
		return false, nil
	}
	if err == nil && !info.IsDir() {
		return false, fmt.Errorf("workdir is not a directory: %s", workDir)
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	return true, s.Cards.Render(card.Event{
		Type:             "workdir_confirm",
		SessionID:        sessionID,
		ReplyToMessageID: replyToMessageID,
		Segments:         []card.Segment{{Kind: card.SegmentText, Text: "Workdir does not exist: " + workDir}},
		Actions:          card.WorkDirCreateActions(workDir),
	})
}

func (s *Service) findSession(id string) *session.Session {
	for _, sess := range s.Sessions.List() {
		if sess.ID == id {
			cp := sess
			return &cp
		}
	}
	return nil
}

func (s *Service) ensureAgentWindow(ctx context.Context, sess session.Session, cmd Command) error {
	adapter, err := agent.AdapterFor(sess.Key.Agent)
	if err != nil {
		return err
	}
	command, err := adapter.BuildCommand(agent.LaunchConfig{Kind: sess.Key.Agent, WorkDir: sess.WorkDir, ApprovalMode: cmd.ApprovalMode})
	if err != nil {
		return err
	}
	return s.Tmux.NewWindow(ctx, tmux.WindowSpec{Name: sess.WindowName, WorkDir: sess.WorkDir, Command: command})
}

func (s *Service) interruptActiveRun(ctx context.Context, sess session.Session) error {
	if sess.Key.Agent == agent.Codex {
		if err := s.Tmux.KillPaneDescendants(ctx, sess.WindowName, string(agent.Codex)); err != nil {
			s.Audit.Record("system", "tool_process_cleanup_failed", sess.ID, err.Error())
		}
	}
	return s.Tmux.Interrupt(ctx, sess.WindowName)
}

func (s *Service) interrupt(ctx context.Context, kind agent.Kind, msg Message) error {
	key := s.sessionKey(kind, msg)
	sess := s.Sessions.GetOrCreate(key, s.Config.DefaultWorkDir)
	s.Audit.Record(msg.Sender, "interrupt", sess.ID, "")
	if err := s.interruptActiveRun(ctx, *sess); err != nil {
		return err
	}
	updated, next, err := s.finishCurrentRun(ctx, *sess)
	if err != nil {
		return err
	}
	if next == nil {
		s.suppressStopOutput(updated.ID)
	}
	if err := s.Cards.Render(card.Event{Type: "stop_button", SessionID: updated.ID, StopButton: card.StopButton{Visible: true, Disabled: true}, Message: "interrupted", Meta: metaFromSession(updated)}); err != nil {
		return err
	}
	if next != nil {
		return s.renderDequeuedInput(updated, *next)
	}
	return nil
}

func (s *Service) stop(ctx context.Context, kind agent.Kind, msg Message) error {
	key := s.sessionKey(kind, msg)
	sess := s.Sessions.Stop(key, s.Config.DefaultWorkDir)
	s.Audit.Record(msg.Sender, "stop_session", sess.ID, "")
	if err := s.Tmux.KillWindow(ctx, sess.WindowName); err != nil {
		return err
	}
	return s.Cards.Render(card.Event{Type: "stop_button", SessionID: sess.ID, StopButton: card.StopButton{Visible: true, Disabled: true}, Message: "stopped"})
}

func (s *Service) Cleanup(ctx context.Context) error {
	s.Audit.Record("system", "cleanup", "", s.Config.TmuxSession)
	return s.Tmux.KillSession(ctx)
}

func (s *Service) renderText(id, replyToMessageID string, kind card.SegmentKind, text string) error {
	for i, page := range card.SplitLongText(text, s.Config.CardMaxChars) {
		eventID := id
		if i > 0 {
			eventID = fmt.Sprintf("%s-page-%d", id, i+1)
		}
		if err := s.Cards.Render(card.Event{Type: "message", SessionID: eventID, ReplyToMessageID: replyToMessageID, Segments: []card.Segment{{Kind: kind, Text: page}}}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) renderStream(event card.Event) error {
	pages := card.SplitSegments(event.Segments, s.Config.CardMaxChars)
	for i, segments := range pages {
		pageEvent := event
		pageEvent.Segments = segments
		if len(pages) > 1 {
			pageEvent.Message = fmt.Sprintf("page %d/%d", i+1, len(pages))
		}
		if err := s.Cards.Render(pageEvent); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) sessionsText() string {
	sessions := s.Sessions.List()
	if len(sessions) == 0 {
		return "no active sessions"
	}
	now := time.Now()
	var b strings.Builder
	for _, sess := range sessions {
		fmt.Fprintf(&b, "%s\n", sess.ID)
		fmt.Fprintf(&b, "  agent=%s chat=%s", sess.Key.Agent, sess.Key.ChatID)
		if sess.Key.Thread != "" {
			fmt.Fprintf(&b, " thread=%s", sess.Key.Thread)
		}
		fmt.Fprintf(&b, "\n")
		fmt.Fprintf(&b, "  state=%s window=%s window_started=%t\n", sess.State, sess.WindowName, sess.WindowStarted)
		fmt.Fprintf(&b, "  workdir=%s\n", sess.WorkDir)
		fmt.Fprintf(&b, "  queue=%d history=%d\n", len(sess.Queue), len(sess.History))
		if sess.ApprovalMode != "" {
			fmt.Fprintf(&b, "  approval=%s\n", sess.ApprovalMode)
		}
		if sess.Model != "" || sess.Tokens > 0 {
			fmt.Fprintf(&b, "  model=%s tokens=%d\n", sess.Model, sess.Tokens)
		}
		if !sess.CreatedAt.IsZero() {
			fmt.Fprintf(&b, "  age=%s\n", roundDuration(now.Sub(sess.CreatedAt)))
		}
		if !sess.LastActive.IsZero() {
			fmt.Fprintf(&b, "  last_active=%s idle=%s\n", sess.LastActive.Format(time.RFC3339), roundDuration(now.Sub(sess.LastActive)))
		}
		fmt.Fprintf(&b, "  attach=%s\n", s.Tmux.AttachCommand(sess.WindowName))
	}
	return strings.TrimSpace(b.String())
}

func roundDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d.Round(time.Second)
}

func (s *Service) sessionKey(kind agent.Kind, msg Message) session.Key {
	thread := msg.ThreadID
	if !s.topicModeEnabled(msg.ChatID) {
		thread = ""
	}
	return session.Key{Agent: kind, ChatID: msg.ChatID, Thread: thread}
}

func (s *Service) topicText(chatID, text string) string {
	mode := strings.ToLower(strings.TrimSpace(text))
	switch mode {
	case "on", "enable", "enabled":
		s.setTopicMode(chatID, true)
		return "topic mode enabled"
	case "off", "disable", "disabled":
		s.setTopicMode(chatID, false)
		return "topic mode disabled"
	case "", "status":
		if s.topicModeEnabled(chatID) {
			return "topic mode enabled"
		}
		return "topic mode disabled"
	default:
		return "usage: /topic on|off|status"
	}
}

func (s *Service) setTopicMode(chatID string, enabled bool) {
	s.topicMu.Lock()
	defer s.topicMu.Unlock()
	s.topicModes[chatID] = enabled
}

func (s *Service) topicModeEnabled(chatID string) bool {
	s.topicMu.Lock()
	defer s.topicMu.Unlock()
	enabled, ok := s.topicModes[chatID]
	if !ok {
		return true
	}
	return enabled
}

func (s *Service) statusText(kind agent.Kind, msg Message) string {
	key := s.sessionKey(kind, msg)
	var b strings.Builder
	fmt.Fprintf(&b, "tmux_session=%s\n", s.Config.TmuxSession)
	fmt.Fprintf(&b, "default_agent=%s\n", s.Config.DefaultAgent)
	fmt.Fprintf(&b, "default_workdir=%s\n", s.Config.DefaultWorkDir)
	fmt.Fprintf(&b, "topic_mode=%t\n", s.topicModeEnabled(msg.ChatID))
	fmt.Fprintf(&b, "current_session=%s\n", key.ID())
	sess := s.findSession(key.ID())
	if sess == nil {
		b.WriteString("state=not_started")
		return b.String()
	}
	fmt.Fprintf(&b, "state=%s\n", sess.State)
	fmt.Fprintf(&b, "window=%s\n", sess.WindowName)
	fmt.Fprintf(&b, "window_started=%t\n", sess.WindowStarted)
	fmt.Fprintf(&b, "workdir=%s\n", sess.WorkDir)
	fmt.Fprintf(&b, "queue=%d\n", len(sess.Queue))
	fmt.Fprintf(&b, "history=%d\n", len(sess.History))
	if sess.Model != "" {
		fmt.Fprintf(&b, "model=%s\n", sess.Model)
	}
	if sess.Tokens > 0 {
		fmt.Fprintf(&b, "tokens=%d\n", sess.Tokens)
	}
	fmt.Fprintf(&b, "attach=%s", s.Tmux.AttachCommand(sess.WindowName))
	return b.String()
}

func (s *Service) attachText(kind agent.Kind, msg Message) string {
	key := s.sessionKey(kind, msg)
	sess := s.Sessions.GetOrCreate(key, s.Config.DefaultWorkDir)
	return s.Tmux.AttachCommand(sess.WindowName)
}

func (s *Service) historyText(kind agent.Kind, msg Message) string {
	key := s.sessionKey(kind, msg)
	sess := s.Sessions.GetOrCreate(key, s.Config.DefaultWorkDir)
	history := s.Sessions.History(sess.ID)
	if len(history) == 0 {
		return "no prompt history"
	}
	var b strings.Builder
	for _, p := range history {
		fmt.Fprintf(&b, "%s %s %s\n", p.Time.Format(time.RFC3339), p.Sender, p.Text)
	}
	return strings.TrimSpace(b.String())
}

func metaFromSession(sess session.Session) card.Meta {
	return card.Meta{Agent: string(sess.Key.Agent), Model: sess.Model, Tokens: sess.Tokens, WorkDir: sess.WorkDir, Status: string(sess.State)}
}
