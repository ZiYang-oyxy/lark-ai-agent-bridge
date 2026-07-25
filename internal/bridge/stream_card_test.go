package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

func TestAgentCardStreamHandleAdoptsFirstTurnSessionID(t *testing.T) {
	// 首轮:sess.AgentSessionID 为空,Handle 收到 update 里带 agent session id 时,
	// 卡片 meta 应当立即反映,让首轮流式卡片就能显示 Session ID(而不是等第二轮)。
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat"}, ID: "claude:chat"}
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: time.Now()})
	if stream.meta.SessionID != "" {
		t.Fatalf("initial meta.SessionID = %q, want empty", stream.meta.SessionID)
	}
	stream.Handle(AgentStreamUpdate{AgentSessionID: "sid-first-turn"})
	if stream.meta.SessionID != "sid-first-turn" {
		t.Fatalf("Handle did not adopt session id: meta.SessionID = %q", stream.meta.SessionID)
	}
	// 后续 update 不带 id 时不应清空;已有 id 时也不覆盖为空。
	stream.Handle(AgentStreamUpdate{Tokens: 10})
	if stream.meta.SessionID != "sid-first-turn" {
		t.Fatalf("subsequent Handle without id clobbered SessionID: %q", stream.meta.SessionID)
	}
}

func TestAgentCardStreamFinishBackfillsFirstTurnSessionID(t *testing.T) {
	// 首轮:如果 Handle 期间从未收到 session id(极端场景),终态 Finish 仍应从
	// postRunMeta 返回的 meta 或 result.AgentSessionID 兜底回填,保证首轮终态卡不缺 Session ID。
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat"}, ID: "claude:chat"}
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: time.Now()})

	// (a) meta.SessionID 分支
	terminal, err := stream.Finish("completed", card.Meta{SessionID: "sid-from-meta"}, AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "done"}}})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Meta.SessionID != "sid-from-meta" {
		t.Fatalf("FinishTransformed did not adopt meta.SessionID: got %q", terminal.Meta.SessionID)
	}

	// (b) result.AgentSessionID 兜底分支
	stream2 := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: time.Now()})
	terminal2, err := stream2.Finish("completed", card.Meta{}, AgentRunResult{AgentSessionID: "sid-from-result", Segments: []card.Segment{{Kind: card.SegmentText, Text: "done"}}})
	if err != nil {
		t.Fatal(err)
	}
	if terminal2.Meta.SessionID != "sid-from-result" {
		t.Fatalf("FinishTransformed did not fall back to result.AgentSessionID: got %q", terminal2.Meta.SessionID)
	}
}

func TestAgentCardStreamHandleAdoptsFirstTurnContextUsage(t *testing.T) {
	// 首轮:sess.AgentSessionID=="" 时 metaForRun 拿不到 sidecar,起始 meta.CtxOK=false;
	// Handle 收到本轮 agent session id 后,如果 sidecar 已落盘且 mtime 晚于 runStartedAt,
	// 应立即把 fresh 值填到 s.meta,并置 CtxApprox=false,让"进行中"卡片显示本轮真值。
	dir := t.TempDir()
	cfg := testConfig(t)
	cfg.ClaudeContextUsageDir = dir
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat"}, ID: "claude:chat"}
	runStartedAt := time.Now().Add(-time.Second)
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: runStartedAt})
	if stream.meta.CtxOK {
		t.Fatalf("first-turn initial meta unexpectedly has ctx: %#v", stream.meta)
	}

	// Sidecar 出现(晚于 runStartedAt);
	path := filepath.Join(dir, "fresh-session.json")
	if err := os.WriteFile(path, []byte(`{"session_id":"fresh-session","used_percentage":37,"total_tokens":74000,"context_window_size":200000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// 显式把 mtime 推后,规避低精度文件系统上 mtime <= runStartedAt 导致的 stale 误判。
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	stream.Handle(AgentStreamUpdate{AgentSessionID: "fresh-session"})
	if !stream.meta.CtxOK {
		t.Fatalf("Handle did not adopt fresh sidecar: %#v", stream.meta)
	}
	if stream.meta.CtxApprox {
		t.Fatalf("fresh sidecar should not be marked approx: %#v", stream.meta)
	}
	if stream.meta.CtxUsedPercent != 37 || stream.meta.CtxTokens != 74000 || stream.meta.CtxWindow != 200000 {
		t.Fatalf("fresh ctx values wrong: %#v", stream.meta)
	}
}

func TestAgentCardStreamHandleUpgradesApproxToFresh(t *testing.T) {
	// 续轮:sess.AgentSessionID 已存在,metaForRun 读到上一轮落盘的 sidecar,起始 meta
	// 应该 CtxOK=true + CtxApprox=true(渲染时带 ~ 前缀)。Handle 期间本轮 sidecar
	// 落盘后,refreshContextUsageLocked 应把值升级为本轮 fresh 并把 approx 清零。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sess-a.json"), []byte(`{"session_id":"sess-a","used_percentage":20,"total_tokens":40000,"context_window_size":200000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.ClaudeContextUsageDir = dir
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat"}, ID: "claude:chat", AgentSessionID: "sess-a"}
	runStartedAt := time.Now().Add(-time.Second)
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: runStartedAt})
	if !stream.meta.CtxOK || !stream.meta.CtxApprox || stream.meta.CtxUsedPercent != 20 {
		t.Fatalf("expected approx from prior sidecar, got %#v", stream.meta)
	}

	// 本轮更新 sidecar 到新值,mtime 推后。
	path := filepath.Join(dir, "sess-a.json")
	if err := os.WriteFile(path, []byte(`{"session_id":"sess-a","used_percentage":55,"total_tokens":110000,"context_window_size":200000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	// Handle 一次(update.AgentSessionID 可省,续轮 SessionID 已在 meta 里)。
	stream.Handle(AgentStreamUpdate{Tokens: 100})
	if !stream.meta.CtxOK || stream.meta.CtxApprox {
		t.Fatalf("expected fresh ctx (approx=false), got %#v", stream.meta)
	}
	if stream.meta.CtxUsedPercent != 55 || stream.meta.CtxTokens != 110000 {
		t.Fatalf("fresh ctx not applied: %#v", stream.meta)
	}
}

func TestAgentCardStreamHandleKeepsApproxWhenSidecarNotYetFresh(t *testing.T) {
	// 续轮无本轮 fresh:sidecar 仍是上一轮的旧值(mtime < runStartedAt),
	// ReadAfter 判 stale 返回 !OK,refreshContextUsageLocked 应该什么都不改,
	// 保留 metaForRun 起始时的 approx,不能把已有 CtxOK 抹成 false 造成闪烁。
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-b.json")
	if err := os.WriteFile(path, []byte(`{"session_id":"sess-b","used_percentage":18,"total_tokens":36000,"context_window_size":200000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// sidecar mtime 明显早于 runStartedAt,模拟"本轮 sidecar 尚未落盘"。
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.ClaudeContextUsageDir = dir
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat"}, ID: "claude:chat", AgentSessionID: "sess-b"}
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: time.Now()})
	if !stream.meta.CtxOK || !stream.meta.CtxApprox {
		t.Fatalf("expected approx from prior sidecar, got %#v", stream.meta)
	}
	stream.Handle(AgentStreamUpdate{Tokens: 50})
	if !stream.meta.CtxOK || !stream.meta.CtxApprox {
		t.Fatalf("Handle wrongly cleared approx despite no fresh sidecar: %#v", stream.meta)
	}
	if stream.meta.CtxUsedPercent != 18 {
		t.Fatalf("approx value should be preserved: %#v", stream.meta)
	}
}

func TestAgentCardStreamFinishOverwritesApproxWithFresh(t *testing.T) {
	// running 期间显示 approx(上一轮值),终态 postRunMeta 拿到本轮 fresh 后应把
	// approx 清零并写入本轮真值。防止"终态卡片仍然带 ~ 前缀"这一 UX 回归。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sess-c.json"), []byte(`{"session_id":"sess-c","used_percentage":22,"total_tokens":44000,"context_window_size":200000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.ClaudeContextUsageDir = dir
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat"}, ID: "claude:chat", AgentSessionID: "sess-c"}
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: time.Now()})
	if !stream.meta.CtxApprox {
		t.Fatalf("initial should be approx: %#v", stream.meta)
	}
	// postRunMeta 模拟返回本轮真值(approx=false)。
	fresh := card.Meta{CtxOK: true, CtxApprox: false, CtxUsedPercent: 66, CtxTokens: 132000, CtxWindow: 200000}
	terminal, err := stream.Finish("completed", fresh, AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "done"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !terminal.Meta.CtxOK || terminal.Meta.CtxApprox {
		t.Fatalf("terminal should have fresh ctx, got: %#v", terminal.Meta)
	}
	if terminal.Meta.CtxUsedPercent != 66 || terminal.Meta.CtxTokens != 132000 {
		t.Fatalf("terminal fresh values wrong: %#v", terminal.Meta)
	}
}

func TestAgentCardStreamFinishClearsStaleContextUsage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old-session.json"), []byte(`{"session_id":"old-session","used_percentage":42,"total_tokens":84000,"context_window_size":200000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.ClaudeContextUsageDir = dir
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat"}, ID: "claude:chat", AgentSessionID: "old-session"}
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: time.Now()})
	if !stream.meta.CtxOK || stream.meta.CtxUsedPercent != 42 {
		t.Fatalf("initial context meta = %#v", stream.meta)
	}
	terminal, err := stream.Finish("completed", card.Meta{CtxOK: false}, AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "done"}}})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Meta.CtxOK || terminal.Meta.CtxUsedPercent != 0 || terminal.Meta.CtxTokens != 0 || terminal.Meta.CtxWindow != 0 {
		t.Fatalf("terminal retained stale context meta: %#v", terminal.Meta)
	}
}

type fakeStreamClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeStreamTimer
}

type fakeStreamTimer struct {
	clock   *fakeStreamClock
	due     time.Time
	fn      func()
	stopped bool
	fired   bool
}

func (c *fakeStreamClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeStreamClock) AfterFunc(delay time.Duration, fn func()) streamTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeStreamTimer{clock: c, due: c.now.Add(delay), fn: fn}
	c.timers = append(c.timers, timer)
	return timer
}

func (t *fakeStreamTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

func (c *fakeStreamClock) Advance(delta time.Duration) {
	target := c.Now().Add(delta)
	for {
		c.mu.Lock()
		var next *fakeStreamTimer
		for _, timer := range c.timers {
			if timer.stopped || timer.fired || timer.due.After(target) {
				continue
			}
			if next == nil || timer.due.Before(next.due) {
				next = timer
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		c.now = next.due
		next.fired = true
		fn := next.fn
		c.mu.Unlock()
		fn()
	}
}

type indexedFailRenderer struct {
	mu       sync.Mutex
	calls    []card.Event
	failCall int
}

func (r *indexedFailRenderer) Render(event card.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, event)
	if len(r.calls) == r.failCall {
		return errors.New("preview update failed")
	}
	return nil
}

func (r *indexedFailRenderer) Events() []card.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]card.Event(nil), r.calls...)
}

func newPreviewTestStream(t *testing.T, renderer card.Renderer, clock streamClock, minDelta, maxPreview int) *agentCardStream {
	return newPreviewTestStreamForMode(t, renderer, clock, minDelta, maxPreview, config.ReplyModeAppend)
}

func newPreviewTestStreamForMode(t *testing.T, renderer card.Renderer, clock streamClock, minDelta, maxPreview int, mode config.ReplyMode) *agentCardStream {
	t.Helper()
	cfg := testConfig(t)
	cfg.CardUpdateEvery = 800 * time.Millisecond
	cfg.CardMinDeltaChars = minDelta
	cfg.CardPreviewMaxChars = maxPreview
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	sess := session.Session{ID: "claude:chat", Tokens: 2}
	input := session.Input{ReplyToMessageID: "source", RequestedModel: "default", RequestedEffort: "low", ReplyMode: mode, Time: clock.Now()}
	return newAgentCardStreamWithClock(svc, "run", sess, input, renderer, nil, clock)
}

func segmentKinds(segments []card.Segment) []card.SegmentKind {
	kinds := make([]card.SegmentKind, len(segments))
	for i, segment := range segments {
		kinds[i] = segment.Kind
	}
	return kinds
}

func assertSegmentKinds(t *testing.T, event card.Event, want ...card.SegmentKind) {
	t.Helper()
	got := segmentKinds(event.Segments)
	if len(got) != len(want) {
		t.Fatalf("segment kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segment kinds = %v, want %v", got, want)
		}
	}
}

func TestAppendStreamPreservesTimeline(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(50, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 1, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "先检查"}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentTool, Text: "Bash(ls)"}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "最终答案"}}})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	previewEvents := renderer.Events()
	preview := previewEvents[len(previewEvents)-1]
	assertSegmentKinds(t, preview, card.SegmentText, card.SegmentTool, card.SegmentText)
	if !preview.OrderedLayout {
		t.Fatal("append preview did not request ordered layout")
	}
	terminal, err := stream.Finish("completed", card.Meta{}, AgentRunResult{OrderedSegments: []card.Segment{
		{Kind: card.SegmentText, Text: "先检查"},
		{Kind: card.SegmentTool, Text: "Bash(ls)"},
		{Kind: card.SegmentText, Text: "最终答案"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertSegmentKinds(t, terminal, card.SegmentText, card.SegmentTool, card.SegmentText)
	if !terminal.OrderedLayout {
		t.Fatal("append terminal did not request ordered layout")
	}
}

func TestAgentCardStreamThinkingOnlyDoesNotSchedulePreviewButCheckpointsIncludeIt(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(55, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 1, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	initialRenders := len(renderer.Events())

	stream.Handle(AgentStreamUpdate{
		Incremental: true,
		Segments:    []card.Segment{{Kind: card.SegmentThought, Text: "reasoning"}},
	})
	clock.Advance(time.Second)
	if got := len(renderer.Events()); got != initialRenders {
		t.Fatalf("thinking-only renders = %d, want %d", got, initialRenders)
	}

	stream.Handle(AgentStreamUpdate{
		Incremental: true,
		Activity:    streamActivityAnswering,
		Segments:    []card.Segment{{Kind: card.SegmentText, Text: "answer"}},
	})
	events := renderer.Events()
	if len(events) != initialRenders+1 {
		t.Fatalf("answer checkpoint renders = %d, want %d", len(events), initialRenders+1)
	}
	preview := events[len(events)-1]
	assertSegmentKinds(t, preview, card.SegmentThought, card.SegmentText)
	if preview.Segments[0].Text != "reasoning" || preview.Segments[1].Text != "answer" {
		t.Fatalf("answer checkpoint segments = %#v", preview.Segments)
	}

	stream.Handle(AgentStreamUpdate{
		Incremental: true,
		Segments:    []card.Segment{{Kind: card.SegmentThought, Text: " more"}},
	})
	clock.Advance(time.Second)
	if got := len(renderer.Events()); got != initialRenders+1 {
		t.Fatalf("second thinking-only renders = %d, want %d", got, initialRenders+1)
	}

	stream.Handle(AgentStreamUpdate{
		Activity: streamActivityTool,
		Segments: []card.Segment{{Kind: card.SegmentTool, Text: "Bash(status)", Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "status", Phase: "use"}}},
	})
	clock.Advance(time.Second)
	events = renderer.Events()
	checkpoint := events[len(events)-1]
	assertSegmentKinds(t, checkpoint, card.SegmentThought, card.SegmentText, card.SegmentTool)
	if checkpoint.Activity != streamActivityTool || !strings.Contains(checkpoint.Segments[0].Text, "reasoning more") {
		t.Fatalf("tool checkpoint = %#v", checkpoint)
	}

	terminal, err := stream.Finish("completed", card.Meta{}, AgentRunResult{})
	if err != nil {
		t.Fatal(err)
	}
	if len(terminal.Segments) == 0 || terminal.Segments[0].Kind != card.SegmentThought || !strings.Contains(terminal.Segments[0].Text, "reasoning more") {
		t.Fatalf("terminal lost accumulated thinking: %#v", terminal.Segments)
	}
}

func TestNonAppendStreamsKeepAggregateLayout(t *testing.T) {
	for _, mode := range []config.ReplyMode{config.ReplyModeAppendCleanCard, config.ReplyModeLatestCard} {
		t.Run(string(mode), func(t *testing.T) {
			clock := &fakeStreamClock{now: time.Unix(60, 0)}
			renderer := card.NewFakeRenderer()
			stream := newPreviewTestStreamForMode(t, renderer, clock, 1, 2000, mode)
			if err := stream.Start(); err != nil {
				t.Fatal(err)
			}
			stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "先检查"}}})
			stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentTool, Text: "Bash(ls)", Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "ls", Phase: "use"}}}})
			stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "最终答案"}}})
			if err := stream.Flush(); err != nil {
				t.Fatal(err)
			}
			events := renderer.Events()
			preview := events[len(events)-1]
			assertSegmentKinds(t, preview, card.SegmentText, card.SegmentTool)
			if preview.OrderedLayout {
				t.Fatalf("%s preview unexpectedly requested ordered layout", mode)
			}
		})
	}
}

func TestStreamPreviewCannotRenderAfterFinish(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(100, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.mu.Lock()
	generation := stream.previewGen
	stream.previewPending = true
	preview := card.Event{Type: "stream", SessionID: "run", Segments: []card.Segment{{Kind: card.SegmentText, Text: "stale preview"}}, Streaming: true}
	stream.mu.Unlock()
	if _, err := stream.Finish("completed", card.Meta{}, AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "final"}}}); err != nil {
		t.Fatal(err)
	}
	before := len(renderer.Events())
	if err := stream.renderPreview(generation, preview); err != nil {
		t.Fatal(err)
	}
	if got := len(renderer.Events()); got != before {
		t.Fatalf("events after stale preview = %d, want %d", got, before)
	}
}

func TestAgentCardStreamAllowsTransformedFinishThenTerminalUpdate(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(100, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)

	terminal, err := stream.FinishTransformed("completed", card.Meta{}, AgentRunResult{
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "![图](chart.png)"}},
	}, func(event card.Event) card.Event {
		event.Segments[0].Text = "🖼️ 图（正在发送）"
		return event
	})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Type != "result" || terminal.Streaming || terminal.Segments[0].Text != "🖼️ 图（正在发送）" {
		t.Fatalf("pending terminal=%#v", terminal)
	}
	terminal.Segments = append([]card.Segment(nil), terminal.Segments...)
	terminal.Segments[0].Text = "🖼️ 图（已作为图片发送）"
	if err := stream.RenderTerminalUpdate(terminal); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if len(events) != 2 || events[0].SessionID != events[1].SessionID {
		t.Fatalf("events=%#v", events)
	}
	if events[0].Segments[0].Text != "🖼️ 图（正在发送）" || events[1].Segments[0].Text != "🖼️ 图（已作为图片发送）" {
		t.Fatalf("events=%#v", events)
	}
}

func TestAgentCardStreamRejectsNonTerminalSecondUpdate(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(100, 0)}
	stream := newPreviewTestStream(t, card.NewFakeRenderer(), clock, 30, 2000)
	if err := stream.RenderTerminalUpdate(card.Event{Type: "stream", Streaming: true}); err == nil {
		t.Fatal("accepted non-terminal update")
	}
}

func TestStreamMarkStoppingBlocksRunningRenders(t *testing.T) {
	// v2 R5:标记停止后,排队/后续到达的 Handle 不再渲染运行中卡片,
	// 避免停止卡发出后被覆盖回"运行中"造成闪烁。
	clock := &fakeStreamClock{now: time.Unix(100, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	before := len(renderer.Events())
	stream.markStopping()
	// 停止后到达的流式增量必须被忽略。
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "late chunk"}}, Activity: streamActivityAnswering})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := len(renderer.Events()); got != before {
		t.Fatalf("events after markStopping = %d, want %d (no running renders)", got, before)
	}
	// 终态仍可正常收敛。
	if _, err := stream.Finish("stopped", card.Meta{}, AgentRunResult{}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if events[len(events)-1].Type != "stopped" {
		t.Fatalf("terminal event = %#v, want stopped", events[len(events)-1].Type)
	}
}

func TestAgentCardStreamUserStopAppendsNoticeAfterExistingOutput(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(70, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 1, 2000)
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "已有输出"}}})

	snapshot := stream.requestStop()
	if snapshot.Type != "stopped" || snapshot.Streaming || !snapshot.StopButton.Disabled {
		t.Fatalf("stop snapshot = %#v", snapshot)
	}
	if got := snapshot.Segments[len(snapshot.Segments)-1].Text; got != stopRequestedNotice {
		t.Fatalf("snapshot tail = %q", got)
	}

	terminal, err := stream.Finish("stopped", card.Meta{}, AgentRunResult{})
	if err != nil {
		t.Fatal(err)
	}
	if len(terminal.Segments) != 2 || terminal.Segments[0].Text != "已有输出" || terminal.Segments[1].Text != stopRequestedNotice {
		t.Fatalf("terminal segments = %#v", terminal.Segments)
	}
}

func TestAgentCardStreamInternalStopDoesNotAppendUserNotice(t *testing.T) {
	stream := newPreviewTestStream(t, card.NewFakeRenderer(), &fakeStreamClock{now: time.Unix(71, 0)}, 1, 2000)
	stream.markStopping()
	terminal, err := stream.Finish("stopped", card.Meta{}, AgentRunResult{})
	if err != nil {
		t.Fatal(err)
	}
	if eventsContainText([]card.Event{terminal}, stopRequestedNotice) {
		t.Fatalf("internal stop event = %#v", terminal)
	}
}

func TestStreamPreviewUsesDeltaAndDeadlineThresholds(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(100, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "首"}}})
	if got := len(renderer.Events()); got != 2 {
		t.Fatalf("events after first output = %d, want immediate preview", got)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: strings.Repeat("界", 30)}}})
	if got := len(renderer.Events()); got != 2 {
		t.Fatalf("events before interval = %d, want coalesced preview", got)
	}
	clock.Advance(799 * time.Millisecond)
	if got := len(renderer.Events()); got != 2 {
		t.Fatalf("events at 799ms = %d", got)
	}
	clock.Advance(time.Millisecond)
	if got := len(renderer.Events()); got != 3 {
		t.Fatalf("events at deadline = %d, want timer preview", got)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "小"}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "增"}}})
	clock.Advance(800 * time.Millisecond)
	if got := len(renderer.Events()); got != 4 {
		t.Fatalf("one-rune burst events = %d, want one coalesced deadline preview", got)
	}
}

func TestStreamPreviewTruncatesUnicodeButTerminalIsCompleteAndCancelsTimer(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(200, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 20)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("你", 25)
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: full}}})
	preview := renderer.Events()[1]
	if len(preview.Segments) != 1 || utf8.RuneCountInString(preview.Segments[0].Text) != 20 {
		t.Fatalf("preview segments = %#v", preview.Segments)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "尾"}}})
	if _, err := stream.Finish("completed", card.Meta{}, AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: full + "尾"}}}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	terminal := events[len(events)-1]
	if terminal.Type != "result" || !terminal.OrderedLayout || len(terminal.Segments) != 2 || terminal.Segments[0].Text+terminal.Segments[1].Text != full+"尾" {
		t.Fatalf("terminal = %#v", terminal)
	}
	before := len(events)
	clock.Advance(time.Second)
	if got := len(renderer.Events()); got != before {
		t.Fatalf("late timer rendered %d events, want %d", got, before)
	}
}

func TestStreamPreviewFailureDisablesOnlyIntermediateUpdates(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(300, 0)}
	renderer := &indexedFailRenderer{failCall: 2}
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "first"}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "second"}}})
	clock.Advance(time.Second)
	if got := len(renderer.Events()); got != 2 {
		t.Fatalf("events after failed preview = %d, want previews disabled", got)
	}
	if _, err := stream.Finish("completed", card.Meta{}, AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "final"}}}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if len(events) != 3 || events[2].Type != "result" {
		t.Fatalf("terminal after preview failure = %#v", events)
	}
}

func TestStreamPreviewPreservesChunkBoundaries(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(400, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{"清晨的光", "落在", "窗台上", "\n\nHello ", "world", "\n\n**粗", "体**"} {
		stream.Handle(AgentStreamUpdate{
			Segments:    []card.Segment{{Kind: card.SegmentText, Text: chunk}},
			Activity:    streamActivityAnswering,
			Incremental: true,
		})
	}
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	preview := events[len(events)-1]
	want := "清晨的光落在窗台上\n\nHello world\n\n**粗体**"
	if len(preview.Segments) != 1 || preview.Segments[0].Text != want {
		t.Fatalf("preview segments = %#v, want %q", preview.Segments, want)
	}
}

func TestStripTrailingBotSignature(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "em dash signature", input: "正文\n\n—— 李俊-H-Bot", want: "正文"},
		{name: "ascii dash signature", input: "正文\n\n-- Demo-Bot", want: "正文"},
		{name: "case insensitive", input: "正文\n—— Demo-bot  ", want: "正文"},
		{name: "middle signature-like line", input: "第一段\n\n—— Demo-Bot\n\n结论", want: "第一段\n\n—— Demo-Bot\n\n结论"},
		{name: "ordinary em dash", input: "正文\n\n—— 这是补充说明", want: "正文\n\n—— 这是补充说明"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripTrailingBotSignature(tt.input); got != tt.want {
				t.Fatalf("stripTrailingBotSignature(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStreamOmitsTrailingBotSignature(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(500, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "正文\n\n—— 李俊-H-Bot"}},
		Activity: streamActivityAnswering,
	})
	preview := renderer.Events()[1]
	if len(preview.Segments) != 1 || preview.Segments[0].Text != "正文" {
		t.Fatalf("preview segments = %#v, want signature-free answer", preview.Segments)
	}
	initialMeta := renderer.Events()[0].Meta
	terminal, err := stream.Finish("completed", card.Meta{Agent: "claude"}, AgentRunResult{
		Segments:       []card.Segment{{Kind: card.SegmentText, Text: "最终正文\n\n—— 李俊-H-Bot"}},
		AnswerSegments: []string{"最终正文\n\n—— 李俊-H-Bot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(terminal.Segments) != 1 || terminal.Segments[0].Text != "最终正文" {
		t.Fatalf("terminal segments = %#v, want signature-free answer", terminal.Segments)
	}
	if terminal.Meta.Agent != "claude" || terminal.Meta.User != initialMeta.User || terminal.Meta.IP != initialMeta.IP {
		t.Fatalf("terminal meta = %#v, want runtime meta preserved", terminal.Meta)
	}
}

func TestStreamRemovesSignatureCompletedAcrossChunks(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(600, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{
		Segments:    []card.Segment{{Kind: card.SegmentText, Text: "正文\n\n-- Demo-"}},
		Activity:    streamActivityAnswering,
		Incremental: true,
	})
	if got := renderer.Events()[1].Segments[0].Text; got != "正文\n\n-- Demo-" {
		t.Fatalf("partial signature preview = %q", got)
	}
	stream.Handle(AgentStreamUpdate{
		Segments:    []card.Segment{{Kind: card.SegmentText, Text: "Bot"}},
		Activity:    streamActivityAnswering,
		Incremental: true,
	})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if got := events[len(events)-1].Segments[0].Text; got != "正文" {
		t.Fatalf("completed signature preview = %q, want cleaned body", got)
	}
}

func TestAppendStreamSnapshotDoesNotDuplicate(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(700, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "Hello "}}, Incremental: true})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "world"}}, Incremental: true})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "Hello world"}}, AnswerSnapshot: true})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	preview := events[len(events)-1]
	if got := preview.Segments[0].Text; got != "Hello world" {
		t.Fatalf("snapshot preview = %q, want no duplicated partial text", got)
	}
	if len(preview.Segments) != 1 || !preview.OrderedLayout {
		t.Fatalf("snapshot preview = %#v, want one ordered text block", preview)
	}
}

func TestAppendStreamSnapshotReplacesTextAndToolTail(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(750, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "先检查"}}, Incremental: true})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentTool, Text: "partial tool"}}, Incremental: true})
	stream.Handle(AgentStreamUpdate{
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "先检查"},
			{Kind: card.SegmentTool, Text: "Bash(ls)"},
		},
		AnswerSnapshot: true,
	})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	preview := events[len(events)-1]
	assertSegmentKinds(t, preview, card.SegmentText, card.SegmentTool)
	if preview.Segments[0].Text != "先检查" || preview.Segments[1].Text != "Bash(ls)" {
		t.Fatalf("snapshot timeline = %#v, want replacement without duplicate tail", preview.Segments)
	}
}

func TestAppendStreamSnapshotWithoutPartialAppendsAfterToolResult(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(775, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	for _, segment := range []card.Segment{
		{Kind: card.SegmentText, Text: "先检查"},
		{Kind: card.SegmentTool, Text: "Bash(ls)"},
		{Kind: card.SegmentTool, Text: "tool_result: file.txt"},
	} {
		stream.Handle(AgentStreamUpdate{Segments: []card.Segment{segment}})
	}
	stream.Handle(AgentStreamUpdate{
		Segments:       []card.Segment{{Kind: card.SegmentText, Text: "最终答案"}},
		AnswerSnapshot: true,
	})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	preview := events[len(events)-1]
	assertSegmentKinds(t, preview, card.SegmentText, card.SegmentTool, card.SegmentTool, card.SegmentText)
	if preview.Segments[0].Text != "先检查" || preview.Segments[3].Text != "最终答案" {
		t.Fatalf("snapshot timeline = %#v, want prior tool history plus final answer", preview.Segments)
	}
}

func TestAppendStreamToolOnlySnapshotClosesPartialBeforeToolResult(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(777, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{
		Segments:          []card.Segment{{Kind: card.SegmentText, Text: "先检查"}},
		AnswerSnapshot:    true,
		AssistantSnapshot: true,
	})
	stream.Handle(AgentStreamUpdate{
		Segments:       []card.Segment{{Kind: card.SegmentTool, Text: "partial tool"}},
		PartialMessage: true,
	})
	stream.Handle(AgentStreamUpdate{
		Segments:          []card.Segment{{Kind: card.SegmentTool, Text: "Bash(ls)"}},
		AssistantSnapshot: true,
	})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentTool, Text: "tool_result: file.txt"}}})
	stream.Handle(AgentStreamUpdate{
		Segments:       []card.Segment{{Kind: card.SegmentText, Text: "最终"}},
		PartialMessage: true,
		Incremental:    true,
	})
	stream.Handle(AgentStreamUpdate{
		Segments:          []card.Segment{{Kind: card.SegmentText, Text: "最终答案"}},
		AnswerSnapshot:    true,
		AssistantSnapshot: true,
	})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	preview := events[len(events)-1]
	assertSegmentKinds(t, preview, card.SegmentText, card.SegmentTool, card.SegmentTool, card.SegmentText)
	if preview.Segments[0].Text != "先检查" || preview.Segments[1].Text != "Bash(ls)" || preview.Segments[2].Text != "tool_result: file.txt" || preview.Segments[3].Text != "最终答案" {
		t.Fatalf("tool-loop timeline = %#v", preview.Segments)
	}
}

func TestAppendStreamConsecutiveFullSnapshotsPreservePrefixHistory(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(780, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{
		Segments:       []card.Segment{{Kind: card.SegmentText, Text: "检查中"}},
		AnswerSnapshot: true,
	})
	stream.Handle(AgentStreamUpdate{
		Segments:       []card.Segment{{Kind: card.SegmentText, Text: "检查中，请稍候"}},
		AnswerSnapshot: true,
	})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	preview := events[len(events)-1]
	assertSegmentKinds(t, preview, card.SegmentText, card.SegmentText)
	if preview.Segments[0].Text != "检查中" || preview.Segments[1].Text != "检查中，请稍候" {
		t.Fatalf("snapshot timeline = %#v, want both complete messages", preview.Segments)
	}
}

func TestAppendLegacyTerminalPreservesStreamedTextTimeline(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(785, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "第一段"}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "第二段"}}})
	terminal, err := stream.Finish("completed", card.Meta{}, AgentRunResult{
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "第一段\n\n第二段"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSegmentKinds(t, terminal, card.SegmentText, card.SegmentText)
	if terminal.Segments[0].Text != "第一段" || terminal.Segments[1].Text != "第二段" {
		t.Fatalf("legacy terminal timeline = %#v, want streamed blocks preserved", terminal.Segments)
	}
}

func TestAppendLegacyTerminalAppendsAnswerAfterTrailingTool(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(790, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "先检查"}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentTool, Text: "Bash(ls)"}}})
	terminal, err := stream.Finish("completed", card.Meta{}, AgentRunResult{
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "最终答案"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSegmentKinds(t, terminal, card.SegmentText, card.SegmentTool, card.SegmentText)
	if terminal.Segments[0].Text != "先检查" || terminal.Segments[2].Text != "最终答案" {
		t.Fatalf("legacy terminal timeline = %#v, want answer appended after tool", terminal.Segments)
	}
}

func TestStreamPreservesFullThinkingBoundariesAndThinkingDeltas(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(800, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentThought, Text: "first"}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentThought, Text: "second"}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentThought, Text: " + "}}, Incremental: true})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentThought, Text: "delta"}}, Incremental: true})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	segments := events[len(events)-1].Segments
	if len(segments) != 1 || segments[0].Kind != card.SegmentThought || segments[0].Text != "first\n\nsecond + delta" {
		t.Fatalf("thought segments = %#v", segments)
	}
}

func TestFailedStreamRemovesAnswerSignatureBeforeErrorBlock(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(900, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	terminal, err := stream.Finish("failed", card.Meta{}, AgentRunResult{
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "正文\n\n—— Demo-Bot"},
			{Kind: card.SegmentError, Text: "runner failed"},
		},
		AnswerSegments: []string{"正文\n\n—— Demo-Bot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(terminal.Segments) != 2 || terminal.Segments[0].Kind != card.SegmentText || terminal.Segments[1].Kind != card.SegmentError {
		t.Fatalf("terminal segments = %#v", terminal.Segments)
	}
	if strings.Contains(terminal.Segments[0].Text, "Demo-Bot") || !strings.Contains(terminal.Segments[0].Text, "正文") || !strings.Contains(terminal.Segments[1].Text, "runner failed") {
		t.Fatalf("terminal timeline = %#v, want body then error without signature", terminal.Segments)
	}
}

func TestStreamWhitespaceOnlyDeltaDoesNotRenderEmptyCardOrDelayFirstText(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(1000, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{
		Segments:    []card.Segment{{Kind: card.SegmentText, Text: "\n"}},
		Incremental: true,
	})
	if got := len(renderer.Events()); got != 1 {
		t.Fatalf("events after whitespace-only delta = %d, want initial card only", got)
	}
	stream.Handle(AgentStreamUpdate{
		Segments:    []card.Segment{{Kind: card.SegmentText, Text: "首"}},
		Incremental: true,
	})
	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events after first visible text = %d, want immediate preview", len(events))
	}
	if got := events[1].Segments[0].Text; got != "\n首" {
		t.Fatalf("first visible preview = %q, want preserved leading delta", got)
	}
}

// TestAppendCleanThreeSectionLatestOnly 验证 append-clean-card 三段布局:思考/工具只保留
// 最新一次,折叠区计数(× N)累计全过程,Event 携带 ThreeSectionLayout + 展开态。
func TestAppendCleanThreeSectionLatestOnly(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(200, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStreamForMode(t, renderer, clock, 1, 4000, config.ReplyModeAppendCleanCard)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}

	// 第 1 轮:思考 delta + 一次工具调用(tool_use → tool_result),以 AssistantSnapshot 收尾。
	// 注意 snapshot 会带完整 thought;delta 阶段的中间态会被 snapshot 权威覆盖,避免重复。
	stream.Handle(AgentStreamUpdate{Activity: streamActivityReasoning, Incremental: true,
		Segments: []card.Segment{{Kind: card.SegmentThought, Text: "先想第一步"}}})
	stream.Handle(AgentStreamUpdate{Activity: streamActivityTool,
		Segments: []card.Segment{{Kind: card.SegmentTool, Text: "- Bash `t1`\n\n```json\n{\"command\":\"ls\"}\n```",
			Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "ls", Phase: "use"}}}})
	stream.Handle(AgentStreamUpdate{
		Segments: []card.Segment{{Kind: card.SegmentTool, Text: "- tool_result `t1`\n\n```\nfile1\nfile2\n```",
			Tool: &card.ToolMeta{ID: "t1", Phase: "result"}}}})
	stream.Handle(AgentStreamUpdate{AssistantSnapshot: true,
		Segments: []card.Segment{
			{Kind: card.SegmentThought, Text: "先想第一步"},
			{Kind: card.SegmentText, Text: "中间答复"},
		}})

	// 第 2 轮:新的思考 + 新一次工具,再 snapshot。
	stream.Handle(AgentStreamUpdate{Activity: streamActivityReasoning, Incremental: true,
		Segments: []card.Segment{{Kind: card.SegmentThought, Text: "再想第二步"}}})
	stream.Handle(AgentStreamUpdate{Activity: streamActivityTool,
		Segments: []card.Segment{{Kind: card.SegmentTool, Text: "- Bash `t2`\n\n```json\n{\"command\":\"cat x\"}\n```",
			Tool: &card.ToolMeta{ID: "t2", Name: "Bash", Summary: "cat x", Phase: "use"}}}})
	stream.Handle(AgentStreamUpdate{
		Segments: []card.Segment{{Kind: card.SegmentTool, Text: "- tool_result `t2`\n\n```\nhello\n```",
			Tool: &card.ToolMeta{ID: "t2", Phase: "result"}}}})
	stream.Handle(AgentStreamUpdate{AssistantSnapshot: true,
		Segments: []card.Segment{
			{Kind: card.SegmentThought, Text: "再想第二步"},
			{Kind: card.SegmentText, Text: "最终答复"},
		}})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}

	ev := renderer.Events()[len(renderer.Events())-1]
	if !ev.ThreeSectionLayout {
		t.Fatalf("expected ThreeSectionLayout for append-clean, got %#v", ev)
	}
	if !ev.ThoughtExpanded || ev.ToolsExpanded {
		t.Fatalf("running expand state wrong: thought=%v tools=%v", ev.ThoughtExpanded, ev.ToolsExpanded)
	}
	if ev.ThoughtRoundCount != 2 || ev.ToolRoundCount != 2 {
		t.Fatalf("counts = thought:%d tool:%d, want 2/2", ev.ThoughtRoundCount, ev.ToolRoundCount)
	}
	thought := segmentTextByKind(ev, card.SegmentThought)
	if strings.Contains(thought, "第一步") || !strings.Contains(thought, "第二步") {
		t.Fatalf("thought should show only latest COT, got %q", thought)
	}
	// 思考区不应重复:snapshot 覆盖 delta,同一轮内不应出现两遍相同 COT。
	if strings.Count(thought, "第二步") != 1 {
		t.Fatalf("thought must not duplicate within a round, got %q", thought)
	}
	tool := segmentTextByKind(ev, card.SegmentTool)
	// 工具区必须是人读格式:含 Name (Bash) + command (cat x) + 输出 (hello),不带 raw JSON / call_ id。
	if !strings.Contains(tool, "Bash") || !strings.Contains(tool, "cat x") || !strings.Contains(tool, "hello") {
		t.Fatalf("tool must show name+command+output in human form, got %q", tool)
	}
	if strings.Contains(tool, "t1") || strings.Contains(tool, "tool_result") || strings.Contains(tool, "\"command\"") {
		t.Fatalf("tool must not leak raw id / raw json / tool_result stub, got %q", tool)
	}

	// 终态:思考折叠,计数保持,内容仍是最新一次;stop button 隐藏。
	terminal, err := stream.Finish("completed", card.Meta{}, AgentRunResult{
		Segments: []card.Segment{
			{Kind: card.SegmentThought, Text: "先想第一步"},
			{Kind: card.SegmentTool, Text: "- Bash `t1`\n\n```json\n{\"command\":\"ls\"}\n```",
				Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "ls", Phase: "use"}},
			{Kind: card.SegmentTool, Text: "- tool_result `t1`\n\n```\nfile1\nfile2\n```",
				Tool: &card.ToolMeta{ID: "t1", Phase: "result"}},
			{Kind: card.SegmentThought, Text: "再想第二步"},
			{Kind: card.SegmentTool, Text: "- Bash `t2`\n\n```json\n{\"command\":\"cat x\"}\n```",
				Tool: &card.ToolMeta{ID: "t2", Name: "Bash", Summary: "cat x", Phase: "use"}},
			{Kind: card.SegmentTool, Text: "- tool_result `t2`\n\n```\nhello\n```",
				Tool: &card.ToolMeta{ID: "t2", Phase: "result"}},
			{Kind: card.SegmentText, Text: "最终答复"},
		},
		AnswerSegments: []string{"最终答复"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.ThoughtExpanded {
		t.Fatalf("terminal thought should be collapsed")
	}
	if terminal.StopButton.Visible {
		t.Fatalf("terminal must hide stop button, got %#v", terminal.StopButton)
	}
	if terminal.ThoughtRoundCount != 2 || terminal.ToolRoundCount != 2 {
		t.Fatalf("terminal counts = thought:%d tool:%d, want 2/2", terminal.ThoughtRoundCount, terminal.ToolRoundCount)
	}
	if got := segmentTextByKind(terminal, card.SegmentThought); strings.Contains(got, "第一步") || !strings.Contains(got, "第二步") {
		t.Fatalf("terminal thought latest-only failed: %q", got)
	}
	if got := segmentTextByKind(terminal, card.SegmentTool); !strings.Contains(got, "cat x") || !strings.Contains(got, "hello") || strings.Contains(got, "ls\n```") {
		t.Fatalf("terminal tool latest-only failed: %q", got)
	}
}

func segmentTextByKind(ev card.Event, kind card.SegmentKind) string {
	for _, seg := range ev.Segments {
		if seg.Kind == kind {
			return seg.Text
		}
	}
	return ""
}
