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
	t.Helper()
	cfg := testConfig(t)
	cfg.CardUpdateEvery = 800 * time.Millisecond
	cfg.CardMinDeltaChars = minDelta
	cfg.CardPreviewMaxChars = maxPreview
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	sess := session.Session{ID: "claude:chat", Tokens: 2}
	input := session.Input{ReplyToMessageID: "source", RequestedModel: "default", RequestedEffort: "low", Time: clock.Now()}
	return newAgentCardStreamWithClock(svc, "run", sess, input, renderer, nil, clock)
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
	if terminal.Type != "result" || len(terminal.Segments) != 1 || terminal.Segments[0].Text != full+"尾" {
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
