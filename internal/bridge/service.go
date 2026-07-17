package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	osuser "os/user"
	"strings"
	"sync"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

type Service struct {
	Config   config.Config
	Sessions *session.Manager
	Cards    card.Renderer
	Runner   AgentRunner
	Audit    *audit.Recorder

	mu          sync.Mutex
	pendingRuns map[string]pendingRun
	activeRuns  map[string]activeRun
}

type AgentRunner interface {
	Run(context.Context, AgentRunRequest) (AgentRunResult, error)
}

type AgentRunRequest struct {
	Kind            agent.Kind
	Prompt          string
	WorkDir         string
	ClaudeSessionID string
	OnEvent         func(AgentStreamUpdate)
}

type AgentRunResult struct {
	Segments        []card.Segment
	Model           string
	Tokens          int
	ClaudeSessionID string
}

type AgentStreamUpdate struct {
	Segments        []card.Segment
	Model           string
	Tokens          int
	ClaudeSessionID string
	Activity        string
}

type pendingRun struct {
	SessionID string
	Command   Command
	Message   Message
	WorkDir   string
	ExpiresAt time.Time
}

type activeRun struct {
	BaseSessionID string
	Key           session.Key
	WorkDir       string
	Cancel        context.CancelFunc
	Stream        *agentCardStream
}

type ActionRequest struct {
	SessionID string
	ActionID  string
	Value     string
	Actor     string
}

func NewService(cfg config.Config, renderer card.Renderer, runner AgentRunner, recorder *audit.Recorder) *Service {
	if renderer == nil {
		renderer = card.NewFakeRenderer()
	}
	if runner == nil {
		runner = CLIExecRunner{}
	}
	if recorder == nil {
		recorder = audit.NewRecorder()
	}
	renderer = card.NewLimitRenderer(renderer, cfg.CardMaxChars)
	return &Service{
		Config:      cfg,
		Sessions:    session.NewManager(),
		Cards:       renderer,
		Runner:      runner,
		Audit:       recorder,
		pendingRuns: map[string]pendingRun{},
		activeRuns:  map[string]activeRun{},
	}
}

func (s *Service) HandleMessage(ctx context.Context, msg Message) error {
	defaultKind, ok := agent.ParseKind(s.Config.DefaultAgent)
	if !ok {
		defaultKind = agent.Claude
	}
	cmd := ParseCommand(msg, defaultKind)
	switch cmd.Type {
	case CommandIgnored:
		return nil
	case CommandHelp:
		return s.renderText("help", msg.ID, card.SegmentText, HelpText())
	case CommandUnknown:
		return s.renderText("command", msg.ID, card.SegmentError, cmd.Text)
	case CommandStatus:
		return s.renderText("status", msg.ID, card.SegmentText, s.statusText(cmd.Agent, msg))
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
	if cmd.Agent != agent.Claude {
		return s.renderText("unsupported-agent", msg.ID, card.SegmentError, "当前 bridge 只适配 claude。")
	}
	key := s.sessionKey(cmd.Agent, msg)
	workDir := s.Config.DefaultWorkDir
	if cmd.WorkDir != "" {
		workDir = cmd.WorkDir
	}
	pendingID := runID(key.ID(), msg.ID)
	asked, err := s.ensureWorkDirOrAsk(workDir, pendingID, msg.ID)
	if err != nil {
		return err
	}
	if asked {
		s.storePendingRun(pendingID, pendingRun{Command: cmd, Message: msg, WorkDir: workDir})
		return nil
	}
	text := strings.TrimSpace(cmd.Text)
	if text == "" {
		if cmd.Reset {
			sess := s.Sessions.Reset(key, workDir)
			s.Audit.Record(msg.Sender, "new_session", sess.ID, workDir)
			return s.Cards.Render(card.Event{
				Type:             "status",
				SessionID:        sess.ID,
				ReplyToMessageID: msg.ID,
				Segments:         []card.Segment{{Kind: card.SegmentText, Text: "Claude session is ready. Send a message in this chat/topic to continue."}},
				Meta:             metaFromSession(sess),
			})
		}
		return s.renderText("empty", msg.ID, card.SegmentError, "empty prompt")
	}
	input := session.Input{Sender: msg.Sender, Text: text, ReplyToMessageID: msg.ID, Time: msg.Time, Reset: cmd.Reset}
	sess, queued := s.Sessions.Enqueue(key, input, workDir)
	if queued {
		s.Audit.Record(msg.Sender, "queue_input", sess.ID, text)
		return s.Cards.Render(card.Event{Type: "reaction", SessionID: runID(sess.ID, msg.ID), ReplyToMessageID: msg.ID, Message: "queued"})
	}
	s.startRun(ctx, sess, input)
	return nil
}

func (s *Service) startRun(ctx context.Context, sess session.Session, input session.Input) {
	if input.Time.IsZero() {
		input.Time = time.Now()
	}
	id := runID(sess.ID, input.ReplyToMessageID)
	runCtx, cancel := context.WithCancel(ctx)
	stream := newAgentCardStream(s, id, sess, input)
	s.storeActiveRun(id, activeRun{BaseSessionID: sess.ID, Key: sess.Key, WorkDir: sess.WorkDir, Cancel: cancel, Stream: stream})
	s.Audit.Record(input.Sender, "run_input", sess.ID, input.Text)
	_ = stream.Start()
	go s.executeRun(runCtx, sess, input)
}

func (s *Service) executeRun(ctx context.Context, sess session.Session, input session.Input) {
	id := runID(sess.ID, input.ReplyToMessageID)
	defer s.clearActiveRun(id)
	result, err := s.Runner.Run(ctx, AgentRunRequest{
		Kind:            sess.Key.Agent,
		Prompt:          input.Text,
		WorkDir:         sess.WorkDir,
		ClaudeSessionID: sess.ClaudeSessionID,
		OnEvent: func(update AgentStreamUpdate) {
			if run, ok := s.activeRun(id); ok && run.Stream != nil {
				run.Stream.Handle(update)
			}
		},
	})
	if errors.Is(ctx.Err(), context.Canceled) {
		updated := s.Sessions.Stop(sess.Key, sess.WorkDir)
		if run, ok := s.activeRun(id); ok && run.Stream != nil {
			run.Stream.Finish("stopped", metaFromSession(updated), result)
		}
		return
	}
	if err != nil {
		updated := s.Sessions.MarkCrashed(sess.Key, sess.WorkDir)
		s.Audit.Record("system", "run_failed", updated.ID, err.Error())
		result.Segments = append(result.Segments, card.Segment{Kind: card.SegmentError, Text: err.Error()})
		if run, ok := s.activeRun(id); ok && run.Stream != nil {
			run.Stream.Finish("failed", metaFromSession(updated), result)
		}
		return
	}
	if result.ClaudeSessionID != "" || result.Model != "" || result.Tokens > 0 {
		sess = s.Sessions.UpdateRunResult(sess.ID, result.ClaudeSessionID, result.Model, result.Tokens)
	}
	updated, next := s.Sessions.Complete(sess.Key, sess.WorkDir)
	if updated.ID != "" {
		sess = updated
	}
	segments := result.Segments
	if len(segments) == 0 {
		segments = []card.Segment{{Kind: card.SegmentText, Text: "Claude 未返回内容。"}}
	}
	result.Segments = segments
	if run, ok := s.activeRun(id); ok && run.Stream != nil {
		run.Stream.Finish("completed", metaFromSession(sess), result)
	}
	if next != nil {
		s.Audit.Record(next.Sender, "dequeue_input", sess.ID, next.Text)
		nextSession := s.Sessions.GetOrCreate(sess.Key, sess.WorkDir)
		s.startRun(context.Background(), nextSession, *next)
	}
}

func (s *Service) HandleAction(ctx context.Context, req ActionRequest) error {
	s.Audit.Record(req.Actor, "card_action", req.SessionID, req.ActionID+" "+req.Value)
	switch req.ActionID {
	case "stop":
		if run, ok := s.cancelActiveRun(req.SessionID); ok {
			updated := s.Sessions.Stop(run.Key, run.WorkDir)
			if run.Stream != nil {
				run.Stream.Finish("stopped", metaFromSession(updated), AgentRunResult{})
			}
			return nil
		}
		return s.Cards.Render(card.Event{Type: "stopped", SessionID: req.SessionID, StopButton: card.StopButton{Visible: true, Disabled: true}, Message: "stopped", HeaderTitle: "⏹ 已停止 · ⏱ 0s", HeaderTemplate: "grey"})
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
			return s.run(ctx, pending.Command, pending.Message)
		}
		return nil
	case "cancel_workdir":
		s.popPendingRun(req.SessionID)
		return s.Cards.Render(card.Event{Type: "action", SessionID: req.SessionID, Message: "workdir creation cancelled"})
	default:
		return s.Cards.Render(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "unknown action: " + req.ActionID}}})
	}
}

func (s *Service) Cleanup(ctx context.Context) error {
	s.Audit.Record("system", "cleanup", "", "oneshot")
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, run := range s.activeRuns {
		run.Cancel()
		delete(s.activeRuns, id)
	}
	return nil
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

func (s *Service) StartBackgroundLoops(ctx context.Context, outputEvery time.Duration) {
	if outputEvery <= 0 {
		outputEvery = time.Second
	}
	go func() {
		ticker := time.NewTicker(outputEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				_ = s.RenderPendingRunTimeouts(now)
			}
		}
	}()
}

func (s *Service) storeActiveRun(id string, run activeRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeRuns[id] = run
}

func (s *Service) clearActiveRun(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeRuns, id)
}

func (s *Service) activeRun(id string) (activeRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.activeRuns[id]
	return run, ok
}

func (s *Service) cancelActiveRun(id string) (activeRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.activeRuns[id]
	if ok {
		run.Cancel()
		delete(s.activeRuns, id)
	}
	return run, ok
}

func (s *Service) storePendingRun(sessionID string, pending pendingRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending.SessionID = sessionID
	if s.Config.InteractionTimeout > 0 && pending.ExpiresAt.IsZero() {
		pending.ExpiresAt = time.Now().Add(s.Config.InteractionTimeout)
	}
	s.pendingRuns[sessionID] = pending
}

func (s *Service) popPendingRun(sessionID string) (pendingRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.pendingRuns[sessionID]
	if ok {
		delete(s.pendingRuns, sessionID)
	}
	return pending, ok
}

func (s *Service) duePendingRuns(now time.Time) []pendingRun {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func (s *Service) statusText(kind agent.Kind, msg Message) string {
	key := s.sessionKey(kind, msg)
	var b strings.Builder
	fmt.Fprintf(&b, "mode=claude_oneshot\n")
	fmt.Fprintf(&b, "default_workdir=%s\n", s.Config.DefaultWorkDir)
	fmt.Fprintf(&b, "current_session=%s\n", key.ID())
	sess := s.findSession(key.ID())
	if sess == nil {
		b.WriteString("state=not_started")
		return b.String()
	}
	fmt.Fprintf(&b, "state=%s\n", sess.State)
	fmt.Fprintf(&b, "workdir=%s\n", sess.WorkDir)
	fmt.Fprintf(&b, "queue=%d\n", len(sess.Queue))
	fmt.Fprintf(&b, "history=%d\n", len(sess.History))
	if sess.ClaudeSessionID != "" {
		fmt.Fprintf(&b, "claude_session=%s\n", sess.ClaudeSessionID)
	}
	if sess.Model != "" {
		fmt.Fprintf(&b, "model=%s\n", sess.Model)
	}
	if sess.Tokens > 0 {
		fmt.Fprintf(&b, "tokens=%d\n", sess.Tokens)
	}
	if msg.IsGroup {
		fmt.Fprintf(&b, "chat_sessions=%d\n", s.countChatSessions(msg.ChatID))
	}
	return strings.TrimSpace(b.String())
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

func (s *Service) countChatSessions(chatID string) int {
	count := 0
	for _, sess := range s.Sessions.List() {
		if sess.Key.ChatID == chatID {
			count++
		}
	}
	return count
}

func (s *Service) sessionKey(kind agent.Kind, msg Message) session.Key {
	return session.Key{Agent: kind, ChatID: msg.ChatID, Thread: msg.ThreadID}
}

func metaFromSession(sess session.Session) card.Meta {
	userName, ip := runtimeIdentity()
	return card.Meta{Agent: string(sess.Key.Agent), Model: sess.Model, Tokens: sess.Tokens, TotalTokens: sess.Tokens, User: userName, IP: ip, WorkDir: sess.WorkDir, Status: string(sess.State)}
}

var (
	runtimeIdentityOnce sync.Once
	runtimeUserName     string
	runtimeIP           string
)

func runtimeIdentity() (string, string) {
	runtimeIdentityOnce.Do(func() {
		if current, err := osuser.Current(); err == nil && current != nil {
			runtimeUserName = strings.TrimSpace(current.Username)
			if idx := strings.LastIndex(runtimeUserName, "\\"); idx >= 0 {
				runtimeUserName = runtimeUserName[idx+1:]
			}
			if idx := strings.LastIndex(runtimeUserName, "/"); idx >= 0 {
				runtimeUserName = runtimeUserName[idx+1:]
			}
		}
		if runtimeUserName == "" {
			runtimeUserName = os.Getenv("USER")
		}
		runtimeIP = firstNonLoopbackIPv4()
	})
	return runtimeUserName, runtimeIP
}

func firstNonLoopbackIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil {
				continue
			}
			ipv4 := ip.To4()
			if ipv4 == nil || ipv4.IsLoopback() {
				continue
			}
			return ipv4.String()
		}
	}
	return ""
}

func runID(baseSessionID, replyToMessageID string) string {
	if replyToMessageID == "" {
		return baseSessionID
	}
	return baseSessionID + ":message:" + replyToMessageID
}

type CLIExecRunner struct{}

func (CLIExecRunner) Run(ctx context.Context, req AgentRunRequest) (AgentRunResult, error) {
	command, err := agent.BuildOneShotCommand(agent.OneShotConfig{
		Kind:            req.Kind,
		WorkDir:         req.WorkDir,
		Prompt:          req.Prompt,
		ClaudeSessionID: req.ClaudeSessionID,
	})
	if err != nil {
		return AgentRunResult{}, err
	}
	if len(command) == 0 {
		return AgentRunResult{}, fmt.Errorf("empty agent command")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = req.WorkDir
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return AgentRunResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return AgentRunResult{}, err
	}
	result, scanErr := parseClaudeStream(pipe, &stdout, req.OnEvent)
	waitErr := cmd.Wait()
	if scanErr != nil {
		return result, scanErr
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return result, fmt.Errorf("%w: %s", waitErr, detail)
		}
		return result, waitErr
	}
	return result, nil
}

func ParseClaudeStreamOutput(data []byte) AgentRunResult {
	result, _ := parseClaudeStream(bytes.NewReader(data), nil, nil)
	return result
}

func parseClaudeStream(input io.Reader, copyTo *bytes.Buffer, onEvent func(AgentStreamUpdate)) (AgentRunResult, error) {
	var result AgentRunResult
	var answer strings.Builder
	var thought strings.Builder
	var tool strings.Builder
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	parsedJSON := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if copyTo != nil {
			copyTo.WriteString(line)
			copyTo.WriteByte('\n')
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			answer.WriteString(line)
			answer.WriteByte('\n')
			emitStreamUpdate(onEvent, AgentStreamUpdate{
				Segments: []card.Segment{{Kind: card.SegmentText, Text: line}},
				Activity: streamActivityAnswering,
			})
			continue
		}
		parsedJSON = true
		emitStreamUpdate(onEvent, streamUpdateFromClaudeEvent(event))
		consumeClaudeEvent(event, &answer, &thought, &tool, &result)
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if !parsedJSON && strings.TrimSpace(answer.String()) == "" {
		if copyTo != nil {
			answer.Write(copyTo.Bytes())
		}
	}
	addSegment := func(kind card.SegmentKind, text string) {
		text = strings.TrimSpace(text)
		if text != "" {
			result.Segments = append(result.Segments, card.Segment{Kind: kind, Text: text})
		}
	}
	addSegment(card.SegmentText, answer.String())
	addSegment(card.SegmentThought, thought.String())
	addSegment(card.SegmentTool, tool.String())
	return result, nil
}

func consumeClaudeEvent(event map[string]any, answer, thought, tool *strings.Builder, result *AgentRunResult) {
	if id, ok := event["session_id"].(string); ok && result.ClaudeSessionID == "" {
		result.ClaudeSessionID = id
	}
	if model, ok := event["model"].(string); ok && result.Model == "" {
		result.Model = model
	}
	result.Tokens += tokensFromValue(event["usage"])
	if eventType, _ := event["type"].(string); eventType == "result" {
		if text, ok := event["result"].(string); ok && strings.TrimSpace(text) != "" && strings.TrimSpace(answer.String()) == "" {
			answer.WriteString(text)
			answer.WriteByte('\n')
		}
		return
	}
	message, _ := event["message"].(map[string]any)
	if message == nil {
		return
	}
	if id, ok := message["session_id"].(string); ok && result.ClaudeSessionID == "" {
		result.ClaudeSessionID = id
	}
	if model, ok := message["model"].(string); ok && result.Model == "" {
		result.Model = model
	}
	result.Tokens += tokensFromValue(message["usage"])
	content, _ := message["content"].([]any)
	for _, raw := range content {
		block, _ := raw.(map[string]any)
		if block == nil {
			continue
		}
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			writeBlockText(answer, block)
		case "thinking", "reasoning", "redacted_thinking":
			writeBlockText(thought, block)
		case "tool_use":
			writeToolUse(tool, block)
		case "tool_result":
			writeToolResult(tool, block)
		}
	}
}

func emitStreamUpdate(onEvent func(AgentStreamUpdate), update AgentStreamUpdate) {
	if onEvent == nil {
		return
	}
	if update.Model == "" && update.Tokens == 0 && update.ClaudeSessionID == "" && update.Activity == "" && len(update.Segments) == 0 {
		return
	}
	onEvent(update)
}

func streamUpdateFromClaudeEvent(event map[string]any) AgentStreamUpdate {
	var update AgentStreamUpdate
	if id, ok := event["session_id"].(string); ok {
		update.ClaudeSessionID = id
	}
	if model, ok := event["model"].(string); ok {
		update.Model = model
	}
	update.Tokens += tokensFromValue(event["usage"])
	if eventType, _ := event["type"].(string); eventType == "result" {
		return update
	}
	if message, _ := event["message"].(map[string]any); message != nil {
		if id, ok := message["session_id"].(string); ok && update.ClaudeSessionID == "" {
			update.ClaudeSessionID = id
		}
		if model, ok := message["model"].(string); ok && update.Model == "" {
			update.Model = model
		}
		update.Tokens += tokensFromValue(message["usage"])
		if content, _ := message["content"].([]any); len(content) > 0 {
			for _, raw := range content {
				if block, _ := raw.(map[string]any); block != nil {
					update.Segments = append(update.Segments, streamSegmentsFromBlock(block)...)
				}
			}
		}
		if len(update.Segments) > 0 {
			update.Activity = activityFromSegments(update.Segments)
		}
		return update
	}
	update.Segments = append(update.Segments, streamSegmentsFromTopLevelEvent(event)...)
	if len(update.Segments) > 0 {
		update.Activity = activityFromSegments(update.Segments)
	}
	return update
}

func streamSegmentsFromBlock(block map[string]any) []card.Segment {
	blockType, _ := block["type"].(string)
	switch blockType {
	case "text":
		return segmentFromText(card.SegmentText, firstString(block, "text", "content"))
	case "thinking", "reasoning", "redacted_thinking":
		return segmentFromText(card.SegmentThought, firstString(block, "thinking", "text", "content"))
	case "tool_use":
		var b strings.Builder
		writeToolUse(&b, block)
		return segmentFromText(card.SegmentTool, b.String())
	case "tool_result":
		var b strings.Builder
		writeToolResult(&b, block)
		return segmentFromText(card.SegmentTool, b.String())
	}
	return nil
}

func streamSegmentsFromTopLevelEvent(event map[string]any) []card.Segment {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "content_block_start":
		if block, _ := event["content_block"].(map[string]any); block != nil {
			return streamSegmentsFromBlock(block)
		}
	case "content_block_delta":
		if delta, _ := event["delta"].(map[string]any); delta != nil {
			return streamSegmentsFromDelta(delta)
		}
	case "thinking", "reasoning", "reasoning_content":
		return segmentFromText(card.SegmentThought, firstString(event, "reasoning_content", "thinking", "content", "delta", "text"))
	case "text":
		return segmentFromText(card.SegmentText, firstString(event, "text", "content", "delta"))
	case "tool_use", "tool_use_delta":
		return segmentFromText(card.SegmentTool, firstString(event, "content", "delta", "tool_input", "input"))
	case "tool_result":
		return segmentFromText(card.SegmentTool, firstString(event, "tool_result", "content", "result"))
	}
	if text := firstString(event, "reasoning_content", "thinking"); text != "" {
		return segmentFromText(card.SegmentThought, text)
	}
	if text := firstString(event, "text", "content", "delta"); text != "" {
		return segmentFromText(card.SegmentText, text)
	}
	return nil
}

func streamSegmentsFromDelta(delta map[string]any) []card.Segment {
	deltaType, _ := delta["type"].(string)
	switch deltaType {
	case "text_delta":
		return segmentFromText(card.SegmentText, firstString(delta, "text"))
	case "thinking_delta", "reasoning_delta":
		return segmentFromText(card.SegmentThought, firstString(delta, "thinking", "text", "reasoning"))
	case "input_json_delta":
		return segmentFromText(card.SegmentTool, firstString(delta, "partial_json"))
	}
	if text := firstString(delta, "thinking", "reasoning"); text != "" {
		return segmentFromText(card.SegmentThought, text)
	}
	if text := firstString(delta, "text", "content", "delta"); text != "" {
		return segmentFromText(card.SegmentText, text)
	}
	if text := firstString(delta, "partial_json"); text != "" {
		return segmentFromText(card.SegmentTool, text)
	}
	return nil
}

func segmentFromText(kind card.SegmentKind, text string) []card.Segment {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return []card.Segment{{Kind: kind, Text: text}}
}

func activityFromSegments(segments []card.Segment) string {
	for _, segment := range segments {
		if segment.Kind == card.SegmentTool {
			return streamActivityTool
		}
	}
	for _, segment := range segments {
		if segment.Kind == card.SegmentText {
			return streamActivityAnswering
		}
	}
	for _, segment := range segments {
		if segment.Kind == card.SegmentThought {
			return streamActivityReasoning
		}
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := m[key].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func writeBlockText(b *strings.Builder, block map[string]any) {
	for _, key := range []string{"text", "thinking", "content"} {
		if text, ok := block[key].(string); ok && strings.TrimSpace(text) != "" {
			b.WriteString(text)
			b.WriteByte('\n')
			return
		}
	}
}

func writeToolUse(b *strings.Builder, block map[string]any) {
	name, _ := block["name"].(string)
	if name == "" {
		name = "tool_use"
	}
	b.WriteString("- ")
	b.WriteString(name)
	if id, ok := block["id"].(string); ok && id != "" {
		b.WriteString(" `")
		b.WriteString(id)
		b.WriteString("`")
	}
	if input, ok := block["input"]; ok {
		if payload, err := json.Marshal(input); err == nil && len(payload) > 0 {
			b.WriteByte('\n')
			b.WriteString("```json\n")
			b.Write(payload)
			b.WriteString("\n```")
		}
	}
	b.WriteByte('\n')
}

func writeToolResult(b *strings.Builder, block map[string]any) {
	b.WriteString("- tool_result")
	if id, ok := block["tool_use_id"].(string); ok && id != "" {
		b.WriteString(" `")
		b.WriteString(id)
		b.WriteString("`")
	}
	b.WriteByte('\n')
	writeBlockText(b, block)
}

func tokensFromValue(value any) int {
	switch v := value.(type) {
	case map[string]any:
		total := 0
		for key, item := range v {
			if strings.HasSuffix(key, "tokens") {
				total += intFromJSONNumber(item)
				continue
			}
			total += tokensFromValue(item)
		}
		return total
	case []any:
		total := 0
		for _, item := range v {
			total += tokensFromValue(item)
		}
		return total
	default:
		return 0
	}
}

func intFromJSONNumber(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
