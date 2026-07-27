package bridge

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"lark-agent-bridge/internal/agent/contextusage"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/reply"
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

// toolCall 保存一次完整工具调用的人读信息:名称、命令、输出
type toolCall struct {
	ID     string
	Name   string
	Cmd    string
	Output string
}

type cleanActivityUpdate struct {
	Number int
	At     time.Time
	Kind   card.SegmentKind
	Text   string
	Tool   *toolCall
}

// format 将工具调用渲染成人读 markdown 格式
func (t *toolCall) format() string {
	if t == nil || (t.ID == "" && t.Name == "" && t.Cmd == "" && t.Output == "") {
		return ""
	}
	var b strings.Builder
	name := t.Name
	if name == "" {
		name = "工具调用"
	}
	b.WriteString("**")
	b.WriteString(name)
	b.WriteString("**")
	if t.Cmd != "" {
		b.WriteString("\n```\n")
		b.WriteString(t.Cmd)
		b.WriteString("\n```")
	}
	if t.Output != "" {
		b.WriteString("\n**输出**:\n```\n")
		b.WriteString(clampToolOutput(t.Output))
		b.WriteString("\n```")
	}
	return b.String()
}

// formatCompact keeps short commands and outputs on one detail line for the
// append-clean timeline. Complex values fall back to fenced blocks.
func (t *toolCall) formatCompact() string {
	if t == nil || (t.ID == "" && t.Name == "" && t.Cmd == "" && t.Output == "") {
		return ""
	}
	name := strings.TrimSpace(t.Name)
	if name == "" {
		name = "工具调用"
	}
	cmd := strings.TrimSpace(t.Cmd)
	output := strings.TrimSpace(clampToolOutput(t.Output))
	cmdInline, cmdCompact := compactInlineToolValue(cmd, 160)
	outputInline, outputCompact := compactInlineToolValue(output, 240)

	var b strings.Builder
	fmt.Fprintf(&b, "**%s**", name)
	if cmd != "" && output != "" && cmdCompact && outputCompact {
		fmt.Fprintf(&b, "\n%s → %s", cmdInline, outputInline)
		return b.String()
	}
	if cmd != "" {
		b.WriteByte('\n')
		if cmdCompact {
			b.WriteString(cmdInline)
		} else {
			fmt.Fprintf(&b, "```\n%s\n```", cmd)
		}
	}
	if output != "" {
		b.WriteString("\n输出：")
		if outputCompact {
			b.WriteString(outputInline)
		} else {
			fmt.Fprintf(&b, "\n```\n%s\n```", output)
		}
	}
	return b.String()
}

func compactInlineToolValue(text string, maxRunes int) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsAny(text, "\r\n`") || utf8.RuneCountInString(text) > maxRunes {
		return "", false
	}
	return "`" + text + "`", true
}

type agentCardStream struct {
	mu               sync.Mutex
	renderMu         sync.Mutex
	renderer         card.Renderer
	refProvider      interface{ RenderRef() session.RenderRef }
	clock            streamClock
	previewPolicy    PreviewPolicy
	previewTail      bool
	previewTimer     streamTimer
	previewGen       uint64
	previewPending   bool
	previewDisabled  bool
	heartbeatEvery   time.Duration
	heartbeatTimer   streamTimer
	heartbeatGen     uint64
	lastFlush        time.Time
	lastFlushedRunes int
	contentRevision  uint64
	lastFlushedRev   uint64
	metaRevision     uint64
	lastFlushedMeta  uint64
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
	stopGrantID      string
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

	// append-clean-card 三段布局:思考/工具滚动显示「最后两次」,但用计数告诉用户
	// 背后累计了多少轮。recentThoughts 保留最新两轮 COT;recentTools 保留最新两次
	// 工具调用的可读三元组(不是 raw segment text);thoughtRounds/toolRounds
	// 是累计轮次/次数,渲染成折叠区标题的「× N」。仅在 append-clean-card 生效。
	//
	// 关键约束:
	//   * 思考:同一轮的 delta 增量拼接到 currentThought,直到该轮 AssistantSnapshot 到达;
	//     snapshot 会带该轮完整 thinking(权威版),用它覆盖 currentThought,避免"delta 已拼过、
	//     snapshot 又拼一次"的重复。轮次结束时把 currentThought 推入 recentThoughts,最多保留2条。
	//   * 工具:同一 tool_use.id 的多次 segment(占位 input → 完整 input)覆盖 command,
	//     不 append;tool_result(同 id)覆盖 output。切换到新 id 才 toolRounds++ 并推入 recentTools,最多保留2条。
	recentThoughts   []string
	currentThought   strings.Builder
	thoughtRounds    int
	thoughtRoundOpen bool
	toolRounds       int
	seenToolIDs      map[string]bool
	// 工具调用的可读三元组,渲染时拼成人读格式(`**Name**` + command 围栏 + 输出围栏)。
	// currentTool 是正在进行中的工具调用,完成后推入 recentTools。
	recentTools          []*toolCall
	currentTool          *toolCall
	cleanUpdates         []cleanActivityUpdate
	nextCleanUpdate      int
	thoughtUpdateCount   int
	toolUpdateCount      int
	currentThoughtUpdate int
	lastThoughtUpdate    int
	currentToolUpdate    int

	// ctxDir 是本轮 agent 对应的 context-usage sidecar 目录(Claude/Codex 各一)。
	// 流式期间用来在 Handle 中读本轮 fresh 用量,让"进行中"卡片能显示实时 ctx。
	// 空串表示 Config 未配置或 agent 不支持,Handle 里跳过读取。
	ctxDir string
}

// maxVisibleSegments 控制三段布局中思考/工具块最多保留最近几段/几次,实现滚动效果。
const maxVisibleSegments = 2

const cleanTimelineSeparator = "────────────────────"

// usesCleanCardLayout identifies reply modes that present one run as the
// append-clean three-section card. latest-card differs only in its policy:
// it rehydrates and replaces the previous run's CardKit card.
func usesCleanCardLayout(mode config.ReplyMode) bool {
	return mode == config.ReplyModeAppendCleanCard || mode == config.ReplyModeLatestCard
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
	continuationPreview := input.EffectiveReplyMode() == config.ReplyModeAppend && input.EffectiveAppendOverflowMode() == config.AppendOverflowModeContinueCard
	if continuationPreview {
		policy.MaxPreviewRunes = reply.ContinuationPreviewMaxRunes(service.Config.CardMaxChars)
	}
	if policy.Interval <= 0 {
		policy.Interval = time.Second
	}
	heartbeatEvery := service.Config.CardHeartbeatEvery
	if heartbeatEvery <= 0 {
		heartbeatEvery = 5 * time.Second
	}
	ctxDir := service.contextUsageDirForRun(sess.Key.Agent, input.AgentHome)
	stopGrantID, grantErr := service.issueActionGrant(input.Sender, sess.Key.ChatID, sessionID, "stop", "", time.Now().Add(defaultActionGrantTTL))
	stopVisible := true
	if grantErr != nil {
		stopVisible = false
		service.Audit.Record("system", "action_grant_issue_failed", sessionID, "action=stop error="+grantErr.Error())
	}
	return &agentCardStream{
		renderer:       renderer,
		refProvider:    refProvider,
		clock:          clock,
		previewPolicy:  policy,
		heartbeatEvery: heartbeatEvery,
		previewTail:    continuationPreview,
		sessionID:      sessionID,
		replyTo:        input.ReplyToMessageID,
		replyInThread:  input.ConversationMode == config.ConversationModeTopic,
		replyMode:      input.EffectiveReplyMode(),
		startedAt:      startedAt,
		status:         "running",
		activity:       streamActivityReasoning,
		meta:           service.metaForRunWithDir(sess, input, ctxDir),
		totalBefore:    sess.Tokens,
		stopVisible:    stopVisible,
		stopGrantID:    stopGrantID,
		ctxDir:         ctxDir,
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
		s.resetHeartbeatLocked()
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
	previousMeta := s.meta
	previousActivity := s.activity
	if update.AgentSessionID != "" && s.meta.SessionID == "" {
		// 首轮流式期间就把 agent 抛出来的 session id 反映到卡片 meta,让首轮卡片也能显示 Session ID。
		// durable session 的回写仍由 Run 结束后的 postRunMeta 路径负责,不受此处影响。
		s.meta.SessionID = update.AgentSessionID
	}
	if update.Model != "" {
		s.meta.Model = update.Model
		s.meta.ModelInfo.Actual = update.Model
		s.meta.ModelPending = false
	}
	if update.Tokens > 0 {
		s.meta.RunTokens += update.Tokens
		s.meta.Tokens = s.meta.RunTokens
		s.meta.TotalTokens = s.totalBefore + s.meta.RunTokens
	}
	// 流式期间只采纳本轮落盘的 sidecar。首卡明确显示同步中，绝不把上一轮的
	// 模型或上下文占用伪装成本轮值。
	s.refreshContextUsageLocked()
	metadataChanged := s.meta != previousMeta
	if metadataChanged {
		s.metaRevision++
	}
	if update.Activity != "" {
		s.activity = update.Activity
	}
	if update.ProgressSnapshot && s.replyMode != config.ReplyModeAppend {
		// Claude 的 assistant 文本会先以正文 snapshot 到达；后续工具活动证明它是
		// 可展示的执行进展。clean/latest 在此移除正文副本，append 则保留原位内联文本。
		s.answer.Reset()
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
		if s.replyMode != config.ReplyModeAppend || assistantSnapshot {
			continue
		}
		s.ordered = appendOrderedSegment(s.ordered, segment, update.Incremental)
	}
	if s.replyMode == config.ReplyModeAppend && assistantSnapshot {
		s.ordered = replaceOrderedAnswerSnapshot(s.ordered, update.Segments, s.orderedPartialAt, s.orderedPartial)
		s.orderedPartialAt = 0
		s.orderedPartial = false
	}
	if usesCleanCardLayout(s.replyMode) {
		s.updateCleanSectionsLocked(update.Segments, update.Incremental, assistantSnapshot, update.ProgressSnapshot)
	}
	activityChanged := update.Activity != "" && update.Activity != previousActivity
	if update.AnswerSnapshot || len(update.Segments) > 0 || activityChanged {
		s.contentRevision++
	}
	if !update.AnswerSnapshot && !hasVisibleOrderedSegments(update.Segments) && !activityChanged && !metadataChanged {
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
	s.meta.CtxApprox = meta.CtxApprox
	s.meta.CtxPending = meta.CtxPending
	s.meta.CtxUsedPercent = meta.CtxUsedPercent
	s.meta.CtxTokens = meta.CtxTokens
	s.meta.CtxWindow = meta.CtxWindow
	if meta.WorkDir != "" {
		s.meta.WorkDir = meta.WorkDir
	}
	// The developer row may warm its update-manifest cache after the first
	// streaming frame. Adopt the terminal snapshot so the same reply can show a
	// newly discovered version instead of waiting for the next user message.
	if s.meta.ShowMetaRowDeveloper || meta.ShowMetaRowDeveloper {
		if meta.Version != "" {
			s.meta.Version = meta.Version
		}
		s.meta.DeveloperMode = meta.DeveloperMode
		s.meta.LatestVersion = meta.LatestVersion
	}
	if result.Model != "" {
		s.meta.Model = result.Model
		s.meta.ModelInfo.Actual = result.Model
		s.meta.ModelPending = false
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
		s.mergeFinalSegmentsLocked(result.Segments, result.AnswerSegments, result.ProgressSegments, result.OrderedSegments)
	}
	if status == "stopped" {
		s.stopVisible = true
	}
	s.closed = true
	s.cancelHeartbeatLocked()
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
	s.cancelHeartbeatLocked()
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

// refreshContextUsageLocked reads the per-session context-usage sidecar (freshness
// gated by s.startedAt) and, if a fresh occupancy is available, overwrites the
// running card's Ctx* fields. Must be called with s.mu held. Idempotent and cheap:
// each call is one small file read + JSON decode against a known path.
// refreshContextUsageLocked accepts only telemetry written after this run
// started. Keeping prior-run values off the running card is intentional: an
// explicit syncing state is more truthful than a plausible but stale model or
// context percentage.
func (s *agentCardStream) refreshContextUsageLocked() {
	if s.ctxDir == "" || s.meta.SessionID == "" {
		return
	}
	u := contextusage.ReadAfter(s.ctxDir, s.meta.SessionID, s.startedAt)
	if !u.OK {
		return
	}
	s.meta.CtxOK = true
	s.meta.CtxApprox = false
	s.meta.CtxPending = false
	s.meta.CtxUsedPercent = u.UsedPercent
	s.meta.CtxTokens = u.TotalTokens
	s.meta.CtxWindow = u.ContextWindow
	if model := strings.TrimSpace(u.Model); model != "" {
		s.meta.Model = model
		s.meta.ModelInfo.Actual = model
		s.meta.ModelPending = false
	}
	if s.meta.ModelInfo.Actual != "" && strings.TrimSpace(u.ReasoningEffort) != "" {
		s.meta.ModelInfo.Effort = u.ReasoningEffort
	}
}

func (s *Service) metaForRun(sess session.Session, input session.Input) card.Meta {
	return s.metaForRunWithDir(sess, input, s.contextUsageDirForRun(sess.Key.Agent, input.AgentHome))
}

func (s *Service) metaForRunWithDir(sess session.Session, input session.Input, ctxDir string) card.Meta {
	meta, _ := s.metaFromSessionWithDir(sess, ctxDir)
	// A new card starts before this run has a session ID or a fresh sidecar. Do
	// not carry over session.Model or a previous sidecar: both describe another
	// run. The requested model/effort remains useful as an explicit startup hint.
	meta.Model = ""
	meta.ModelInfo = card.ModelInfo{Requested: input.RequestedModel, Effort: input.RequestedEffort}
	meta.ModelPending = true
	meta.CtxOK = false
	meta.CtxApprox = false
	meta.CtxPending = true
	meta.CtxUsedPercent = 0
	meta.CtxTokens = 0
	meta.CtxWindow = 0
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
	if s.contentRevision <= s.lastFlushedRev && s.metaRevision <= s.lastFlushedMeta {
		return false, 0
	}
	currentRunes := s.previewRuneCountLocked()
	// Whitespace-only deltas may be significant once visible text follows, but
	// they must not replace the initial progress card with an empty body.
	if currentRunes == 0 && s.lastFlushedRunes == 0 && s.metaRevision <= s.lastFlushedMeta {
		return false, 0
	}
	now := s.clock.Now()
	delta := currentRunes - s.lastFlushedRunes
	if delta < 0 {
		delta = 0
	}
	firstVisible := s.lastFlushedRev == 0
	firstMetadata := s.lastFlushedMeta == 0 && s.metaRevision > 0
	contentShrank := currentRunes < s.lastFlushedRunes
	deadlineReached := s.lastFlush.IsZero() || !now.Before(s.lastFlush.Add(s.previewPolicy.Interval))
	if force || firstVisible || firstMetadata || contentShrank || (delta >= s.previewPolicy.MinDeltaRunes && deadlineReached) || deadlineReached {
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
	if s.contentRevision <= s.lastFlushedRev && s.metaRevision <= s.lastFlushedMeta {
		s.mu.Unlock()
		return
	}
	s.previewPending = true
	s.mu.Unlock()
	_ = s.flushPreview(generation)
}

func (s *agentCardStream) resetHeartbeatLocked() {
	s.cancelHeartbeatLocked()
	if s.closed || s.stopping || s.previewDisabled || s.heartbeatEvery <= 0 {
		return
	}
	generation := s.heartbeatGen
	s.heartbeatTimer = s.clock.AfterFunc(s.heartbeatEvery, func() { s.onHeartbeat(generation) })
}

func (s *agentCardStream) cancelHeartbeatLocked() {
	s.heartbeatGen++
	if s.heartbeatTimer != nil {
		s.heartbeatTimer.Stop()
		s.heartbeatTimer = nil
	}
}

func (s *agentCardStream) onHeartbeat(generation uint64) {
	s.mu.Lock()
	if s.closed || s.stopping || s.previewDisabled || generation != s.heartbeatGen {
		s.mu.Unlock()
		return
	}
	s.heartbeatTimer = nil
	if s.previewPending {
		s.resetHeartbeatLocked()
		s.mu.Unlock()
		return
	}
	previousMeta := s.meta
	// A quiet model may still flush its context sidecar. Heartbeats are the
	// independent refresh path when no AgentStreamUpdate arrives.
	s.refreshContextUsageLocked()
	if s.meta != previousMeta {
		s.metaRevision++
	}
	event := s.eventLocked(false)
	// Heartbeats must keep the same bounded body as ordinary previews. They
	// still force a full CardKit update for the elapsed-time header, but sending
	// the accumulated body here would make the next normal preview shrink the
	// card back to CardPreviewMaxChars.
	if s.previewTail {
		event = limitPreviewEventTail(event, s.previewPolicy.MaxPreviewRunes)
	} else {
		event = limitPreviewEvent(event, s.previewPolicy.MaxPreviewRunes)
	}
	event.ForceFullUpdate = true
	if len(event.Segments) == 0 {
		event.Message = "任务仍在运行…"
	}
	s.mu.Unlock()

	_ = s.renderHeartbeat(generation, event)
	s.mu.Lock()
	if generation != s.heartbeatGen {
		s.mu.Unlock()
		return
	}
	// A transient CardKit failure must not permanently disable liveness updates.
	s.resetHeartbeatLocked()
	s.mu.Unlock()
}

func (s *agentCardStream) flushPreview(generation uint64) error {
	s.mu.Lock()
	if s.closed || s.previewDisabled || !s.previewPending || generation != s.previewGen {
		s.mu.Unlock()
		return nil
	}
	event := s.eventLocked(false)
	if s.previewTail {
		event = limitPreviewEventTail(event, s.previewPolicy.MaxPreviewRunes)
	} else {
		event = limitPreviewEvent(event, s.previewPolicy.MaxPreviewRunes)
	}
	flushedRunes := s.previewRuneCountLocked()
	flushedRevision := s.contentRevision
	flushedMetaRevision := s.metaRevision
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
		s.cancelHeartbeatLocked()
		s.mu.Unlock()
		return err
	}
	s.lastFlush = s.clock.Now()
	s.lastFlushedRunes = flushedRunes
	s.lastFlushedRev = flushedRevision
	s.lastFlushedMeta = flushedMetaRevision
	s.resetHeartbeatLocked()
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

func (s *agentCardStream) renderHeartbeat(generation uint64, event card.Event) error {
	s.renderMu.Lock()
	defer s.renderMu.Unlock()
	s.mu.Lock()
	if s.closed || s.stopping || s.previewDisabled || generation != s.heartbeatGen {
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

func limitPreviewEventTail(event card.Event, maxRunes int) card.Event {
	if maxRunes <= 0 {
		return event
	}
	total := 0
	for _, segment := range event.Segments {
		total += len([]rune(segment.Text))
	}
	if total <= maxRunes {
		return event
	}
	const notice = "_较早过程已省略_"
	noticeRunes := []rune(notice)
	if maxRunes <= len(noticeRunes) {
		event.Segments = []card.Segment{{Kind: card.SegmentText, Text: string(noticeRunes[:maxRunes])}}
		return event
	}
	remaining := maxRunes - len(noticeRunes) - 2
	reversed := make([]card.Segment, 0, len(event.Segments))
	for i := len(event.Segments) - 1; i >= 0 && remaining > 0; i-- {
		segment := event.Segments[i]
		runes := []rune(segment.Text)
		if len(runes) > remaining {
			segment.Text = string(runes[len(runes)-remaining:])
			runes = runes[:remaining]
		}
		remaining -= len(runes)
		reversed = append(reversed, segment)
	}
	segments := make([]card.Segment, 1, len(reversed)+1)
	segments[0] = card.Segment{Kind: card.SegmentText, Text: notice}
	for i := len(reversed) - 1; i >= 0; i-- {
		segments = append(segments, reversed[i])
	}
	event.Segments = segments
	return event
}

func (s *agentCardStream) appendSegmentLocked(segment card.Segment, incremental bool) {
	appendSegmentToBuilders(segment, incremental, &s.answer, &s.thought, &s.tools)
}

// updateCleanSectionsLocked 维护 append-clean-card 三段布局的「滚动显示最后两次 + 计数」状态。
//
// 思考:同一轮的 delta 拼到 currentThought(流式期间逐字出现);该轮 AssistantSnapshot 到达时,
// 用 snapshot 里的完整 thought 权威覆盖 currentThought,消除「delta 已拼过、snapshot 又拼一次」
// 的重复。thoughtRounds 在 assistantSnapshot 结算时 +1;完成的 thought 推入 recentThoughts,
// 最多保留 maxVisibleSegments(2) 条,实现滚动效果。下一段 delta 视为新一轮,先 Reset currentThought。
//
// 工具:tool_use 与 tool_result 共享 tool_use.id(claudeToolResultMeta),按 id 归到一次调用。
// 同 id 的 tool_use segment 多次到达(占位 input → 完整 input)覆盖 command,不 append;
// 同 id 的 tool_result 覆盖 output。新 id 才 toolRounds++ 并初始化 currentTool;
// 工具调用完成(result 到达)后推入 recentTools,最多保留 maxVisibleSegments(2) 条。
// 用 ToolMeta 提取 Name / Summary(command 摘要)作为人读展示,不再暴露 call_xxx id 与 raw JSON。
func (s *agentCardStream) updateCleanSectionsLocked(segments []card.Segment, incremental, assistantSnapshot, progressSnapshot bool) {
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
	// 下一段 thought 视为新一轮(触发推入历史+Reset)。避免"两轮 thinking 全部累积成一大段"。
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
			// 一整段快照)一定视为新一段;incremental delta 若发生在
			// 「上一段已闭合」(thoughtRoundOpen=false,比如上一段被 tool 段兜底关轮了)也是新一段。
			if !incremental || !s.thoughtRoundOpen {
				// 新一轮思考开始:如果当前有未闭合的思考,先收尾推入历史
				s.finalizeCurrentThoughtLocked()
				s.currentThought.Reset()
				s.thoughtRoundOpen = true
				s.currentThoughtUpdate = s.startCleanUpdateLocked(card.SegmentThought)
			}
			if incremental {
				s.currentThought.WriteString(segment.Text)
			} else {
				s.currentThought.WriteString(strings.TrimSpace(segment.Text))
			}
			s.updateCleanThoughtLocked(s.currentThoughtUpdate, s.currentThought.String())
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
			// 新调用:切换 currentTool,计数 +1。旧调用(同 id)只更新对应字段,不重复计数。
			if id == "" || s.currentTool == nil || id != s.currentTool.ID {
				// 新工具调用开始:如果有未完成的旧工具,先收尾推入历史
				if s.currentTool != nil && (s.currentTool.Cmd != "" || s.currentTool.Output != "") {
					s.pushRecentToolLocked(s.currentTool)
				}
				if id == "" || !s.seenToolIDs[id] {
					s.toolRounds++
					if id != "" {
						s.seenToolIDs[id] = true
					}
				}
				s.currentTool = &toolCall{
					ID:   id,
					Name: strings.TrimSpace(name),
				}
				s.currentToolUpdate = s.startCleanUpdateLocked(card.SegmentTool)
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
					s.currentTool.Cmd = cmd
				}
				if strings.TrimSpace(name) != "" {
					s.currentTool.Name = strings.TrimSpace(name)
				}
				s.updateCleanToolLocked(s.currentToolUpdate, s.currentTool)
			case "result":
				// tool_result:从 raw text 提取 ``` 围栏内的实际输出,不显示 "- tool_result call_xxx" 头。
				out := extractToolResultOutput(segment.Text)
				if out == "" {
					out = strings.TrimSpace(segment.Text)
				}
				if isError {
					out = "❌ " + out
				}
				s.currentTool.Output = out
				s.updateCleanToolLocked(s.currentToolUpdate, s.currentTool)
				// 收到 result 说明工具调用完成,推入历史
				s.pushRecentToolLocked(s.currentTool)
				s.currentTool = nil
				s.currentToolUpdate = 0
			default:
				if text := strings.TrimSpace(segment.Text); text != "" {
					s.currentTool.Cmd = text
				}
				s.updateCleanToolLocked(s.currentToolUpdate, s.currentTool)
			}
		}
	}
	if assistantSnapshot {
		alreadyCounted := false
		if latestSnapshotThought != "" {
			if progressSnapshot {
				// 每条 Claude progress 都是独立的 assistant message；它不是上一轮
				// thinking snapshot 的权威覆盖，必须新开 timeline update。
				s.finalizeCurrentThoughtLocked()
				s.currentThought.Reset()
				s.thoughtRoundOpen = true
				s.currentThoughtUpdate = s.startCleanUpdateLocked(card.SegmentThought)
				s.currentThought.WriteString(latestSnapshotThought)
				s.updateCleanThoughtLocked(s.currentThoughtUpdate, latestSnapshotThought)
			} else if s.thoughtRoundOpen {
				// 正常情况:思考轮还开着,用 snapshot 权威版覆盖 currentThought
				s.currentThought.Reset()
				s.currentThought.WriteString(latestSnapshotThought)
				s.updateCleanThoughtLocked(s.currentThoughtUpdate, latestSnapshotThought)
			} else if len(s.recentThoughts) > 0 {
				// 兜底情况:之前 sawTool 已经把不完整的思考推入历史了,直接替换最后一条为权威版
				s.recentThoughts[len(s.recentThoughts)-1] = latestSnapshotThought
				s.updateCleanThoughtLocked(s.lastThoughtUpdate, latestSnapshotThought)
				alreadyCounted = true // 推入时没计数,现在补上
			} else {
				// 异常兜底:没有历史也没开轮,直接写入
				s.currentThought.Reset()
				s.currentThought.WriteString(latestSnapshotThought)
				s.thoughtRoundOpen = true
				s.currentThoughtUpdate = s.startCleanUpdateLocked(card.SegmentThought)
				s.updateCleanThoughtLocked(s.currentThoughtUpdate, latestSnapshotThought)
			}
		}
		if s.thoughtRoundOpen {
			// snapshot 到达说明这一轮思考结束,收尾推入历史
			s.finalizeCurrentThoughtLocked()
			s.thoughtRounds++
			s.thoughtRoundOpen = false
		} else if alreadyCounted {
			// 兜底路径下已经推入过历史,这里补上计数
			s.thoughtRounds++
		}
	} else if sawTool && s.thoughtRoundOpen {
		// 视觉兜底:tool 段暗示 thinking 阶段结束(一轮 flow 是 thinking → tool_use)。
		// 收尾推入历史,只关轮不 +1(计数交给 assistantSnapshot 主路径或 finalizeCleanSectionsLocked 的
		// result 兜底);下一段 thought 触发新轮逻辑,避免"两轮 thinking 累积成一大段"。
		// 注意:这里推入的是不完整的思考,后面如果 snapshot 到来会用权威版覆盖最近一条,不会重复计数。
		s.finalizeCurrentThoughtLocked()
		s.thoughtRoundOpen = false
	}
}

// finalizeCurrentThoughtLocked 将当前正在进行的思考收尾,推入 recentThoughts 并维护最多保留 maxVisibleSegments 条
func (s *agentCardStream) finalizeCurrentThoughtLocked() {
	text := strings.TrimSpace(s.currentThought.String())
	if text == "" {
		return
	}
	s.recentThoughts = append(s.recentThoughts, text)
	s.updateCleanThoughtLocked(s.currentThoughtUpdate, text)
	s.lastThoughtUpdate = s.currentThoughtUpdate
	s.currentThoughtUpdate = 0
	// 超过上限时去掉最旧的一条,保持滚动窗口
	if len(s.recentThoughts) > maxVisibleSegments {
		s.recentThoughts = s.recentThoughts[len(s.recentThoughts)-maxVisibleSegments:]
	}
	s.currentThought.Reset()
}

// pushRecentToolLocked 将完成的工具调用推入 recentTools,维护最多保留 maxVisibleSegments 条
func (s *agentCardStream) pushRecentToolLocked(t *toolCall) {
	if t == nil {
		return
	}
	// 复制一份避免后续修改影响历史
	copied := &toolCall{
		ID:     t.ID,
		Name:   t.Name,
		Cmd:    t.Cmd,
		Output: t.Output,
	}
	if copied.format() == "" {
		return
	}
	s.recentTools = append(s.recentTools, copied)
	s.updateCleanToolLocked(s.currentToolUpdate, copied)
	// 超过上限时去掉最旧的一条,保持滚动窗口
	if len(s.recentTools) > maxVisibleSegments {
		s.recentTools = s.recentTools[len(s.recentTools)-maxVisibleSegments:]
	}
}

func (s *agentCardStream) startCleanUpdateLocked(kind card.SegmentKind) int {
	s.nextCleanUpdate++
	switch kind {
	case card.SegmentThought:
		s.thoughtUpdateCount++
	case card.SegmentTool:
		s.toolUpdateCount++
	}
	number := s.nextCleanUpdate
	s.cleanUpdates = append(s.cleanUpdates, cleanActivityUpdate{
		Number: number,
		At:     s.clock.Now(),
		Kind:   kind,
	})
	sameKind := 0
	for _, update := range s.cleanUpdates {
		if update.Kind == kind {
			sameKind++
		}
	}
	if sameKind > maxVisibleSegments {
		for i, update := range s.cleanUpdates {
			if update.Kind == kind {
				s.cleanUpdates = append(s.cleanUpdates[:i], s.cleanUpdates[i+1:]...)
				break
			}
		}
	}
	return number
}

func (s *agentCardStream) updateCleanThoughtLocked(number int, text string) {
	for i := range s.cleanUpdates {
		if s.cleanUpdates[i].Number == number {
			s.cleanUpdates[i].Text = strings.TrimSpace(text)
			return
		}
	}
}

func (s *agentCardStream) updateCleanToolLocked(number int, tool *toolCall) {
	if tool == nil {
		return
	}
	for i := range s.cleanUpdates {
		if s.cleanUpdates[i].Number == number {
			copied := *tool
			s.cleanUpdates[i].Tool = &copied
			return
		}
	}
}

func (s *agentCardStream) formatCleanTimelineLocked(kind card.SegmentKind) string {
	var b strings.Builder
	visible := 0
	for i := len(s.cleanUpdates) - 1; i >= 0; i-- {
		update := s.cleanUpdates[i]
		if update.Kind != kind {
			continue
		}
		body := strings.TrimSpace(update.Text)
		if update.Tool != nil {
			body = strings.TrimSpace(update.Tool.formatCompact())
		}
		if body == "" {
			continue
		}
		if visible > 0 {
			b.WriteString("\n")
			b.WriteString(cleanTimelineSeparator)
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "**Update #%d · %s** · %s", update.Number, update.At.Format("15:04:05"), body)
		visible++
	}
	return b.String()
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

// formatToolsLocked 把 recentTools + currentTool(正在进行的)按时间顺序渲染成人读 markdown,
// 最近的在最下面。每个工具调用格式: **<Name>**\n```\n<command>\n```\n**输出**:\n```\n<output>\n```
// 缺 name / cmd / output 都会跳过对应段,不显示 call_xxx id、不暴露 raw JSON payload。
// 当工具调用总次数超过 maxVisibleSegments 时,顶部添加省略提示说明仅显示最近2次。
func (s *agentCardStream) formatToolsLocked() string {
	var b strings.Builder
	// 超过2次时顶部添加省略提示
	if s.toolRounds > maxVisibleSegments {
		b.WriteString(fmt.Sprintf("*（已省略前面 %d 次工具调用，仅显示最近 %d 次）*\n\n", s.toolRounds-maxVisibleSegments, maxVisibleSegments))
	}
	// 先渲染历史工具调用
	for _, t := range s.recentTools {
		if formatted := t.format(); formatted != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n---\n\n")
			}
			b.WriteString(formatted)
		}
	}
	// 再渲染正在进行中的工具调用(如果有)
	if s.currentTool != nil {
		if formatted := s.currentTool.format(); formatted != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n---\n\n")
			}
			b.WriteString(formatted)
		}
	}
	return b.String()
}

// formatThoughtsLocked 把 recentThoughts + currentThought(正在进行的)按时间顺序渲染成 markdown,
// 最近的在最下面,段之间用分隔线分开,实现滚动显示效果。
// 当思考总轮次超过 maxVisibleSegments 时,顶部添加省略提示说明仅显示最近2轮。
func (s *agentCardStream) formatThoughtsLocked() string {
	var b strings.Builder
	// 超过2轮时顶部添加省略提示
	if s.thoughtRounds > maxVisibleSegments {
		b.WriteString(fmt.Sprintf("*（已省略前面 %d 轮思考过程，仅显示最近 %d 轮）*\n\n", s.thoughtRounds-maxVisibleSegments, maxVisibleSegments))
	}
	// 先渲染历史思考
	for _, t := range s.recentThoughts {
		if text := strings.TrimSpace(t); text != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n---\n\n")
			}
			b.WriteString(text)
		}
	}
	// 再渲染正在进行中的思考(如果有)
	currentText := strings.TrimSpace(s.currentThought.String())
	if currentText != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString(currentText)
	}
	return b.String()
}

// maxToolOutputRunes 是单次工具调用「输出」段在卡片里的字符预算。
// 远小于卡片整体软上限(LarkCardSoftMaxJSONBytes=28KB / CardMaxChars=12000),
// 目的是:让工具名与命令(信息密度最高、用户最需要看到的部分)永远完整保留,
// 只有冗长输出才被有界省略。否则下游 capacity.fitLarkCard 会用 keepTail 截断
// 整个 tool 段——从头部吃起,反而先牺牲工具名和命令、只留一堆输出(本次修复的 bug)。
const maxToolOutputRunes = 6000

// clampToolOutput 对过长的工具输出做「保头 + 保尾 + 省中间」截断。
// 头尾都保留能同时体现「命令产出的开头」与「结尾/退出状态」,中间用一行省略提示替代。
// 未超预算时原样返回。
func clampToolOutput(output string) string {
	runes := []rune(output)
	if len(runes) <= maxToolOutputRunes {
		return output
	}
	omitted := len(runes) - maxToolOutputRunes
	// 头部占 ~60%,尾部占 ~40%:开头信息通常更重要,但保留结尾以便看到退出/结论。
	head := maxToolOutputRunes * 6 / 10
	tail := maxToolOutputRunes - head
	var b strings.Builder
	b.WriteString(strings.TrimRight(string(runes[:head]), "\n"))
	b.WriteString(fmt.Sprintf("\n\n… （中间省略 %d 字符）…\n\n", omitted))
	b.WriteString(strings.TrimLeft(string(runes[len(runes)-tail:]), "\n"))
	return b.String()
}

func appendOrderedSegment(segments []card.Segment, segment card.Segment, incremental bool) []card.Segment {
	if segment.Text == "" && !isStructuredToolSegment(segment) {
		return segments
	}
	if incremental && segment.Text != "" && len(segments) > 0 && segments[len(segments)-1].Kind == segment.Kind {
		segments[len(segments)-1].Text += segment.Text
		return segments
	}
	return append(segments, segment)
}

func replaceOrderedAnswerSnapshot(segments, snapshot []card.Segment, partialAt int, hasPartial bool) []card.Segment {
	visible := make([]card.Segment, 0, len(snapshot))
	for _, segment := range snapshot {
		if segment.Text != "" || isStructuredToolSegment(segment) {
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
		if isVisibleOrderedSegment(segment) {
			return true
		}
	}
	return false
}

func isVisibleOrderedSegment(segment card.Segment) bool {
	return strings.TrimSpace(segment.Text) != "" || isStructuredToolSegment(segment)
}

func isStructuredToolSegment(segment card.Segment) bool {
	return segment.Kind == card.SegmentTool && segment.Tool != nil
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
func (s *agentCardStream) mergeFinalSegmentsLocked(segments []card.Segment, answerSegments, progressSegments []string, orderedSegments []card.Segment) {
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
	for _, progress := range progressSegments {
		appendToBuilder(&thought, progress)
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
	if usesCleanCardLayout(s.replyMode) {
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
		merged := make([]card.Segment, 0, len(segments)+len(progressSegments)+len(orderedSegments))
		for _, seg := range segments {
			if seg.Kind == card.SegmentThought {
				merged = append(merged, seg)
			}
			if seg.Kind == card.SegmentTool && !hasOrderedTool {
				merged = append(merged, seg)
			}
		}
		for _, progress := range progressSegments {
			if text := strings.TrimSpace(progress); text != "" {
				merged = append(merged, card.Segment{Kind: card.SegmentThought, Text: text})
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
// 「滚动显示最后两次」:最近 maxVisibleSegments 轮 COT;最近 maxVisibleSegments 次工具调用的
// name/command/output 从对应 tool_use / tool_result segment 的 ToolMeta 与围栏文本提取。
// 计数以流式期间累计的 thoughtRounds/toolRounds 为准(含全过程,更准);流式没跑过时(直出终态、计数为 0)
// 从终态 segments 兜底计数,避免「× N」缺失。渲染由 segmentsLocked 的 formatThoughtsLocked/formatToolsLocked 完成。
func (s *agentCardStream) finalizeCleanSectionsLocked(segments []card.Segment) {
	rebuildTimeline := len(s.cleanUpdates) == 0
	var thoughts []string
	thoughtCount := 0
	toolIDs := make(map[string]bool)
	uniqueTools := 0
	var tools []*toolCall
	var currentTool *toolCall

	// 先收尾流式期间可能未完成的当前思考/工具
	s.finalizeCurrentThoughtLocked()
	if s.currentTool != nil && (s.currentTool.Cmd != "" || s.currentTool.Output != "") {
		s.pushRecentToolLocked(s.currentTool)
		s.currentTool = nil
	}

	// 遍历一遍收集所有 thought 和 tool,最后取最近 maxVisibleSegments 条
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		switch segment.Kind {
		case card.SegmentThought:
			thoughts = append(thoughts, text)
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
			// 新工具调用
			if id == "" || currentTool == nil || id != currentTool.ID {
				if currentTool != nil && (currentTool.Cmd != "" || currentTool.Output != "") {
					tools = append(tools, currentTool)
				}
				currentTool = &toolCall{
					ID:   id,
					Name: name,
				}
			}
			switch phase {
			case "use":
				cmd := summary
				if cmd == "" {
					cmd = extractToolUseCommand(segment.Text)
				}
				if cmd != "" {
					currentTool.Cmd = cmd
				}
				if name != "" {
					currentTool.Name = name
				}
			case "result":
				out := extractToolResultOutput(segment.Text)
				if out == "" {
					out = text
				}
				if isError {
					out = "❌ " + out
				}
				currentTool.Output = out
				// result 到达说明工具调用完成
				tools = append(tools, currentTool)
				currentTool = nil
			}
		}
	}
	// 收尾最后一个未完成的工具
	if currentTool != nil && (currentTool.Cmd != "" || currentTool.Output != "") {
		tools = append(tools, currentTool)
	}

	// 重置状态
	s.recentThoughts = nil
	s.currentThought.Reset()
	s.recentTools = nil
	s.currentTool = nil

	// 只保留最后 maxVisibleSegments 条思考
	if len(thoughts) > 0 {
		start := len(thoughts) - maxVisibleSegments
		if start < 0 {
			start = 0
		}
		s.recentThoughts = thoughts[start:]
	}

	// 只保留最后 maxVisibleSegments 次工具调用
	if len(tools) > 0 {
		start := len(tools) - maxVisibleSegments
		if start < 0 {
			start = 0
		}
		s.recentTools = tools[start:]
	}

	if s.thoughtRounds == 0 {
		s.thoughtRounds = thoughtCount
	}
	if s.toolRounds == 0 {
		s.toolRounds = uniqueTools
	}
	if rebuildTimeline {
		s.rebuildCleanTimelineLocked(segments)
	}
}

// rebuildCleanTimelineLocked covers agents that emit only a terminal result.
// Tool use/result segments sharing an ID remain one update, matching the
// streaming path; timestamps are necessarily the terminal observation time.
func (s *agentCardStream) rebuildCleanTimelineLocked(segments []card.Segment) {
	toolUpdates := make(map[string]int)
	toolCalls := make(map[string]*toolCall)
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		switch segment.Kind {
		case card.SegmentThought:
			if text == "" {
				continue
			}
			number := s.startCleanUpdateLocked(card.SegmentThought)
			s.updateCleanThoughtLocked(number, text)
		case card.SegmentTool:
			id, name, summary, phase, isError := "", "", "", "", false
			if segment.Tool != nil {
				id = segment.Tool.ID
				name = strings.TrimSpace(segment.Tool.Name)
				summary = strings.TrimSpace(segment.Tool.Summary)
				phase = segment.Tool.Phase
				isError = segment.Tool.IsError
			}
			key := id
			if key == "" {
				key = fmt.Sprintf("anonymous-%d", s.nextCleanUpdate+1)
			}
			tool := toolCalls[key]
			number := toolUpdates[key]
			if tool == nil {
				tool = &toolCall{ID: id, Name: name}
				toolCalls[key] = tool
				number = s.startCleanUpdateLocked(card.SegmentTool)
				toolUpdates[key] = number
			}
			switch phase {
			case "use":
				if summary == "" {
					summary = extractToolUseCommand(segment.Text)
				}
				if summary != "" {
					tool.Cmd = summary
				}
				if name != "" {
					tool.Name = name
				}
			case "result":
				output := extractToolResultOutput(segment.Text)
				if output == "" {
					output = text
				}
				if isError {
					output = "❌ " + output
				}
				tool.Output = output
			default:
				tool.Cmd = text
			}
			s.updateCleanToolLocked(number, tool)
		}
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
	// 三段布局(append-clean-card/latest-card)在终态隐藏 stop 按钮:终态卡片只需展示答案与折叠面板,
	// "已完成 ✗" 灰按钮占位无意义,还会打断视觉。运行/停止请求中(status=running)仍显示。
	if usesCleanCardLayout(s.replyMode) && s.status != "running" {
		stopVisible = false
	}
	return card.Event{
		Type:             s.statusEventTypeLocked(),
		SessionID:        s.sessionID,
		ReplyToMessageID: s.replyTo,
		ReplyInThread:    s.replyInThread,
		Segments:         segments,
		Meta:             s.meta,
		StopButton:       card.StopButton{Visible: stopVisible, Disabled: stopDisabled, GrantID: s.stopGrantID, ActionSessionID: s.sessionID},
		Message:          message,
		HeaderTitle:      s.headerTitleLocked(),
		HeaderTemplate:   s.headerTemplateLocked(),
		Streaming:        s.status == "running",
		Activity:         s.activity,
		// v2:过程折叠区在运行期固定折叠,不随 activity 开合,保持骨架稳定以便 native 流式命中。
		ProcessExpanded:    false,
		ToolCallCount:      s.toolCallCount,
		OrderedLayout:      s.replyMode == config.ReplyModeAppend,
		ThreeSectionLayout: usesCleanCardLayout(s.replyMode),
		// 三段布局:思考默认展开(用户要求),终态折叠让最终答案更清爽;工具恒默认折叠。
		ThoughtExpanded:     usesCleanCardLayout(s.replyMode) && s.status == "running",
		ToolsExpanded:       false,
		ThoughtRoundCount:   s.thoughtRounds,
		ToolRoundCount:      s.toolRounds,
		ThoughtOmittedCount: max(0, s.thoughtRounds-maxVisibleSegments),
		ToolOmittedCount:    max(0, s.toolRounds-maxVisibleSegments),
	}
}

func (s *agentCardStream) segmentsLocked() []card.Segment {
	if s.replyMode == config.ReplyModeAppend {
		return s.withStopRequestedNoticeLocked(s.orderedSegmentsLocked())
	}
	// append-clean-card/latest-card 在两个独立折叠区中分别倒序显示最新两条思考/工具 update。
	thoughtText := s.thought.String()
	toolsText := s.tools.String()
	if usesCleanCardLayout(s.replyMode) {
		thoughtText = s.formatCleanTimelineLocked(card.SegmentThought)
		toolsText = s.formatCleanTimelineLocked(card.SegmentTool)
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
	if text := strings.TrimSpace(s.thought.String()); text != "" && !containsOrderedKind(s.ordered, card.SegmentThought) {
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
		if strings.TrimSpace(segment.Text) != "" || isStructuredToolSegment(segment) {
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
