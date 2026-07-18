package bridge

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

const (
	streamActivityReasoning = "reasoning"
	streamActivityTool      = "tool"
	streamActivityAnswering = "answering"
)

type PreviewPolicy struct {
	MinDeltaRunes   int
	MaxPreviewRunes int
	Interval        time.Duration
}

type streamTimer interface {
	Stop() bool
}

type streamClock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) streamTimer
}

type realStreamClock struct{}

func (realStreamClock) Now() time.Time { return time.Now() }
func (realStreamClock) AfterFunc(delay time.Duration, fn func()) streamTimer {
	return time.AfterFunc(delay, fn)
}

type agentCardStream struct {
	mu               sync.Mutex
	renderMu         sync.Mutex
	renderer         card.Renderer
	refProvider      interface{ RenderRef() session.RenderRef }
	clock            streamClock
	previewPolicy    PreviewPolicy
	previewTimer     streamTimer
	previewGen       uint64
	previewPending   bool
	previewDisabled  bool
	lastFlush        time.Time
	lastFlushedRunes int
	sessionID        string
	replyTo          string
	replyInThread    bool
	startedAt        time.Time
	status           string
	activity         string
	meta             card.Meta
	totalBefore      int
	stopVisible      bool
	closed           bool
	answer           strings.Builder
	thought          strings.Builder
	tools            strings.Builder
}

func newAgentCardStream(service *Service, sessionID string, sess session.Session, input session.Input) *agentCardStream {
	return newAgentCardStreamWithRenderer(service, sessionID, sess, input, service.Cards, nil)
}

func newAgentCardStreamWithRenderer(service *Service, sessionID string, sess session.Session, input session.Input, renderer card.Renderer, refProvider interface{ RenderRef() session.RenderRef }) *agentCardStream {
	return newAgentCardStreamWithClock(service, sessionID, sess, input, renderer, refProvider, realStreamClock{})
}

func newAgentCardStreamWithClock(service *Service, sessionID string, sess session.Session, input session.Input, renderer card.Renderer, refProvider interface{ RenderRef() session.RenderRef }, clock streamClock) *agentCardStream {
	if clock == nil {
		clock = realStreamClock{}
	}
	startedAt := input.Time
	if startedAt.IsZero() {
		startedAt = clock.Now()
	}
	policy := PreviewPolicy{
		MinDeltaRunes:   service.Config.CardMinDeltaChars,
		MaxPreviewRunes: service.Config.CardPreviewMaxChars,
		Interval:        service.Config.CardUpdateEvery,
	}
	if policy.MinDeltaRunes <= 0 {
		policy.MinDeltaRunes = 30
	}
	if policy.MaxPreviewRunes <= 0 {
		policy.MaxPreviewRunes = 2000
	}
	if policy.Interval <= 0 {
		policy.Interval = time.Second
	}
	return &agentCardStream{
		renderer:      renderer,
		refProvider:   refProvider,
		clock:         clock,
		previewPolicy: policy,
		sessionID:     sessionID,
		replyTo:       input.ReplyToMessageID,
		replyInThread: input.ConversationMode == config.ConversationModeTopic,
		startedAt:     startedAt,
		status:        "running",
		activity:      streamActivityReasoning,
		meta:          metaForRun(sess, input),
		totalBefore:   sess.Tokens,
		stopVisible:   true,
	}
}

func (s *agentCardStream) RenderRef() *session.RenderRef {
	if s.refProvider == nil {
		return nil
	}
	ref := s.refProvider.RenderRef()
	if ref.CardID == "" {
		return nil
	}
	return &ref
}

func (s *agentCardStream) Start() error {
	s.mu.Lock()
	event := s.eventLocked(true)
	s.mu.Unlock()
	err := s.renderEvent(event)
	if err == nil {
		s.mu.Lock()
		s.lastFlush = s.clock.Now()
		s.mu.Unlock()
	}
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
	immediate, generation := s.requestPreviewLocked(false)
	s.mu.Unlock()
	if immediate {
		_ = s.flushPreview(generation)
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
	s.previewGen++
	if s.previewTimer != nil {
		s.previewTimer.Stop()
		s.previewTimer = nil
	}
	s.previewPending = false
	event := s.eventLocked(false)
	s.mu.Unlock()
	return event, s.renderEvent(event)
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
	immediate, generation := s.requestPreviewLocked(true)
	s.mu.Unlock()
	if immediate {
		return s.flushPreview(generation)
	}
	return nil
}

func (s *agentCardStream) requestPreviewLocked(force bool) (bool, uint64) {
	if s.closed || s.previewDisabled || s.previewPending {
		return false, 0
	}
	currentRunes := s.previewRuneCountLocked()
	if currentRunes <= s.lastFlushedRunes {
		return false, 0
	}
	now := s.clock.Now()
	delta := currentRunes - s.lastFlushedRunes
	firstVisible := s.lastFlushedRunes == 0
	deadlineReached := s.lastFlush.IsZero() || !now.Before(s.lastFlush.Add(s.previewPolicy.Interval))
	if force || firstVisible || (delta >= s.previewPolicy.MinDeltaRunes && deadlineReached) || deadlineReached {
		s.cancelPreviewTimerLocked()
		s.previewPending = true
		return true, s.previewGen
	}
	if s.previewTimer == nil {
		delay := s.lastFlush.Add(s.previewPolicy.Interval).Sub(now)
		if delay < 0 {
			delay = 0
		}
		s.previewGen++
		generation := s.previewGen
		s.previewTimer = s.clock.AfterFunc(delay, func() { s.onPreviewTimer(generation) })
	}
	return false, 0
}

func (s *agentCardStream) cancelPreviewTimerLocked() {
	s.previewGen++
	if s.previewTimer != nil {
		s.previewTimer.Stop()
		s.previewTimer = nil
	}
}

func (s *agentCardStream) onPreviewTimer(generation uint64) {
	s.mu.Lock()
	if s.closed || s.previewDisabled || s.previewPending || generation != s.previewGen {
		s.mu.Unlock()
		return
	}
	s.previewTimer = nil
	if s.previewRuneCountLocked() <= s.lastFlushedRunes {
		s.mu.Unlock()
		return
	}
	s.previewPending = true
	s.mu.Unlock()
	_ = s.flushPreview(generation)
}

func (s *agentCardStream) flushPreview(generation uint64) error {
	s.mu.Lock()
	if s.closed || s.previewDisabled || !s.previewPending || generation != s.previewGen {
		s.mu.Unlock()
		return nil
	}
	event := limitPreviewEvent(s.eventLocked(false), s.previewPolicy.MaxPreviewRunes)
	flushedRunes := s.previewRuneCountLocked()
	s.mu.Unlock()

	err := s.renderPreview(generation, event)
	s.mu.Lock()
	if generation != s.previewGen {
		s.mu.Unlock()
		return err
	}
	s.previewPending = false
	if err != nil {
		s.previewDisabled = true
		s.cancelPreviewTimerLocked()
		s.mu.Unlock()
		return err
	}
	s.lastFlush = s.clock.Now()
	s.lastFlushedRunes = flushedRunes
	immediate, nextGeneration := s.requestPreviewLocked(false)
	s.mu.Unlock()
	if immediate {
		return s.flushPreview(nextGeneration)
	}
	return nil
}

func (s *agentCardStream) renderEvent(event card.Event) error {
	s.renderMu.Lock()
	defer s.renderMu.Unlock()
	return s.renderer.Render(event)
}

func (s *agentCardStream) renderPreview(generation uint64, event card.Event) error {
	s.renderMu.Lock()
	defer s.renderMu.Unlock()
	s.mu.Lock()
	if s.closed || s.previewDisabled || !s.previewPending || generation != s.previewGen {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.renderer.Render(event)
}

func (s *agentCardStream) previewRuneCountLocked() int {
	total := 0
	for _, segment := range s.segmentsLocked() {
		total += len([]rune(segment.Text))
	}
	return total
}

func limitPreviewEvent(event card.Event, maxRunes int) card.Event {
	if maxRunes <= 0 {
		return event
	}
	remaining := maxRunes
	segments := make([]card.Segment, 0, len(event.Segments))
	for _, segment := range event.Segments {
		if remaining == 0 {
			break
		}
		runes := []rune(segment.Text)
		if len(runes) > remaining {
			segment.Text = string(runes[:remaining])
			runes = runes[:remaining]
		}
		remaining -= len(runes)
		segments = append(segments, segment)
	}
	event.Segments = segments
	return event
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
		ReplyInThread:    s.replyInThread,
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
	elapsed := s.clock.Now().Sub(s.startedAt).Round(time.Second)
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
