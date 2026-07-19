package bridge

import (
	"fmt"
	"regexp"
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
	contentRevision  uint64
	lastFlushedRev   uint64
	sessionID        string
	replyTo          string
	replyInThread    bool
	replyMode        config.ReplyMode
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
	toolCallCount    int
	stopping         bool
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
		replyMode:     input.EffectiveReplyMode(),
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
	if s.closed || s.stopping {
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
	if update.AnswerSnapshot {
		s.answer.Reset()
	}
	for _, segment := range update.Segments {
		s.appendSegmentLocked(segment, update.Incremental)
	}
	if update.AnswerSnapshot || len(update.Segments) > 0 {
		s.contentRevision++
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
	if result.ToolCallCount > 0 {
		s.toolCallCount = result.ToolCallCount
	}
	if len(result.Segments) > 0 {
		s.mergeFinalSegmentsLocked(result.Segments, result.AnswerSegments)
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

// markStopping 标记本轮已请求停止:后续运行期 Handle/preview 不再渲染 Streaming=true 的中间帧,
// 避免停止卡发出后被排队的 preview 覆盖成"运行中"造成闪烁。终态 Finish 仍可正常收敛。
func (s *agentCardStream) markStopping() {
	s.mu.Lock()
	s.stopping = true
	s.previewGen++
	if s.previewTimer != nil {
		s.previewTimer.Stop()
		s.previewTimer = nil
	}
	s.previewPending = false
	s.mu.Unlock()
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
	if s.closed || s.stopping || s.previewDisabled || s.previewPending {
		return false, 0
	}
	if s.contentRevision <= s.lastFlushedRev {
		return false, 0
	}
	currentRunes := s.previewRuneCountLocked()
	// Whitespace-only deltas may be significant once visible text follows, but
	// they must not replace the initial progress card with an empty body.
	if currentRunes == 0 && s.lastFlushedRunes == 0 {
		return false, 0
	}
	now := s.clock.Now()
	delta := currentRunes - s.lastFlushedRunes
	if delta < 0 {
		delta = 0
	}
	firstVisible := s.lastFlushedRev == 0
	contentShrank := currentRunes < s.lastFlushedRunes
	deadlineReached := s.lastFlush.IsZero() || !now.Before(s.lastFlush.Add(s.previewPolicy.Interval))
	if force || firstVisible || contentShrank || (delta >= s.previewPolicy.MinDeltaRunes && deadlineReached) || deadlineReached {
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
	if s.contentRevision <= s.lastFlushedRev {
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
	flushedRevision := s.contentRevision
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
	s.lastFlushedRev = flushedRevision
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

func (s *agentCardStream) appendSegmentLocked(segment card.Segment, incremental bool) {
	appendSegmentToBuilders(segment, incremental, &s.answer, &s.thought, &s.tools)
}

// mergeFinalSegmentsLocked 用终态结果重建卡片正文。append 保留本次 run
// 的聚合正文；append-clean-card/latest-card 只保留最后一段 assistant
// 回复。runner 错误作为独立块追加，不参与最后一段选取。
func (s *agentCardStream) mergeFinalSegmentsLocked(segments []card.Segment, answerSegments []string) {
	var aggregateAnswer strings.Builder
	var thought strings.Builder
	var tools strings.Builder
	var errorBlock strings.Builder
	for _, segment := range segments {
		switch segment.Kind {
		case card.SegmentThought:
			appendToBuilder(&thought, segment.Text)
		case card.SegmentTool:
			appendToBuilder(&tools, segment.Text)
		case card.SegmentError:
			appendToBuilder(&errorBlock, "**Error**\n"+strings.TrimSpace(segment.Text))
		default:
			appendToBuilder(&aggregateAnswer, segment.Text)
		}
	}

	finalAnswer := strings.TrimSpace(aggregateAnswer.String())
	if s.replyMode == config.ReplyModeAppendCleanCard || s.replyMode == config.ReplyModeLatestCard {
		if last := lastNonEmpty(answerSegments); last != "" {
			finalAnswer = last
		}
	}
	finalAnswer = stripTrailingBotSignature(finalAnswer)
	if errorBlock.Len() > 0 {
		finalAnswer = strings.TrimSpace(finalAnswer + "\n\n" + errorBlock.String())
	}
	if finalAnswer != "" {
		s.answer.Reset()
		s.answer.WriteString(finalAnswer)
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

func appendToBuilder(b *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(text)
}

func lastNonEmpty(segments []string) string {
	for i := len(segments) - 1; i >= 0; i-- {
		if text := strings.TrimSpace(segments[i]); text != "" {
			return text
		}
	}
	return ""
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
		// v2:过程折叠区在运行期固定折叠,不随 activity 开合,保持骨架稳定以便 native 流式命中。
		ProcessExpanded: false,
		ToolCallCount:   s.toolCallCount,
	}
}

func (s *agentCardStream) segmentsLocked() []card.Segment {
	var segments []card.Segment
	if text := stripTrailingBotSignature(s.answer.String()); strings.TrimSpace(text) != "" {
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

func appendSegmentToBuilders(segment card.Segment, incremental bool, answer, thought, tools *strings.Builder) {
	if segment.Text == "" {
		return
	}
	switch segment.Kind {
	case card.SegmentThought:
		if incremental {
			thought.WriteString(segment.Text)
		} else {
			appendToBuilder(thought, segment.Text)
		}
	case card.SegmentTool:
		appendToBuilder(tools, segment.Text)
	case card.SegmentError:
		appendToBuilder(answer, "**Error**\n"+strings.TrimSpace(segment.Text))
	default:
		if incremental {
			answer.WriteString(segment.Text)
		} else {
			appendToBuilder(answer, segment.Text)
		}
	}
}

var botSignatureLineRE = regexp.MustCompile(`(?i)^(?:—{2,}|-{2,})[\t ]+[^\r\n]+-bot$`)

// stripTrailingBotSignature removes only a complete, standalone signature at
// the end of the answer. Similar text in the middle of an answer is preserved.
func stripTrailingBotSignature(text string) string {
	trimmedEnd := strings.TrimRight(text, " \t\r\n")
	lineStart := strings.LastIndex(trimmedEnd, "\n") + 1
	if !botSignatureLineRE.MatchString(strings.TrimSpace(trimmedEnd[lineStart:])) {
		return text
	}
	return strings.TrimRight(trimmedEnd[:lineStart], " \t\r\n")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
