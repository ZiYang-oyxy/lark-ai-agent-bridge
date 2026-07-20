package bridge

import (
	"errors"
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
			stream.Handle(AgentStreamUpdate{Segments: []card.Segment{{Kind: card.SegmentTool, Text: "Bash(ls)"}}})
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
