package bridge

import (
	"errors"
	"fmt"
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
	"lark-agent-bridge/internal/reply"
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

func TestAgentCardStreamFinishAdoptsLatestVersionDiscoveredDuringRun(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat"}, ID: "claude:chat"}
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: time.Now()})
	stream.meta.ShowMetaRowDeveloper = true
	stream.meta.Version = "dev"

	terminal, err := stream.Finish("completed", card.Meta{
		ShowMetaRowDeveloper: true,
		Version:              "dev",
		DeveloperMode:        true,
		LatestVersion:        "v0.1.11-rc.6",
	}, AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "done"}}})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Meta.LatestVersion != "v0.1.11-rc.6" || !terminal.Meta.DeveloperMode {
		t.Fatalf("terminal developer meta = %#v", terminal.Meta)
	}
	rows := card.MetaRows(terminal.Meta)
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "✨ 最新 v0.1.11-rc.6") {
		t.Fatalf("terminal developer row = %#v", rows)
	}
}

func TestAgentCardStreamUsesConfiguredCompletionStatusText(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	clock := &fakeStreamClock{now: time.Unix(100, 0)}
	stream := newAgentCardStreamWithClock(svc, "run", session.Session{ID: "claude:chat"}, session.Input{
		ReplyToMessageID: "source", CompletionStatusText: "🎉 任务完成", Time: clock.Now(),
	}, renderer, nil, clock)
	clock.now = clock.now.Add(2 * time.Second)
	if got := stream.headerTitleLocked(); got != "🧠 正在推理 · ⏱ 2s" {
		t.Fatalf("running title = %q", got)
	}
	stream.status = "completed"
	if got := stream.headerTitleLocked(); got != "🎉 任务完成 · ⏱ 2s" {
		t.Fatalf("completed title = %q", got)
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

func TestCodexAgentCardStreamStartsWithCurrentRunMetadataPending(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "context-usage")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prior.json"), []byte(`{"session_id":"prior","cwd":"`+workDir+`","used_percentage":31,"context_tokens":62000,"context_window_size":200000,"model":"gpt-codex","reasoning_effort":"high"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.CodexContextUsageDir = ""
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "codex", ChatID: "chat"}, ID: "codex:chat", WorkDir: workDir}

	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", AgentHome: home, Time: time.Now()})

	if stream.meta.CtxOK || stream.meta.CtxApprox || !stream.meta.CtxPending || !stream.meta.ModelPending || stream.meta.Model != "" || stream.meta.CtxUsedPercent != 0 || stream.meta.CtxTokens != 0 {
		t.Fatalf("Codex initial metadata must not inherit prior sidecar = %#v", stream.meta)
	}
	if stream.ctxDir != dir {
		t.Fatalf("Codex stream context dir = %q, want %q", stream.ctxDir, dir)
	}
}

func TestCodexAgentCardStreamMetadataOnlyUpdateRefreshesModelAndContext(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	cfg := testConfig(t)
	cfg.CodexContextUsageDir = dir
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "codex", ChatID: "chat"}, ID: "codex:chat", WorkDir: workDir}
	started := time.Now().Add(-time.Second)
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: started})
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "current.json")
	if err := os.WriteFile(path, []byte(`{"session_id":"current","cwd":"`+workDir+`","used_percentage":42,"context_tokens":84000,"context_window_size":200000,"model":"gpt-current","reasoning_effort":"medium"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	stream.Handle(AgentStreamUpdate{AgentSessionID: "current"})

	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("metadata-only event count = %d, want 2: %#v", len(events), events)
	}
	got := events[1].Meta
	if got.SessionID != "current" || got.Model != "gpt-current" || got.ModelInfo != (card.ModelInfo{Actual: "gpt-current", Effort: "medium"}) || !got.CtxOK || got.CtxApprox || got.CtxUsedPercent != 42 {
		t.Fatalf("metadata-only update = %#v", got)
	}
}

func TestAgentCardStreamHandleReplacesPendingWithFreshContext(t *testing.T) {
	// 续轮也必须从同步中开始；本轮 sidecar 到达后才显示占用。
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
	if stream.meta.CtxOK || !stream.meta.CtxPending || stream.meta.CtxUsedPercent != 0 {
		t.Fatalf("expected pending metadata without prior context, got %#v", stream.meta)
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
	if !stream.meta.CtxOK || stream.meta.CtxApprox || stream.meta.CtxPending {
		t.Fatalf("expected fresh ctx after sidecar, got %#v", stream.meta)
	}
	if stream.meta.CtxUsedPercent != 55 || stream.meta.CtxTokens != 110000 {
		t.Fatalf("fresh ctx not applied: %#v", stream.meta)
	}
}

func TestAgentCardStreamKeepsPendingWhenSidecarIsStale(t *testing.T) {
	// 首轮 synthetic thread 收到 session id 后，如果 sidecar 仍属于上一轮，
	// 必须继续显示同步中，而不是回退到旧占用。
	dir := t.TempDir()
	path := filepath.Join(dir, "new-session.json")
	if err := os.WriteFile(path, []byte(`{"session_id":"new-session","used_percentage":37,"total_tokens":74000,"context_window_size":200000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.ClaudeContextUsageDir = dir
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	// 首轮 synthetic thread:session 没 AgentSessionID,metaForRun 里 CtxOK=false。
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat", Thread: "@bot:m1"}, ID: "claude:chat:thread:@bot:m1"}
	stream := newAgentCardStream(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: time.Now()})
	if stream.meta.CtxOK {
		t.Fatalf("first-turn synthetic key should start with CtxOK=false, got %#v", stream.meta)
	}
	// Handle 携带 agent session id;本轮 sidecar 仍 stale。
	stream.Handle(AgentStreamUpdate{AgentSessionID: "new-session"})
	if stream.meta.CtxOK || !stream.meta.CtxPending || stream.meta.CtxUsedPercent != 0 || stream.meta.CtxTokens != 0 || stream.meta.CtxWindow != 0 {
		t.Fatalf("stale sidecar must not appear on the running card: %#v", stream.meta)
	}
}

func TestAgentCardStreamHandleKeepsPendingWhenSidecarNotYetFresh(t *testing.T) {
	// 续轮也不沿用旧 sidecar；本轮 fresh 数据到达前始终保持同步中。
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
	if stream.meta.CtxOK || !stream.meta.CtxPending {
		t.Fatalf("expected pending metadata without fresh sidecar, got %#v", stream.meta)
	}
	stream.Handle(AgentStreamUpdate{Tokens: 50})
	if stream.meta.CtxOK || !stream.meta.CtxPending || stream.meta.CtxUsedPercent != 0 {
		t.Fatalf("stale sidecar must remain hidden: %#v", stream.meta)
	}
}

func TestAgentCardStreamFinishOverwritesPendingWithFresh(t *testing.T) {
	// 终态 postRunMeta 的本轮真值必须覆盖运行期间的同步中状态。
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
	if stream.meta.CtxOK || !stream.meta.CtxPending {
		t.Fatalf("initial should be pending: %#v", stream.meta)
	}
	// postRunMeta 模拟返回本轮真值(approx=false)。
	fresh := card.Meta{CtxOK: true, CtxApprox: false, CtxPending: false, CtxUsedPercent: 66, CtxTokens: 132000, CtxWindow: 200000}
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
	if stream.meta.CtxOK || !stream.meta.CtxPending || stream.meta.CtxUsedPercent != 0 {
		t.Fatalf("initial context meta = %#v", stream.meta)
	}
	terminal, err := stream.Finish("completed", card.Meta{CtxOK: false}, AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "done"}}})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Meta.CtxOK || terminal.Meta.CtxPending || terminal.Meta.CtxUsedPercent != 0 || terminal.Meta.CtxTokens != 0 || terminal.Meta.CtxWindow != 0 {
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

type previewMarkdownCapture struct {
	*card.FakeRenderer
}

func (r *previewMarkdownCapture) RenderRef() session.RenderRef { return session.RenderRef{} }

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
	cfg.CardHeartbeatEvery = time.Hour
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

func TestAppendStreamPreviewKeepsNewestTimelineTail(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(50, 0)}
	cardRenderer := &previewMarkdownCapture{FakeRenderer: card.NewFakeRenderer()}
	renderer := reply.NewMarkdownCardRendererWithLimit(cardRenderer, 80)
	stream := newPreviewTestStream(t, renderer, clock, 1, 80)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentThought, Text: "旧思考" + strings.Repeat("旧", 100)}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentThought, Text: "最新思考"}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{
		Kind: card.SegmentTool,
		Text: "最新工具",
		Tool: &card.ToolMeta{ID: "tool-new", Name: "Bash", Phase: "use", Summary: "echo newest"},
	}}})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}

	events := cardRenderer.Events()
	preview := events[len(events)-1].Markdown
	if !strings.Contains(preview, "较早过程已省略") {
		t.Fatalf("preview did not mark the truncated head: %q", preview)
	}
	if strings.Contains(preview, "旧思考") {
		t.Fatalf("preview retained the stale head: %q", preview)
	}
	if !strings.Contains(preview, "最新思考") || !strings.Contains(preview, "Bash") {
		t.Fatalf("preview did not advance to newest thought and tool: %q", preview)
	}
}

func TestCleanCardsKeepLatestAnswerAcrossProgressSnapshots(t *testing.T) {
	for _, mode := range []config.ReplyMode{config.ReplyModeAppendCleanCard, config.ReplyModeLatestCard} {
		t.Run(string(mode), func(t *testing.T) {
			clock := &fakeStreamClock{now: time.Unix(50, 0)}
			renderer := card.NewFakeRenderer()
			stream := newPreviewTestStreamForMode(t, renderer, clock, 1, 2000, mode)
			if err := stream.Start(); err != nil {
				t.Fatal(err)
			}
			stream.Handle(AgentStreamUpdate{
				Segments:       []card.Segment{{Kind: card.SegmentText, Text: "已完成检查，继续执行。"}},
				Activity:       streamActivityAnswering,
				AnswerSnapshot: true,
			})
			stream.Handle(AgentStreamUpdate{
				Segments:          []card.Segment{{Kind: card.SegmentThought, Text: "已完成检查，继续执行。"}},
				Activity:          streamActivityReasoning,
				AssistantSnapshot: true,
				ProgressSnapshot:  true,
			})
			if err := stream.Flush(); err != nil {
				t.Fatal(err)
			}
			preview := renderer.Events()[len(renderer.Events())-1]
			if answer := segmentTextByKind(preview, card.SegmentText); answer != "已完成检查，继续执行。" {
				t.Fatalf("%s answer after progress = %q", mode, answer)
			}
			if thought := segmentTextByKind(preview, card.SegmentThought); !strings.Contains(thought, "已完成检查，继续执行。") {
				t.Fatalf("%s thought after progress = %q", mode, thought)
			}

			stream.Handle(AgentStreamUpdate{
				Segments:       []card.Segment{{Kind: card.SegmentText, Text: "最终答案"}},
				Activity:       streamActivityAnswering,
				AnswerSnapshot: true,
			})
			if err := stream.Flush(); err != nil {
				t.Fatal(err)
			}
			preview = renderer.Events()[len(renderer.Events())-1]
			if answer := segmentTextByKind(preview, card.SegmentText); answer != "最终答案" {
				t.Fatalf("%s answer after replacement = %q", mode, answer)
			}
		})
	}
}

func TestCleanCardsKeepCandidateAnswerOnFailure(t *testing.T) {
	for _, mode := range []config.ReplyMode{config.ReplyModeAppendCleanCard, config.ReplyModeLatestCard} {
		t.Run(string(mode), func(t *testing.T) {
			clock := &fakeStreamClock{now: time.Unix(50, 0)}
			renderer := card.NewFakeRenderer()
			stream := newPreviewTestStreamForMode(t, renderer, clock, 1, 2000, mode)
			if err := stream.Start(); err != nil {
				t.Fatal(err)
			}
			stream.Handle(AgentStreamUpdate{
				Segments:       []card.Segment{{Kind: card.SegmentText, Text: "已完成检查，准备继续。"}},
				Activity:       streamActivityAnswering,
				AnswerSnapshot: true,
			})
			terminal, err := stream.Finish("failed", card.Meta{}, AgentRunResult{
				Segments: []card.Segment{
					{Kind: card.SegmentThought, Text: "已完成检查，准备继续。"},
					{Kind: card.SegmentError, Text: "工具执行失败"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			answer := segmentTextByKind(terminal, card.SegmentText)
			if !containsAll(answer, "已完成检查，准备继续。", "工具执行失败") {
				t.Fatalf("%s failed answer = %q", mode, answer)
			}
		})
	}
}

func TestAgentCardStreamThinkingOnlySchedulesAppendPreview(t *testing.T) {
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
	if got := len(renderer.Events()); got != initialRenders+1 {
		t.Fatalf("thinking-only renders = %d, want %d", got, initialRenders+1)
	}

	stream.Handle(AgentStreamUpdate{
		Incremental: true,
		Activity:    streamActivityAnswering,
		Segments:    []card.Segment{{Kind: card.SegmentText, Text: "answer"}},
	})
	events := renderer.Events()
	if len(events) != initialRenders+2 {
		t.Fatalf("answer checkpoint renders = %d, want %d", len(events), initialRenders+2)
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
	if got := len(renderer.Events()); got != initialRenders+3 {
		t.Fatalf("second thinking-only renders = %d, want %d", got, initialRenders+3)
	}

	stream.Handle(AgentStreamUpdate{
		Activity: streamActivityTool,
		Segments: []card.Segment{{Kind: card.SegmentTool, Text: "Bash(status)", Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "status", Phase: "use"}}},
	})
	clock.Advance(time.Second)
	events = renderer.Events()
	checkpoint := events[len(events)-1]
	assertSegmentKinds(t, checkpoint, card.SegmentThought, card.SegmentText, card.SegmentThought, card.SegmentTool)
	if checkpoint.Activity != streamActivityTool || !strings.Contains(checkpoint.Segments[0].Text, "reasoning") || !strings.Contains(checkpoint.Segments[2].Text, "more") {
		t.Fatalf("tool checkpoint = %#v", checkpoint)
	}

	terminal, err := stream.Finish("completed", card.Meta{}, AgentRunResult{})
	if err != nil {
		t.Fatal(err)
	}
	if len(terminal.Segments) < 3 || terminal.Segments[0].Kind != card.SegmentThought || !strings.Contains(terminal.Segments[0].Text, "reasoning") || terminal.Segments[2].Kind != card.SegmentThought || !strings.Contains(terminal.Segments[2].Text, "more") {
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

func TestCardHeartbeatRefreshesQuietRunningCardAndStopsAtTerminal(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(100, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	stream.heartbeatEvery = 15 * time.Second
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}

	clock.Advance(14 * time.Second)
	if got := len(renderer.Events()); got != 1 {
		t.Fatalf("events before heartbeat = %d, want 1", got)
	}
	clock.Advance(time.Second)
	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events after heartbeat = %d, want 2", len(events))
	}
	heartbeat := events[1]
	if heartbeat.Type != "stream" || !heartbeat.Streaming || !heartbeat.ForceFullUpdate || heartbeat.Message != "任务仍在运行…" {
		t.Fatalf("heartbeat event = %#v", heartbeat)
	}
	if !strings.Contains(heartbeat.HeaderTitle, "15s") {
		t.Fatalf("heartbeat title = %q, want refreshed elapsed time", heartbeat.HeaderTitle)
	}

	if _, err := stream.Finish("completed", card.Meta{}, AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "done"}}}); err != nil {
		t.Fatal(err)
	}
	before := len(renderer.Events())
	clock.Advance(time.Minute)
	if got := len(renderer.Events()); got != before {
		t.Fatalf("events after terminal = %d, want %d", got, before)
	}
}

func TestCardHeartbeatKeepsBodyWithinPreviewLimit(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(100, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 1, 20)
	stream.heartbeatEvery = 15 * time.Second
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}

	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: strings.Repeat("x", 30)}}})
	clock.Advance(15 * time.Second)

	events := renderer.Events()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %#v", len(events), events)
	}
	heartbeat := events[2]
	if !heartbeat.ForceFullUpdate {
		t.Fatalf("heartbeat did not request a full update: %#v", heartbeat)
	}
	if len(heartbeat.Segments) != 2 || heartbeat.Segments[0].Text != "_较早过程已省略_" || !strings.HasSuffix(heartbeat.Segments[1].Text, "x") {
		t.Fatalf("heartbeat body = %#v, want preview-limited tail", heartbeat.Segments)
	}
	totalRunes := 0
	for _, segment := range heartbeat.Segments {
		totalRunes += utf8.RuneCountInString(segment.Text)
	}
	if totalRunes > 20 {
		t.Fatalf("heartbeat body has %d runes, want at most 20", totalRunes)
	}
}

func TestCardHeartbeatRefreshesCurrentRunContextUsage(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(100, 0)}
	dir := t.TempDir()
	cfg := testConfig(t)
	cfg.ClaudeContextUsageDir = dir
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	sess := session.Session{Key: session.Key{Agent: "claude", ChatID: "chat"}, ID: "claude:chat", AgentSessionID: "current"}
	stream := newAgentCardStreamWithClock(svc, "run", sess, session.Input{ReplyToMessageID: "source", Time: clock.Now()}, renderer, nil, clock)
	stream.heartbeatEvery = 15 * time.Second
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	if !renderer.Events()[0].Meta.CtxPending {
		t.Fatalf("initial event must be pending: %#v", renderer.Events()[0].Meta)
	}

	path := filepath.Join(dir, "current.json")
	if err := os.WriteFile(path, []byte(`{"session_id":"current","used_percentage":37,"total_tokens":74000,"context_window_size":200000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	freshAt := clock.Now().Add(time.Second)
	if err := os.Chtimes(path, freshAt, freshAt); err != nil {
		t.Fatal(err)
	}

	clock.Advance(15 * time.Second)
	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events after heartbeat = %d, want 2", len(events))
	}
	got := events[1].Meta
	if !got.CtxOK || got.CtxPending || got.CtxApprox || got.CtxUsedPercent != 37 || got.CtxTokens != 74000 {
		t.Fatalf("heartbeat did not refresh current context usage: %#v", got)
	}
}

func TestCardHeartbeatWaitsForQuietPeriodAfterNormalPreview(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(200, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 1, 2000)
	stream.heartbeatEvery = 15 * time.Second
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}

	clock.Advance(10 * time.Second)
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentText, Text: "output"}}})
	if got := len(renderer.Events()); got != 2 {
		t.Fatalf("events after normal preview = %d, want 2", got)
	}
	clock.Advance(14 * time.Second)
	if got := len(renderer.Events()); got != 2 {
		t.Fatalf("events before reset heartbeat = %d, want 2", got)
	}
	clock.Advance(time.Second)
	if got := len(renderer.Events()); got != 3 {
		t.Fatalf("events after reset heartbeat = %d, want 3", got)
	}
}

func TestStaleCardHeartbeatCannotRenderAfterFinish(t *testing.T) {
	clock := &fakeStreamClock{now: time.Unix(300, 0)}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStream(t, renderer, clock, 30, 2000)
	stream.heartbeatEvery = 15 * time.Second
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.mu.Lock()
	generation := stream.heartbeatGen
	stale := stream.eventLocked(false)
	stream.mu.Unlock()
	if _, err := stream.Finish("completed", card.Meta{}, AgentRunResult{}); err != nil {
		t.Fatal(err)
	}
	before := len(renderer.Events())
	if err := stream.renderHeartbeat(generation, stale); err != nil {
		t.Fatal(err)
	}
	if got := len(renderer.Events()); got != before {
		t.Fatalf("events after stale heartbeat = %d, want %d", got, before)
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
	clock.Advance(2 * time.Hour)
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
	if len(preview.Segments) != 2 || preview.Segments[0].Text != "_较早过程已省略_" || !strings.HasSuffix(preview.Segments[1].Text, "你") {
		t.Fatalf("preview segments = %#v", preview.Segments)
	}
	previewRunes := 0
	for _, segment := range preview.Segments {
		previewRunes += utf8.RuneCountInString(segment.Text)
	}
	if previewRunes > 20 {
		t.Fatalf("preview has %d runes, want at most 20", previewRunes)
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
	if len(segments) != 2 || segments[0].Kind != card.SegmentThought || segments[0].Text != "first" || segments[1].Kind != card.SegmentThought || segments[1].Text != "second + delta" {
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

// TestAppendCleanThreeSectionLatestOnly verifies that append-clean preserves
// separate reasoning/answer/tool sections and timestamps each category's two-item window.
func TestAppendCleanThreeSectionLatestOnly(t *testing.T) {
	clock := &fakeStreamClock{now: time.Date(2026, 7, 26, 11, 32, 0, 0, time.FixedZone("CST", 8*60*60))}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStreamForMode(t, renderer, clock, 1, 4000, config.ReplyModeAppendCleanCard)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}

	// 第 1 轮:思考 delta + 一次工具调用(tool_use → tool_result),以 AssistantSnapshot 收尾。
	// 注意 snapshot 会带完整 thought;delta 阶段的中间态会被 snapshot 权威覆盖,避免重复。
	stream.Handle(AgentStreamUpdate{Activity: streamActivityReasoning, Incremental: true,
		Segments: []card.Segment{{Kind: card.SegmentThought, Text: "先想第一步"}}})
	clock.now = time.Date(2026, 7, 26, 11, 32, 10, 0, clock.now.Location())
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
	clock.now = time.Date(2026, 7, 26, 11, 32, 30, 0, clock.now.Location())
	stream.Handle(AgentStreamUpdate{Activity: streamActivityReasoning, Incremental: true,
		Segments: []card.Segment{{Kind: card.SegmentThought, Text: "再想第二步"}}})
	clock.now = time.Date(2026, 7, 26, 11, 33, 20, 0, clock.now.Location())
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
	if ev.ThoughtExpanded || ev.ToolsExpanded {
		t.Fatalf("running expand state wrong: thought=%v tools=%v", ev.ThoughtExpanded, ev.ToolsExpanded)
	}
	if ev.ThoughtRoundCount != 2 || ev.ToolRoundCount != 2 {
		t.Fatalf("counts = thought:%d tool:%d, want 2/2", ev.ThoughtRoundCount, ev.ToolRoundCount)
	}
	if ev.HeaderTitle != "🛠️ 正在执行工具 · 💭 2 · 🔧 2 · ⏱ 1m20s" {
		t.Fatalf("worker running header = %q", ev.HeaderTitle)
	}
	thought := segmentTextByKind(ev, card.SegmentThought)
	for _, want := range []string{"**🔹 #3 · 11:32:30** · 再想第二步", "**🔹 #1 · 11:32:00** · 先想第一步", cleanTimelineSeparator} {
		if !strings.Contains(thought, want) {
			t.Fatalf("thought timeline missing %q: %q", want, thought)
		}
	}
	if strings.Index(thought, "🔹 #3") > strings.Index(thought, "🔹 #1") {
		t.Fatalf("thought timeline must be newest-first: %q", thought)
	}
	if strings.Contains(thought, "\n\n"+cleanTimelineSeparator) || strings.Contains(thought, cleanTimelineSeparator+"\n\n") {
		t.Fatalf("thought separator must not have blank lines: %q", thought)
	}
	tool := segmentTextByKind(ev, card.SegmentTool)
	for _, want := range []string{
		"**🔹 #4 · 11:33:20** · **Bash**\n`cat x` → `hello`",
		"**🔹 #2 · 11:32:10** · **Bash**\n`ls`\n输出：\n```\nfile1\nfile2\n```",
		cleanTimelineSeparator,
	} {
		if !strings.Contains(tool, want) {
			t.Fatalf("tool timeline missing %q: %q", want, tool)
		}
	}
	if strings.Contains(strings.Split(tool, cleanTimelineSeparator)[0], "```") {
		t.Fatalf("short newest tool values should stay compact: %q", tool)
	}
	if strings.Index(tool, "🔹 #4") > strings.Index(tool, "🔹 #2") {
		t.Fatalf("tool timeline must be newest-first: %q", tool)
	}
	if strings.Contains(tool, "\n\n"+cleanTimelineSeparator) || strings.Contains(tool, cleanTimelineSeparator+"\n\n") {
		t.Fatalf("tool separator must not have blank lines: %q", tool)
	}

	// 终态:思考折叠,计数保持,内容保留最后两次;stop button 隐藏。
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
	if terminal.HeaderTitle != "✅ 已完成 · 💭 2 · 🔧 2 · ⏱ 1m20s" {
		t.Fatalf("worker terminal header = %q", terminal.HeaderTitle)
	}
	if got := segmentTextByKind(terminal, card.SegmentThought); !strings.Contains(got, "🔹 #3 · 11:32:30") || !strings.Contains(got, "🔹 #1 · 11:32:00") {
		t.Fatalf("terminal thought timeline should preserve two thoughts, got %q", got)
	}
	if got := segmentTextByKind(terminal, card.SegmentTool); !strings.Contains(got, "🔹 #4 · 11:33:20") || !strings.Contains(got, "🔹 #2 · 11:32:10") {
		t.Fatalf("terminal tool timeline should preserve two tools, got %q", got)
	}
}

func TestLatestCardUsesAppendCleanThreeSectionLayout(t *testing.T) {
	clock := &fakeStreamClock{now: time.Date(2026, 7, 26, 12, 10, 0, 0, time.FixedZone("CST", 8*60*60))}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStreamForMode(t, renderer, clock, 1, 4000, config.ReplyModeLatestCard)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	stream.Handle(AgentStreamUpdate{Activity: streamActivityReasoning, Incremental: true,
		Segments: []card.Segment{{Kind: card.SegmentThought, Text: "先定位问题"}}})
	clock.Advance(10 * time.Second)
	stream.Handle(AgentStreamUpdate{Activity: streamActivityTool,
		Segments: []card.Segment{{Kind: card.SegmentTool, Text: "- Bash `t1`\n\n```json\n{\"command\":\"go test ./...\"}\n```",
			Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "go test ./...", Phase: "use"}}}})
	stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentTool, Text: "- tool_result `t1`\n\n```\nok\n```",
		Tool: &card.ToolMeta{ID: "t1", Phase: "result"}}}})
	stream.Handle(AgentStreamUpdate{AssistantSnapshot: true,
		Segments: []card.Segment{{Kind: card.SegmentThought, Text: "先定位问题"}, {Kind: card.SegmentText, Text: "最终答复"}}})
	if err := stream.Flush(); err != nil {
		t.Fatal(err)
	}

	preview := renderer.Events()[len(renderer.Events())-1]
	if !preview.ThreeSectionLayout || !preview.ThoughtExpanded || preview.ToolsExpanded {
		t.Fatalf("latest preview layout = %#v, want running append-clean layout", preview)
	}
	if preview.ThoughtRoundCount != 1 || preview.ToolRoundCount != 1 {
		t.Fatalf("latest preview counts = thought:%d tool:%d, want 1/1", preview.ThoughtRoundCount, preview.ToolRoundCount)
	}
	if got := segmentTextByKind(preview, card.SegmentThought); !strings.Contains(got, "🔹 #1 · 12:10:00") || !strings.Contains(got, "先定位问题") {
		t.Fatalf("latest thought timeline = %q", got)
	}
	if got := segmentTextByKind(preview, card.SegmentTool); !strings.Contains(got, "🔹 #2 · 12:10:10") || !strings.Contains(got, "go test ./...") {
		t.Fatalf("latest tool timeline = %q", got)
	}

	terminal, err := stream.Finish("completed", card.Meta{}, AgentRunResult{
		Segments: []card.Segment{
			{Kind: card.SegmentThought, Text: "先定位问题"},
			{Kind: card.SegmentTool, Text: "- Bash `t1`\n\n```json\n{\"command\":\"go test ./...\"}\n```", Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "go test ./...", Phase: "use"}},
			{Kind: card.SegmentTool, Text: "- tool_result `t1`\n\n```\nok\n```", Tool: &card.ToolMeta{ID: "t1", Phase: "result"}},
			{Kind: card.SegmentText, Text: "最终答复"},
		},
		AnswerSegments: []string{"最终答复"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !terminal.ThreeSectionLayout || terminal.ThoughtExpanded || terminal.ToolsExpanded || terminal.StopButton.Visible {
		t.Fatalf("latest terminal layout = %#v, want folded append-clean layout without stop", terminal)
	}
	if got := segmentTextByKind(terminal, card.SegmentThought); !strings.Contains(got, "🔹 #1 · 12:10:00") {
		t.Fatalf("latest terminal thought timeline = %q", got)
	}
	if got := segmentTextByKind(terminal, card.SegmentTool); !strings.Contains(got, "🔹 #2 · 12:10:10") {
		t.Fatalf("latest terminal tool timeline = %q", got)
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

func TestAppendCleanTimelineKeepsTwoUpdatesPerPanel(t *testing.T) {
	clock := &fakeStreamClock{now: time.Date(2026, 7, 26, 11, 30, 0, 0, time.FixedZone("CST", 8*60*60))}
	stream := &agentCardStream{clock: clock}

	for i := 1; i <= 3; i++ {
		thoughtNumber := stream.startCleanUpdateLocked(card.SegmentThought)
		stream.updateCleanThoughtLocked(thoughtNumber, fmt.Sprintf("thought-%d", i))
		clock.Advance(time.Second)

		toolNumber := stream.startCleanUpdateLocked(card.SegmentTool)
		stream.updateCleanToolLocked(toolNumber, &toolCall{Name: "Bash", Cmd: fmt.Sprintf("tool-%d", i)})
		clock.Advance(time.Second)
	}

	if len(stream.cleanUpdates) != 4 {
		t.Fatalf("retained update count=%d, want two thoughts + two tools", len(stream.cleanUpdates))
	}
	stream.thoughtRounds = 3
	stream.toolRounds = 3
	thought := stream.formatCleanTimelineLocked(card.SegmentThought)
	tool := stream.formatCleanTimelineLocked(card.SegmentTool)
	event := stream.eventLocked(false)
	if event.ThoughtOmittedCount != 1 || event.ToolOmittedCount != 1 {
		t.Fatalf("omitted counts = thought:%d tool:%d, want 1/1", event.ThoughtOmittedCount, event.ToolOmittedCount)
	}
	for _, tc := range []struct {
		name      string
		text      string
		newest    string
		second    string
		forbidden string
	}{
		{name: "thought", text: thought, newest: "🔹 #5", second: "🔹 #3", forbidden: "🔹 #1"},
		{name: "tool", text: tool, newest: "🔹 #6", second: "🔹 #4", forbidden: "🔹 #2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range []string{tc.newest, tc.second} {
				if !strings.Contains(tc.text, want) {
					t.Fatalf("timeline missing %q: %q", want, tc.text)
				}
			}
			if strings.Contains(tc.text, tc.forbidden) {
				t.Fatalf("timeline retained oldest update %q: %q", tc.forbidden, tc.text)
			}
			if strings.Contains(tc.text, "已省略") {
				t.Fatalf("timeline omission notice must live in panel title: %q", tc.text)
			}
			if strings.Contains(tc.text, "\n\n"+cleanTimelineSeparator) || strings.Contains(tc.text, cleanTimelineSeparator+"\n\n") {
				t.Fatalf("separator must not have blank lines: %q", tc.text)
			}
		})
	}
}

func TestToolCallFormatCompactFallsBackForComplexValues(t *testing.T) {
	got := (&toolCall{Name: "Bash", Cmd: "printf 'one\\ntwo'\nprintf done", Output: "line one\nline two"}).formatCompact()
	for _, want := range []string{"**Bash**", "```\nprintf 'one\\ntwo'\nprintf done\n```", "输出：\n```\nline one\nline two\n```"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact tool fallback missing %q: %q", want, got)
		}
	}
}

func TestAppendCleanTerminalOnlyResultBuildsTimeline(t *testing.T) {
	clock := &fakeStreamClock{now: time.Date(2026, 7, 26, 12, 0, 1, 0, time.FixedZone("CST", 8*60*60))}
	renderer := card.NewFakeRenderer()
	stream := newPreviewTestStreamForMode(t, renderer, clock, 1, 4000, config.ReplyModeAppendCleanCard)
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	terminal, err := stream.Finish("completed", card.Meta{}, AgentRunResult{
		Segments: []card.Segment{
			{Kind: card.SegmentThought, Text: "定位根因"},
			{Kind: card.SegmentTool, Text: "- Bash `t1`\n\n```json\n{\"command\":\"go test ./...\"}\n```", Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "go test ./...", Phase: "use"}},
			{Kind: card.SegmentTool, Text: "- tool_result `t1`\n\n```\nok\n```", Tool: &card.ToolMeta{ID: "t1", Phase: "result"}},
			{Kind: card.SegmentText, Text: "完成"},
		},
		AnswerSegments: []string{"完成"},
	})
	if err != nil {
		t.Fatal(err)
	}
	thought := segmentTextByKind(terminal, card.SegmentThought)
	for _, want := range []string{"🔹 #1 · 12:00:01", "定位根因"} {
		if !strings.Contains(thought, want) {
			t.Fatalf("terminal-only thought timeline missing %q: %q", want, thought)
		}
	}
	tool := segmentTextByKind(terminal, card.SegmentTool)
	for _, want := range []string{"🔹 #2 · 12:00:01", "go test ./...", "ok"} {
		if !strings.Contains(tool, want) {
			t.Fatalf("terminal-only tool timeline missing %q: %q", want, tool)
		}
	}
}

// TestFormatLatestToolClampsHugeOutputKeepsCommand 回归:超大工具输出时,
// 工具名与命令必须完整保留在卡片里(bug:capacity keepTail 截断从头部吃起,
// 只留输出、丢掉了命令)。修复后由 clampToolOutput 在生成阶段给输出上界,
// 命令永远完整,输出保头尾省中间,整段不再超卡片容量。
func TestFormatLatestToolClampsHugeOutputKeepsCommand(t *testing.T) {
	cmd := "grep -rn func internal/bridge"
	huge := strings.Repeat("internal/bridge/service.go:660:func resumeCardData longline\n", 2000)
	s := &agentCardStream{
		currentTool: &toolCall{
			ID:     "tu-1",
			Name:   "Bash",
			Cmd:    cmd,
			Output: huge,
		},
	}
	rendered := s.formatToolsLocked()

	// 1) 命令与工具名必须完整在渲染结果里
	if !strings.Contains(rendered, cmd) {
		t.Fatalf("命令丢失,rendered 头部=%q", rendered[:min(200, len(rendered))])
	}
	if !strings.Contains(rendered, "**Bash**") {
		t.Fatalf("工具名丢失")
	}
	// 2) 输出被省略(不再原样塞入全部 huge)
	if !strings.Contains(rendered, "中间省略") {
		t.Fatalf("超大输出未被省略")
	}
	if len([]rune(rendered)) >= len([]rune(huge)) {
		t.Fatalf("渲染结果未收缩: %d >= %d", len([]rune(rendered)), len([]rune(huge)))
	}
	// 3) 渲染结果整体受控在预算附近(命令+框架+省略后的输出),远小于卡片 28KB 软上限,
	//    从而下游 capacity 通常无需再对该 tool 段做 keepTail 截断(即便触发,命令也在头部已保住)。
	if got := len([]rune(rendered)); got > maxToolOutputRunes+2000 {
		t.Fatalf("渲染结果仍过大: %d runes", got)
	}
	// 4) 结构完整:命令在前、输出在后
	if strings.Index(rendered, cmd) > strings.Index(rendered, "**输出**") {
		t.Fatalf("命令未排在输出之前")
	}
}
