package bridge

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/session"
)

const (
	streamActivityReasoning = "reasoning"
	streamActivityTool      = "tool"
	streamActivityAnswering = "answering"
)

type agentCardStream struct {
	mu          sync.Mutex
	service     *Service
	sessionID   string
	replyTo     string
	startedAt   time.Time
	lastFlush   time.Time
	flushEvery  time.Duration
	status      string
	activity    string
	meta        card.Meta
	totalBefore int
	stopVisible bool
	closed      bool
	answer      strings.Builder
	thought     strings.Builder
	tools       strings.Builder
}

func newAgentCardStream(service *Service, sessionID string, sess session.Session, input session.Input) *agentCardStream {
	startedAt := input.Time
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	flushEvery := service.Config.CardUpdateEvery
	if flushEvery <= 0 {
		flushEvery = time.Second
	}
	return &agentCardStream{
		service:     service,
		sessionID:   sessionID,
		replyTo:     input.ReplyToMessageID,
		startedAt:   startedAt,
		flushEvery:  flushEvery,
		status:      "running",
		activity:    streamActivityReasoning,
		meta:        metaForRun(sess, input),
		totalBefore: sess.Tokens,
		stopVisible: true,
	}
}

func (s *agentCardStream) Start() error {
	_, err := s.render(true)
	return err
}

func (s *agentCardStream) Handle(update AgentStreamUpdate) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if update.ClaudeSessionID != "" {
		// Session id is applied to the durable session after Run returns; streaming
		// updates only need model/tokens for display.
	}
	if update.Model != "" {
		s.meta.Model = update.Model
		s.meta.ModelInfo.Actual = update.Model
	}
	if update.Tokens > 0 {
		s.meta.RunTokens += update.Tokens
		s.meta.Tokens = s.meta.RunTokens
		s.meta.TotalTokens = s.totalBefore + s.meta.RunTokens
	}
	if update.Activity != "" {
		s.activity = update.Activity
	}
	for _, segment := range update.Segments {
		s.appendSegmentLocked(segment)
	}
	shouldFlush := time.Since(s.lastFlush) >= s.flushEvery
	s.mu.Unlock()
	if shouldFlush {
		_ = s.Flush()
	}
}

func (s *agentCardStream) Finish(status string, meta card.Meta, result AgentRunResult) (card.Event, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return card.Event{}, nil
	}
	s.status = status
	if meta.Agent != "" {
		s.meta.Agent = meta.Agent
	}
	if meta.Model != "" && s.meta.ModelInfo == (card.ModelInfo{}) {
		s.meta.Model = meta.Model
	}
	if meta.Tokens > 0 {
		s.meta.TotalTokens = maxInt(s.meta.TotalTokens, meta.Tokens)
	}
	if meta.RunTokens > 0 {
		s.meta.RunTokens = meta.RunTokens
		s.meta.Tokens = meta.RunTokens
	}
	if meta.TotalTokens > 0 {
		s.meta.TotalTokens = maxInt(s.meta.TotalTokens, meta.TotalTokens)
	}
	if meta.WorkDir != "" {
		s.meta.WorkDir = meta.WorkDir
	}
	if result.Model != "" {
		s.meta.Model = result.Model
		s.meta.ModelInfo.Actual = result.Model
	}
	if result.Tokens > 0 {
		s.meta.RunTokens = result.Tokens
		s.meta.Tokens = result.Tokens
		s.meta.TotalTokens = maxInt(s.meta.TotalTokens, s.totalBefore+result.Tokens)
	}
	if len(result.Segments) > 0 {
		s.mergeFinalSegmentsLocked(result.Segments)
	}
	if status == "stopped" {
		s.stopVisible = true
	}
	s.closed = true
	s.mu.Unlock()
	return s.render(false)
}

func metaForRun(sess session.Session, input session.Input) card.Meta {
	meta := metaFromSession(sess)
	meta.Model = ""
	meta.ModelInfo = card.ModelInfo{Requested: input.RequestedModel, Effort: input.RequestedEffort}
	return meta
}

func (s *agentCardStream) Flush() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	_, err := s.render(false)
	return err
}

func (s *agentCardStream) appendSegmentLocked(segment card.Segment) {
	appendSegmentToBuilders(segment, &s.answer, &s.thought, &s.tools)
}

func (s *agentCardStream) mergeFinalSegmentsLocked(segments []card.Segment) {
	var answer strings.Builder
	var thought strings.Builder
	var tools strings.Builder
	for _, segment := range segments {
		appendSegmentToBuilders(segment, &answer, &thought, &tools)
	}
	if answer.Len() > 0 {
		s.answer.Reset()
		s.answer.WriteString(answer.String())
	}
	if thought.Len() > 0 {
		s.thought.Reset()
		s.thought.WriteString(thought.String())
	}
	if tools.Len() > 0 {
		s.tools.Reset()
		s.tools.WriteString(tools.String())
	}
}

func (s *agentCardStream) render(initial bool) (card.Event, error) {
	s.mu.Lock()
	event := s.eventLocked(initial)
	s.lastFlush = time.Now()
	s.mu.Unlock()
	return event, s.service.Cards.Render(event)
}

func (s *agentCardStream) eventLocked(initial bool) card.Event {
	segments := s.segmentsLocked()
	message := ""
	if initial && len(segments) == 0 {
		message = "正在执行 Claude 请求..."
	}
	stopVisible := s.stopVisible && (s.status == "running" || s.status == "stopped" || s.status == "completed" || s.status == "failed")
	stopDisabled := s.status != "running"
	return card.Event{
		Type:             s.statusEventTypeLocked(),
		SessionID:        s.sessionID,
		ReplyToMessageID: s.replyTo,
		Segments:         segments,
		Meta:             s.meta,
		StopButton:       card.StopButton{Visible: stopVisible, Disabled: stopDisabled},
		Message:          message,
		HeaderTitle:      s.headerTitleLocked(),
		HeaderTemplate:   s.headerTemplateLocked(),
		Streaming:        s.status == "running",
		Activity:         s.activity,
		ThoughtExpanded:  s.status == "running" && s.activity == streamActivityReasoning,
		ToolsExpanded:    s.status == "running" && s.activity == streamActivityTool,
	}
}

func (s *agentCardStream) segmentsLocked() []card.Segment {
	var segments []card.Segment
	if text := strings.TrimSpace(s.answer.String()); text != "" {
		segments = append(segments, card.Segment{Kind: card.SegmentText, Text: text})
	}
	if text := strings.TrimSpace(s.thought.String()); text != "" {
		segments = append(segments, card.Segment{Kind: card.SegmentThought, Text: text})
	}
	if text := strings.TrimSpace(s.tools.String()); text != "" {
		segments = append(segments, card.Segment{Kind: card.SegmentTool, Text: text})
	}
	return segments
}

func (s *agentCardStream) statusEventTypeLocked() string {
	switch s.status {
	case "completed":
		return "result"
	case "failed":
		return "error"
	case "stopped":
		return "stopped"
	default:
		return "stream"
	}
}

func (s *agentCardStream) headerTemplateLocked() string {
	switch s.status {
	case "completed":
		return "green"
	case "failed":
		return "red"
	case "stopped":
		return "grey"
	default:
		return "blue"
	}
}

func (s *agentCardStream) headerTitleLocked() string {
	elapsed := time.Since(s.startedAt).Round(time.Second)
	if elapsed < 0 {
		elapsed = 0
	}
	switch s.status {
	case "completed":
		return fmt.Sprintf("✅ 已完成 · ⏱ %s", elapsed)
	case "failed":
		return fmt.Sprintf("❌ 执行失败 · ⏱ %s", elapsed)
	case "stopped":
		return fmt.Sprintf("⏹ 已停止 · ⏱ %s", elapsed)
	}
	switch s.activity {
	case streamActivityTool:
		return fmt.Sprintf("🛠️ 正在执行工具 · ⏱ %s", elapsed)
	case streamActivityAnswering:
		return fmt.Sprintf("✍️ 正在回复 · ⏱ %s", elapsed)
	default:
		return fmt.Sprintf("🧠 正在推理 · ⏱ %s", elapsed)
	}
}

func appendSegmentToBuilders(segment card.Segment, answer, thought, tools *strings.Builder) {
	text := strings.TrimSpace(segment.Text)
	if text == "" {
		return
	}
	target := answer
	switch segment.Kind {
	case card.SegmentThought:
		target = thought
	case card.SegmentTool:
		target = tools
	case card.SegmentError:
		target = answer
		text = "**Error**\n" + text
	}
	if target.Len() > 0 {
		target.WriteString("\n")
	}
	target.WriteString(text)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
