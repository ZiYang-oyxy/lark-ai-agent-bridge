package bridge

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

const (
	streamActivityReasoning = "reasoning"
	streamActivityTool      = "tool"
	streamActivityAnswering = "answering"
	stopRequestedNotice     = "已请求停止当前任务；排队输入将继续执行。"
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
	ordered          []card.Segment
	orderedPartialAt int
	orderedPartial   bool
	toolCallCount    int
	stopping         bool
	stopRequested    bool

	// append-clean-card 三段布局:思考/工具只显示「最新一次」,但用计数告诉用户
	// 背后累计了多少轮。cleanThought 是最新一轮 COT;latestToolName/Command/Output
	// 是最新一次工具调用的可读三元组(不是 raw segment text);thoughtRounds/toolRounds
	// 是累计轮次/次数,渲染成折叠区标题的「× N」。仅在 append-clean-card 生效。
	//
	// 关键约束:
	//   * 思考:同一轮的 delta 增量拼接到 cleanThought,直到该轮 AssistantSnapshot 到达;
	//     snapshot 会带该轮完整 thinking(权威版),用它覆盖 cleanThought,避免"delta 已拼过、
	//     snapshot 又拼一次"的重复。
	//   * 工具:同一 tool_use.id 的多次 segment(占位 input → 完整 input)覆盖 command,
	//     不 append;tool_result(同 id)覆盖 output。切换到新 id 才 toolRounds++ + Reset。
	cleanThought     strings.Builder
	thoughtRounds    int
	thoughtRoundOpen bool
	toolRounds       int
	seenToolIDs      map[string]bool
	// 最新一次工具调用的三元组;渲染时拼成人读格式(`**Name**` + command 围栏 + 输出围栏)。
	latestToolID     string
	latestToolName   string
	latestToolCmd    string
	latestToolOutput string
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
		meta:          service.metaForRun(sess, input),
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
	previousActivity := s.activity
	if update.AgentSessionID != "" && s.meta.SessionID == "" {
		// 首轮流式期间就把 agent 抛出来的 session id 反映到卡片 meta,让首轮卡片也能显示 Session ID。
		// durable session 的回写仍由 Run 结束后的 postRunMeta 路径负责,不受此处影响。
		s.meta.SessionID = update.AgentSessionID
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
	assistantSnapshot := update.AssistantSnapshot || update.AnswerSnapshot
	partialUpdate := update.PartialMessage || update.Incremental
	if s.replyMode == config.ReplyModeAppend && !assistantSnapshot && partialUpdate && !s.orderedPartial && hasVisibleOrderedSegments(update.Segments) {
		s.orderedPartialAt = len(s.ordered)
		s.orderedPartial = true
	}
	for _, segment := range update.Segments {
		s.appendSegmentLocked(segment, update.Incremental)
		if s.replyMode != config.ReplyModeAppend || assistantSnapshot || segment.Kind == card.SegmentThought {
			continue
		}
		s.ordered = appendOrderedSegment(s.ordered, segment, update.Incremental)
	}
	if s.replyMode == config.ReplyModeAppend && assistantSnapshot {
		s.ordered = replaceOrderedAnswerSnapshot(s.ordered, update.Segments, s.orderedPartialAt, s.orderedPartial)
		s.orderedPartialAt = 0
		s.orderedPartial = false
	}
	if s.replyMode == config.ReplyModeAppendCleanCard {
		s.updateCleanSectionsLocked(update.Segments, update.Incremental, assistantSnapshot)
	}
	activityChanged := update.Activity != "" && update.Activity != previousActivity
	if update.AnswerSnapshot || len(update.Segments) > 0 || activityChanged {
		s.contentRevision++
	}
	if !update.AnswerSnapshot && !hasVisibleOrderedSegments(update.Segments) && !activityChanged {
		s.mu.Unlock()
		return
	}
	immediate, generation := s.requestPreviewLocked(false)
	s.mu.Unlock()
	if immediate {
		_ = s.flushPreview(generation)
	}
}

func (s *agentCardStream) Finish(status string, meta card.Meta, result AgentRunResult) (card.Event, error) {
	return s.FinishTransformed(status, meta, result, nil)
}

func (s *agentCardStream) FinishTransformed(status string, meta card.Meta, result AgentRunResult, transform func(card.Event) card.Event) (card.Event, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return card.Event{}, nil
	}
	s.status = status
	if meta.Agent != "" {
		s.meta.Agent = meta.Agent
	}
	// Session ID 兜底:流式期间 Handle 已尽力从 update.AgentSessionID 采纳;
	// 终态再从 postRunMeta 返回的 meta 和 result 兜底一次,确保首轮终态卡不缺 Session ID。
	if s.meta.SessionID == "" {
		if meta.SessionID != "" {
			s.meta.SessionID = meta.SessionID
		} else if result.AgentSessionID != "" {
			s.meta.SessionID = result.AgentSessionID
		}
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
	s.meta.CtxOK = meta.CtxOK
	s.meta.CtxUsedPercent = meta.CtxUsedPercent
	s.meta.CtxTokens = meta.CtxTokens
	s.meta.CtxWindow = meta.CtxWindow
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
	if s.replyMode == config.ReplyModeAppend {
		s.mergeAppendFinalSegmentsLocked(result)
	} else if len(result.Segments) > 0 {
		s.mergeFinalSegmentsLocked(result.Segments, result.AnswerSegments, result.OrderedSegments)
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
	if transform != nil {
		event = transform(event)
	}
	s.mu.Unlock()
	return event, s.renderEvent(event)
}

func (s *agentCardStream) RenderTerminalUpdate(event card.Event) error {
	if !terminalStreamEvent(event.Type) || event.Streaming {
		return fmt.Errorf("terminal card update requires a non-streaming terminal event")
	}
	return s.renderEvent(event)
}

func terminalStreamEvent(eventType string) bool {
	switch eventType {
	case "result", "error", "stopped", "interrupted":
		return true
	default:
		return false
	}
}

// markStopping 标记本轮已请求停止:后续运行期 Handle/preview 不再渲染 Streaming=true 的中间帧,
// 避免停止卡发出后被排队的 preview 覆盖成"运行中"造成闪烁。终态 Finish 仍可正常收敛。
func (s *agentCardStream) markStopping() {
	s.mu.Lock()
	s.markStoppingLocked()
	s.mu.Unlock()
}

func (s *agentCardStream) markStoppingLocked() {
	s.stopping = true
	s.previewGen++
	if s.previewTimer != nil {
		s.previewTimer.Stop()
		s.previewTimer = nil
	}
	s.previewPending = false
}

func (s *agentCardStream) requestStop() card.Event {
	s.mu.Lock()
	s.stopRequested = true
	s.markStoppingLocked()
	previousStatus := s.status
	s.status = "stopped"
	event := s.eventLocked(false)
	s.status = previousStatus
	s.mu.Unlock()
	return event
}

func (s *Service) metaForRun(sess session.Session, input session.Input) card.Meta {
	meta := s.metaFromSession(sess)
	meta.Model = ""
	if sess.Key.Agent == agent.Codex {
		// Bridge deliberately does not select Codex model or reasoning effort.
		// The chosen executable and its environment own that configuration.
		meta.ModelInfo = card.ModelInfo{}
	} else {
		meta.ModelInfo = card.ModelInfo{Requested: input.RequestedModel, Effort: input.RequestedEffort}
	}
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

// updateCleanSectionsLocked 维护 append-clean-card 三段布局的「只显示最新一次 + 计数」状态。
//
// 思考:同一轮的 delta 拼到 cleanThought(流式期间逐字出现);该轮 AssistantSnapshot 到达时,
// 用 snapshot 里的完整 thought 权威覆盖 cleanThought,消除「delta 已拼过、snapshot 又拼一次」
// 的重复。thoughtRounds 在 assistantSnapshot 结算时 +1;下一段 delta 视为新一轮,先 Reset。
//
// 工具:tool_use 与 tool_result 共享 tool_use.id(claudeToolResultMeta),按 id 归到一次调用。
// 同 id 的 tool_use segment 多次到达(占位 input → 完整 input)覆盖 command,不 append;
// 同 id 的 tool_result 覆盖 output。新 id 才 toolRounds++ 并 Reset 三元组。
// 用 ToolMeta 提取 Name / Summary(command 摘要)作为人读展示,不再暴露 call_xxx id 与 raw JSON。
func (s *agentCardStream) updateCleanSectionsLocked(segments []card.Segment, incremental, assistantSnapshot bool) {
	if s.seenToolIDs == nil {
		s.seenToolIDs = make(map[string]bool)
	}
	// snapshot 到来时,收集该 update 里的最后一段完整 thought(权威版)用于覆盖 delta 中间态。
	// 一轮 assistant message 内即使有多段 thinking,视觉上只保留最后一段(用户视角=同一次思考的最终版)。
	latestSnapshotThought := ""
	if assistantSnapshot {
		for _, segment := range segments {
			if segment.Kind == card.SegmentThought {
				if t := strings.TrimSpace(segment.Text); t != "" {
					latestSnapshotThought = t
				}
			}
		}
	}
	// 兜底轮次边界:某些 Claude vendor 不发标准 assistant message snapshot,`assistantSnapshot`
	// 永不会为 true。此时依赖"tool 到来即上一轮 thought 结束"的语义:一轮 flow 是
	// thinking → tool_use → tool_result;见到 tool 段就把之前累积的 thought 视为一轮定稿,
	// 下一段 thought 视为新一轮(触发 Reset)。避免"两轮 thinking 全部累积成一大段"。
	sawTool := false
	for _, segment := range segments {
		if segment.Kind == card.SegmentTool {
			sawTool = true
			break
		}
	}
	for _, segment := range segments {
		switch segment.Kind {
		case card.SegmentThought:
			if segment.Text == "" || assistantSnapshot {
				// snapshot 帧的 thought 交给下面一次性权威覆盖,不再在这里逐段拼。
				continue
			}
			// 判定新 thought 段:非 incremental(即 content_block_start 的完整 thinking 首帧或
			// 一整段快照)一定视为新一段,直接 Reset 覆盖旧内容;incremental delta 若发生在
			// 「上一段已闭合」(thoughtRoundOpen=false,比如上一段被 tool 段兜底关轮了)也是新一段。
			// 这样任何 vendor 发出的多段 thinking 都恒只保留最新一段,避免"两句思考并列"。
			if !incremental || !s.thoughtRoundOpen {
				s.cleanThought.Reset()
				s.thoughtRoundOpen = true
			}
			if incremental {
				s.cleanThought.WriteString(segment.Text)
			} else {
				s.cleanThought.WriteString(strings.TrimSpace(segment.Text))
			}
		case card.SegmentTool:
			id := ""
			name := ""
			summary := ""
			phase := ""
			isError := false
			if segment.Tool != nil {
				id = segment.Tool.ID
				name = segment.Tool.Name
				summary = segment.Tool.Summary
				phase = segment.Tool.Phase
				isError = segment.Tool.IsError
			}
			// 新调用:切换 latest*,计数 +1。旧调用(同 id)只更新对应字段,不重复计数。
			if id == "" || id != s.latestToolID {
				if id == "" || !s.seenToolIDs[id] {
					s.toolRounds++
					if id != "" {
						s.seenToolIDs[id] = true
					}
				}
				s.latestToolID = id
				s.latestToolName = strings.TrimSpace(name)
				s.latestToolCmd = strings.TrimSpace(summary)
				s.latestToolOutput = ""
			}
			switch phase {
			case "use":
				// command 优先取 ToolMeta.Summary(toolInputSummary 抽出的 command 字段);
				// Summary 空(例如非 Bash 类工具、或 input 尚未补齐)时,从 raw text 里的 ```json
				// 围栏 parse 出 command / 常见字段,兜底再用剥离头的整段。同一 id 多次到达都覆盖。
				cmd := strings.TrimSpace(summary)
				if cmd == "" {
					cmd = extractToolUseCommand(segment.Text)
				}
				if cmd != "" {
					s.latestToolCmd = cmd
				}
				if strings.TrimSpace(name) != "" {
					s.latestToolName = strings.TrimSpace(name)
				}
			case "result":
				// tool_result:从 raw text 提取 ``` 围栏内的实际输出,不显示 "- tool_result call_xxx" 头。
				out := extractToolResultOutput(segment.Text)
				if out == "" {
					out = strings.TrimSpace(segment.Text)
				}
				if isError {
					out = "❌ " + out
				}
				s.latestToolOutput = out
			}
		}
	}
	if assistantSnapshot {
		if latestSnapshotThought != "" {
			s.cleanThought.Reset()
			s.cleanThought.WriteString(latestSnapshotThought)
			s.thoughtRoundOpen = true
		}
		if s.thoughtRoundOpen {
			s.thoughtRounds++
			s.thoughtRoundOpen = false
		}
	} else if sawTool && s.thoughtRoundOpen {
		// 视觉兜底:tool 段暗示 thinking 阶段结束(一轮 flow 是 thinking → tool_use)。
		// 只关轮不 +1(计数交给 assistantSnapshot 主路径或 finalizeCleanSectionsLocked 的
		// result 兜底);下一段 thought 触发 Reset 覆盖旧内容,避免"两轮 thinking 累积成一大段"。
		s.thoughtRoundOpen = false
	}
}

// extractToolUseCommand 从 writeToolUse 生成的 markdown 里提出人读的 command。
// 输入形如 "- Bash `id`\n\n```json\n{...}\n```"。先剥 ```json 围栏拿 JSON,parse 出 command /
// file_path / path / query / url 等常见字段;失败则退回围栏原文(比 raw 段更干净,无 - Name id 头)。
func extractToolUseCommand(raw string) string {
	if raw == "" {
		return ""
	}
	fenceStart := strings.Index(raw, "```")
	if fenceStart < 0 {
		return ""
	}
	rest := raw[fenceStart+3:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	fenceEnd := strings.LastIndex(rest, "```")
	if fenceEnd < 0 {
		return ""
	}
	body := strings.TrimSpace(rest[:fenceEnd])
	// 尝试 parse JSON 抽 command;失败就直接返回围栏内原文(避免 raw JSON 但含 call_id 头)。
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err == nil {
		for _, key := range []string{"command", "file_path", "path", "query", "url"} {
			if v, ok := payload[key]; ok {
				if text, ok := v.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return body
}

// extractToolResultOutput 从 writeToolResult 生成的 markdown 里剥出 ``` 围栏内的原始输出。
// 输入形如:"- tool_result `id`\n\n```\n<output>\n```"。取围栏内内容;找不到围栏时返回空串,
// 让调用方用整段 text 兜底。这样卡片里显示纯输出,不带 "- tool_result call_xxx" 这类壳文本。
func extractToolResultOutput(raw string) string {
	start := strings.Index(raw, "```")
	if start < 0 {
		return ""
	}
	rest := raw[start+3:]
	// 跳过可选的 lang 标签,直到当前行末。
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	end := strings.LastIndex(rest, "```")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimRight(rest[:end], "\n\r\t ")
}

// formatLatestToolLocked 把 latestTool 三元组渲染成人读的 markdown:
// **<Name>**\n\n```\n<command>\n```\n\n**输出**:\n```\n<output>\n```
// 缺 name / cmd / output 都会跳过对应段,不显示 call_xxx id、不暴露 raw JSON payload。
func (s *agentCardStream) formatLatestToolLocked() string {
	if s.latestToolID == "" && s.latestToolName == "" && s.latestToolCmd == "" && s.latestToolOutput == "" {
		return ""
	}
	var b strings.Builder
	name := s.latestToolName
	if name == "" {
		name = "工具调用"
	}
	b.WriteString("**")
	b.WriteString(name)
	b.WriteString("**")
	if s.latestToolCmd != "" {
		b.WriteString("\n\n```\n")
		b.WriteString(s.latestToolCmd)
		b.WriteString("\n```")
	}
	if s.latestToolOutput != "" {
		b.WriteString("\n\n**输出**:\n\n```\n")
		b.WriteString(s.latestToolOutput)
		b.WriteString("\n```")
	}
	return b.String()
}

func appendOrderedSegment(segments []card.Segment, segment card.Segment, incremental bool) []card.Segment {
	if segment.Text == "" || segment.Kind == card.SegmentThought {
		return segments
	}
	if incremental && len(segments) > 0 && segments[len(segments)-1].Kind == segment.Kind {
		segments[len(segments)-1].Text += segment.Text
		return segments
	}
	return append(segments, segment)
}

func replaceOrderedAnswerSnapshot(segments, snapshot []card.Segment, partialAt int, hasPartial bool) []card.Segment {
	visible := make([]card.Segment, 0, len(snapshot))
	for _, segment := range snapshot {
		if segment.Kind != card.SegmentThought && segment.Text != "" {
			visible = append(visible, segment)
		}
	}
	if len(visible) == 0 {
		return segments
	}
	if !hasPartial || partialAt < 0 || partialAt > len(segments) {
		return append(segments, visible...)
	}
	out := make([]card.Segment, 0, partialAt+len(visible))
	out = append(out, segments[:partialAt]...)
	out = append(out, visible...)
	return out
}

func (s *agentCardStream) mergeAppendFinalSegmentsLocked(result AgentRunResult) {
	if len(result.OrderedSegments) > 0 {
		s.ordered = append([]card.Segment(nil), result.OrderedSegments...)
	} else if hasVisibleOrderedSegments(result.Segments) {
		if len(s.ordered) == 0 {
			s.ordered = nil
			for _, segment := range result.Segments {
				s.ordered = appendOrderedSegment(s.ordered, segment, false)
			}
		} else if containsOrderedKind(s.ordered, card.SegmentTool) || countOrderedKind(s.ordered, card.SegmentText) == 1 {
			s.mergeLegacyAppendAnswerLocked(result.Segments)
		}
	}
	for _, segment := range result.Segments {
		if segment.Kind == card.SegmentThought {
			if text := strings.TrimSpace(segment.Text); text != "" {
				s.thought.Reset()
				s.thought.WriteString(text)
			}
			continue
		}
		if segment.Kind == card.SegmentError && !containsOrderedSegment(s.ordered, segment) {
			s.ordered = appendOrderedSegment(s.ordered, segment, false)
		}
	}
}

func countOrderedKind(segments []card.Segment, kind card.SegmentKind) int {
	count := 0
	for _, segment := range segments {
		if segment.Kind == kind && strings.TrimSpace(segment.Text) != "" {
			count++
		}
	}
	return count
}

func (s *agentCardStream) mergeLegacyAppendAnswerLocked(segments []card.Segment) {
	finalAnswer := ""
	for _, segment := range segments {
		if segment.Kind == card.SegmentText && strings.TrimSpace(segment.Text) != "" {
			finalAnswer = segment.Text
		}
	}
	if finalAnswer == "" {
		return
	}
	lastText := -1
	lastTool := -1
	for i, segment := range s.ordered {
		switch segment.Kind {
		case card.SegmentText:
			lastText = i
		case card.SegmentTool:
			lastTool = i
		}
	}
	if lastText > lastTool {
		s.ordered[lastText].Text = finalAnswer
		return
	}
	s.ordered = append(s.ordered, card.Segment{Kind: card.SegmentText, Text: finalAnswer})
}

func containsOrderedKind(segments []card.Segment, kind card.SegmentKind) bool {
	for _, segment := range segments {
		if segment.Kind == kind && strings.TrimSpace(segment.Text) != "" {
			return true
		}
	}
	return false
}

func hasVisibleOrderedSegments(segments []card.Segment) bool {
	for _, segment := range segments {
		if segment.Kind != card.SegmentThought && strings.TrimSpace(segment.Text) != "" {
			return true
		}
	}
	return false
}

func containsOrderedSegment(segments []card.Segment, want card.Segment) bool {
	for _, segment := range segments {
		if segment.Kind == want.Kind && segment.Text == want.Text {
			return true
		}
	}
	return false
}

// mergeFinalSegmentsLocked 用终态结果重建卡片正文。append 保留本次 run
// 的聚合正文；append-clean-card/latest-card 只保留最后一段 assistant
// 回复。runner 错误作为独立块追加，不参与最后一段选取。
// orderedSegments 保留原始 Tool 元信息(finalizeCleanSectionsLocked 需要)。
func (s *agentCardStream) mergeFinalSegmentsLocked(segments []card.Segment, answerSegments []string, orderedSegments []card.Segment) {
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
	if s.replyMode == config.ReplyModeAppendCleanCard {
		// 组装 finalize 源:thought 从 segments;tool 优先从 orderedSegments(Claude stream 只有
		// 这里保留了 Tool meta,segments 里的 SegmentTool 只是合并 markdown 无 meta),
		// orderedSegments 里没有 tool 段时再从 segments 里取(测试路径或 codex path)。
		hasOrderedTool := false
		for _, seg := range orderedSegments {
			if seg.Kind == card.SegmentTool {
				hasOrderedTool = true
				break
			}
		}
		merged := make([]card.Segment, 0, len(segments)+len(orderedSegments))
		for _, seg := range segments {
			if seg.Kind == card.SegmentThought {
				merged = append(merged, seg)
			}
			if seg.Kind == card.SegmentTool && !hasOrderedTool {
				merged = append(merged, seg)
			}
		}
		if hasOrderedTool {
			for _, seg := range orderedSegments {
				if seg.Kind == card.SegmentTool {
					merged = append(merged, seg)
				}
			}
		}
		s.finalizeCleanSectionsLocked(merged)
	}
}

// finalizeCleanSectionsLocked 在终态用本轮完整 result.Segments 重算 append-clean 的
// 「最新一次」:最新一轮 COT = 最后一段 thought;最新一次工具的 name/command/output 从对应
// tool_use / tool_result segment 的 ToolMeta 与围栏文本提取。计数以流式期间累计的
// thoughtRounds/toolRounds 为准(含全过程,更准);流式没跑过时(直出终态、计数为 0)从终态
// segments 兜底计数,避免「× N」缺失。三元组渲染由 segmentsLocked 的 formatLatestToolLocked 完成。
func (s *agentCardStream) finalizeCleanSectionsLocked(segments []card.Segment) {
	var latestThought string
	thoughtCount := 0
	toolIDs := make(map[string]bool)
	uniqueTools := 0
	// 遍历一遍即定位最后一段 thought 与最后一次 tool 的 use/result。
	s.latestToolID = ""
	s.latestToolName = ""
	s.latestToolCmd = ""
	s.latestToolOutput = ""
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		switch segment.Kind {
		case card.SegmentThought:
			latestThought = text
			thoughtCount++
		case card.SegmentTool:
			id := ""
			name := ""
			summary := ""
			phase := ""
			isError := false
			if segment.Tool != nil {
				id = segment.Tool.ID
				name = strings.TrimSpace(segment.Tool.Name)
				summary = strings.TrimSpace(segment.Tool.Summary)
				phase = segment.Tool.Phase
				isError = segment.Tool.IsError
			}
			if id != "" && !toolIDs[id] {
				toolIDs[id] = true
				uniqueTools++
			} else if id == "" {
				uniqueTools++
			}
			if id == "" || id != s.latestToolID {
				s.latestToolID = id
				s.latestToolName = name
				s.latestToolCmd = ""
				s.latestToolOutput = ""
			}
			switch phase {
			case "use":
				cmd := summary
				if cmd == "" {
					cmd = extractToolUseCommand(segment.Text)
				}
				if cmd != "" {
					s.latestToolCmd = cmd
				}
				if name != "" {
					s.latestToolName = name
				}
			case "result":
				out := extractToolResultOutput(segment.Text)
				if out == "" {
					out = text
				}
				if isError {
					out = "❌ " + out
				}
				s.latestToolOutput = out
			}
		}
	}
	s.cleanThought.Reset()
	s.cleanThought.WriteString(latestThought)
	if s.thoughtRounds == 0 {
		s.thoughtRounds = thoughtCount
	}
	if s.toolRounds == 0 {
		s.toolRounds = uniqueTools
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
		message = "思考中…"
	}
	stopVisible := s.stopVisible && (s.status == "running" || s.status == "stopped" || s.status == "completed" || s.status == "failed")
	stopDisabled := s.status != "running"
	// 三段布局(append-clean-card)在终态隐藏 stop 按钮:终态卡片只需展示答案与折叠面板,
	// "已完成 ✗" 灰按钮占位无意义,还会打断视觉。运行/停止请求中(status=running)仍显示。
	if s.replyMode == config.ReplyModeAppendCleanCard && s.status != "running" {
		stopVisible = false
	}
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
		ProcessExpanded:    false,
		ToolCallCount:      s.toolCallCount,
		OrderedLayout:      s.replyMode == config.ReplyModeAppend,
		ThreeSectionLayout: s.replyMode == config.ReplyModeAppendCleanCard,
		// 三段布局:思考默认展开(用户要求),终态折叠让最终答案更清爽;工具恒默认折叠。
		ThoughtExpanded:   s.replyMode == config.ReplyModeAppendCleanCard && s.status == "running",
		ToolsExpanded:     false,
		ThoughtRoundCount: s.thoughtRounds,
		ToolRoundCount:    s.toolRounds,
	}
}

func (s *agentCardStream) segmentsLocked() []card.Segment {
	if s.replyMode == config.ReplyModeAppend {
		return s.withStopRequestedNoticeLocked(s.orderedSegmentsLocked())
	}
	// append-clean-card 三段布局只显示最新一次 COT / 工具调用,取 clean* 裁剪结果;
	// 其余模式(latest-card 等走此分支的)保持旧的累积 thought / tools。
	thoughtText := s.thought.String()
	toolsText := s.tools.String()
	if s.replyMode == config.ReplyModeAppendCleanCard {
		thoughtText = s.cleanThought.String()
		toolsText = s.formatLatestToolLocked()
	}
	var segments []card.Segment
	if text := stripTrailingBotSignature(s.answer.String()); strings.TrimSpace(text) != "" {
		segments = append(segments, card.Segment{Kind: card.SegmentText, Text: text})
	}
	if text := strings.TrimSpace(thoughtText); text != "" {
		segments = append(segments, card.Segment{Kind: card.SegmentThought, Text: text})
	}
	if text := strings.TrimSpace(toolsText); text != "" {
		segments = append(segments, card.Segment{Kind: card.SegmentTool, Text: text})
	}
	return s.withStopRequestedNoticeLocked(segments)
}

func (s *agentCardStream) withStopRequestedNoticeLocked(segments []card.Segment) []card.Segment {
	if !s.stopRequested || s.status != "stopped" {
		return segments
	}
	for _, segment := range segments {
		if segment.Kind == card.SegmentText && strings.TrimSpace(segment.Text) == stopRequestedNotice {
			return segments
		}
	}
	return append(segments, card.Segment{Kind: card.SegmentText, Text: stopRequestedNotice})
}

func (s *agentCardStream) orderedSegmentsLocked() []card.Segment {
	segments := make([]card.Segment, 0, len(s.ordered)+1)
	if text := strings.TrimSpace(s.thought.String()); text != "" {
		segments = append(segments, card.Segment{Kind: card.SegmentThought, Text: text})
	}
	segments = append(segments, s.ordered...)
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i].Kind != card.SegmentText {
			continue
		}
		segments[i].Text = stripTrailingBotSignature(segments[i].Text)
		break
	}
	out := segments[:0]
	for _, segment := range segments {
		if strings.TrimSpace(segment.Text) != "" {
			out = append(out, segment)
		}
	}
	return out
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
