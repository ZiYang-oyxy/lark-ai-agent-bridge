package reply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/session"
)

type fakeTarget struct {
	newCalls       int
	rehydrateCalls int
	appendCalls    int
	newRenderer    *fakeResumable
	rehydrated     *fakeResumable
	appended       []card.Event
}

type pagedFakeTarget struct {
	renderers []*fakeResumable
	sessions  []string
}

func (f *pagedFakeTarget) NewStreaming(_ context.Context, sessionID, _ string) (feishu.ResumableRenderer, error) {
	r := &fakeResumable{ref: session.RenderRef{CardID: "card-" + sessionID, ReplyMessageID: "reply-" + sessionID}}
	f.renderers = append(f.renderers, r)
	f.sessions = append(f.sessions, sessionID)
	return r, nil
}

func (f *pagedFakeTarget) AppendTerminal(context.Context, string, card.Event) error { return nil }
func (f *pagedFakeTarget) Rehydrate(string, session.RenderRef) feishu.ResumableRenderer {
	return nil
}

func (f *fakeTarget) NewStreaming(context.Context, string, string) (feishu.ResumableRenderer, error) {
	f.newCalls++
	if f.newRenderer == nil {
		f.newRenderer = &fakeResumable{ref: session.RenderRef{CardID: "new-card", ReplyMessageID: "new-reply"}}
	}
	return f.newRenderer, nil
}

func (f *fakeTarget) AppendTerminal(_ context.Context, _ string, event card.Event) error {
	f.appendCalls++
	f.appended = append(f.appended, event)
	return nil
}

func (f *fakeTarget) Rehydrate(_ string, ref session.RenderRef) feishu.ResumableRenderer {
	f.rehydrateCalls++
	if f.rehydrated == nil {
		f.rehydrated = &fakeResumable{ref: ref}
	}
	return f.rehydrated
}

type fakeResumable struct {
	ref      session.RenderRef
	events   []card.Event
	failCall int
	err      error
}

type alwaysUnknownResolver struct{}

func (alwaysUnknownResolver) RenderRefSequenceUnknown(session.RenderRef) bool { return true }

func (f *fakeResumable) Render(event card.Event) error {
	call := len(f.events) + 1
	f.events = append(f.events, event)
	if call == f.failCall {
		return f.err
	}
	f.ref.Version++
	return nil
}

func (f *fakeResumable) RenderRef() session.RenderRef { return f.ref }

func TestPolicyAppendUsesOneStreamingCard(t *testing.T) {
	target := &fakeTarget{}
	run, err := NewPolicy(target, nil).Begin(context.Background(), configMode("append"), "scope", "run", "source")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Render(card.Event{Type: "stream"}); err != nil {
		t.Fatal(err)
	}
	if err := run.Render(card.Event{Type: "result"}); err != nil {
		t.Fatal(err)
	}
	if target.newCalls != 1 || target.appendCalls != 0 || len(target.newRenderer.events) != 2 {
		t.Fatalf("new/append/events = %d/%d/%d", target.newCalls, target.appendCalls, len(target.newRenderer.events))
	}
}

func TestPolicyAppendContinuationCreatesAtMostNineCardsAndReusesSlidingWindow(t *testing.T) {
	target := &pagedFakeTarget{}
	run, err := NewPolicy(target, nil).Begin(context.Background(), config.ReplyModeAppend, "scope", "run", "source")
	if err != nil {
		t.Fatal(err)
	}
	var persisted []session.RenderRef
	run.SetActiveRefChanged(func(ref session.RenderRef) error {
		persisted = append(persisted, ref)
		return nil
	})
	two := []card.Event{
		{Type: "stream", Streaming: true, MarkdownLayout: true, Markdown: "page-1"},
		{Type: "stream", Streaming: true, MarkdownLayout: true, Markdown: "page-2"},
	}
	if err := run.RenderPages(two); err != nil {
		t.Fatal(err)
	}
	if len(target.renderers) != 2 || len(persisted) != 1 || persisted[0].CardID != "card-run:page:2" {
		t.Fatalf("renderers/persisted = %d/%#v", len(target.renderers), persisted)
	}
	if got := target.renderers[0].events[0]; got.Streaming || got.StopButton.Visible {
		t.Fatalf("frozen first page = %#v", got)
	}

	nine := make([]card.Event, maxContinuationCards)
	for i := range nine {
		nine[i] = card.Event{Type: "stream", Streaming: true, MarkdownLayout: true, Markdown: fmt.Sprintf("window-a-%d", i+1)}
	}
	if err := run.RenderPages(nine); err != nil {
		t.Fatal(err)
	}
	if len(target.renderers) != maxContinuationCards || len(persisted) != maxContinuationCards-1 {
		t.Fatalf("nine-card state = renderers %d persisted %d", len(target.renderers), len(persisted))
	}
	for i := range nine {
		nine[i].Markdown = fmt.Sprintf("window-b-%d", i+1)
	}
	if err := run.RenderPages(nine); err != nil {
		t.Fatal(err)
	}
	if len(target.renderers) != maxContinuationCards {
		t.Fatalf("sliding window created extra cards: %d", len(target.renderers))
	}
	for i, renderer := range target.renderers {
		last := renderer.events[len(renderer.events)-1]
		if last.Markdown != fmt.Sprintf("window-b-%d", i+1) {
			t.Fatalf("page %d markdown = %q", i+1, last.Markdown)
		}
	}

	before := len(target.renderers[0].events)
	if err := run.RenderPages(nine); err != nil {
		t.Fatal(err)
	}
	if len(target.renderers[0].events) != before {
		t.Fatal("unchanged page was rendered again")
	}
	if got := run.RenderRef(); got.CardID != "card-run:page:9" {
		t.Fatalf("active ref = %#v", got)
	}
}

func TestPolicyAppendCleanCardRemovesProcessPanelsAndFallsBackOnce(t *testing.T) {
	terminal := card.Event{
		Type: "result",
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "answer"},
			{Kind: card.SegmentThought, Text: "private thought"},
			{Kind: card.SegmentTool, Text: "tool process"},
		},
		Meta: card.Meta{ModelInfo: card.ModelInfo{Actual: "actual"}, Tokens: 7, WorkDir: "/work", Status: "completed"},
	}
	target := &fakeTarget{newRenderer: &fakeResumable{ref: session.RenderRef{CardID: "card"}, failCall: 2, err: errors.New("terminal update failed")}}
	run, err := NewPolicy(target, nil).Begin(context.Background(), configMode("append-clean-card"), "scope", "run", "source")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Render(card.Event{Type: "stream", Segments: terminal.Segments}); err != nil {
		t.Fatal(err)
	}
	if err := run.Render(terminal); err != nil {
		t.Fatalf("clean terminal fallback error = %v", err)
	}
	if target.appendCalls != 1 || len(target.appended) != 1 {
		t.Fatalf("append fallback calls/events = %d/%d", target.appendCalls, len(target.appended))
	}
	clean := target.appended[0]
	if len(clean.Segments) != 1 || clean.Segments[0].Kind != card.SegmentText || clean.Segments[0].Text != "answer" {
		t.Fatalf("clean terminal segments = %#v", clean.Segments)
	}
	if clean.Meta.ModelInfo.Actual != "actual" || clean.Meta.Tokens != 7 || clean.Meta.WorkDir != "/work" || clean.Meta.Status != "completed" {
		t.Fatalf("clean terminal meta = %#v", clean.Meta)
	}
	if !clean.HideAgentPanels {
		t.Fatal("clean terminal did not hide thought/tool panels")
	}
}

func TestPolicyLatestCardRehydratesAndPersistsAdvancedRef(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	old := &session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4}
	if err := store.SetLatest("scope", old); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{}
	run, err := NewPolicy(target, store).Begin(context.Background(), configMode("latest-card"), "scope", "run", "source")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Render(card.Event{Type: "stream"}); err != nil {
		t.Fatal(err)
	}
	got := store.GetLatest("scope")
	if target.rehydrateCalls != 1 || target.newCalls != 0 || got == nil || got.CardID != "old-card" || got.Version != 5 {
		t.Fatalf("rehydrate/new/latest = %d/%d/%#v", target.rehydrateCalls, target.newCalls, got)
	}
}

func TestPolicyLatestCardCleansTerminalAndPersistsAdvancedRef(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	old := &session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4}
	if err := store.SetLatest("scope", old); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{}
	run, err := NewPolicy(target, store).Begin(context.Background(), config.ReplyModeLatestCard, "scope", "run", "source")
	if err != nil {
		t.Fatal(err)
	}
	terminal := card.Event{
		Type: "result",
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "answer"},
			{Kind: card.SegmentThought, Text: "private thought"},
			{Kind: card.SegmentTool, Text: "tool process"},
		},
		Streaming:       true,
		Activity:        "tool",
		ProcessExpanded: true,
	}
	if err := run.Render(terminal); err != nil {
		t.Fatal(err)
	}
	if len(target.rehydrated.events) != 1 {
		t.Fatalf("terminal events = %#v", target.rehydrated.events)
	}
	clean := target.rehydrated.events[0]
	if len(clean.Segments) != 1 || clean.Segments[0].Kind != card.SegmentText || clean.Segments[0].Text != "answer" {
		t.Fatalf("clean terminal segments = %#v", clean.Segments)
	}
	if clean.Streaming || clean.Activity != "" || clean.ProcessExpanded || !clean.HideAgentPanels {
		t.Fatalf("clean terminal state = %#v", clean)
	}
	got := store.GetLatest("scope")
	if got == nil || got.CardID != old.CardID || got.Version != old.Version+1 {
		t.Fatalf("latest ref = %#v, want advanced %#v", got, old)
	}
}

func TestPolicyLatestCardDiscardsResolverUnknownRef(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	old := &session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4}
	if err := store.SetLatest("scope", old); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{}
	policy := NewPolicy(target, store)
	policy.Resolver = alwaysUnknownResolver{}
	if _, err := policy.Begin(context.Background(), configMode("latest-card"), "scope", "run", "source"); err != nil {
		t.Fatal(err)
	}
	if target.rehydrateCalls != 0 || target.newCalls != 1 {
		t.Fatalf("rehydrate/new = %d/%d", target.rehydrateCalls, target.newCalls)
	}
}

func TestPolicyLatestCardExpiresUnsafeReferencesAtBegin(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		ref           session.RenderRef
		wantRehydrate int
		wantNew       int
	}{
		{name: "fresh", ref: session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4, CreatedAt: now.Add(-latestCardTTL + time.Nanosecond)}, wantRehydrate: 1},
		{name: "at ttl", ref: session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4, CreatedAt: now.Add(-latestCardTTL)}, wantNew: 1},
		{name: "expired", ref: session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4, CreatedAt: now.Add(-latestCardTTL - time.Nanosecond)}, wantNew: 1},
		{name: "legacy zero time", ref: session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4}, wantRehydrate: 1},
		{name: "sequence unknown", ref: session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4, CreatedAt: now.Add(-time.Hour), SequenceUnknown: true}, wantNew: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "replies.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SetLatest("scope", &tc.ref); err != nil {
				t.Fatal(err)
			}
			target := &fakeTarget{}
			policy := NewPolicy(target, store)
			policy.now = func() time.Time { return now }
			run, err := policy.Begin(context.Background(), configMode("latest-card"), "scope", "run", "source")
			if err != nil {
				t.Fatal(err)
			}
			if target.rehydrateCalls != tc.wantRehydrate || target.newCalls != tc.wantNew {
				t.Fatalf("Begin rehydrate/new = %d/%d, want %d/%d", target.rehydrateCalls, target.newCalls, tc.wantRehydrate, tc.wantNew)
			}
			if tc.wantNew != 0 && store.GetLatest("scope") != nil {
				t.Fatalf("unsafe mapping remains before first render: %#v", store.GetLatest("scope"))
			}
			if err := run.Render(card.Event{Type: "stream"}); err != nil {
				t.Fatal(err)
			}
			got := store.GetLatest("scope")
			if got == nil || (tc.wantNew != 0 && got.CardID != "new-card") {
				t.Fatalf("latest after render = %#v", got)
			}
		})
	}
}

func TestPolicyLatestCardClearFailurePreventsNewCard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replies.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SetLatest("scope", &session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", CreatedAt: now.Add(-latestCardTTL)}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{}
	policy := NewPolicy(target, store)
	policy.now = func() time.Time { return now }
	if _, err := policy.Begin(context.Background(), configMode("latest-card"), "scope", "run", "source"); err == nil {
		t.Fatal("Begin() error = nil, want clearing error")
	}
	if target.newCalls != 0 {
		t.Fatalf("new calls = %d, want 0", target.newCalls)
	}
}

func TestPolicyLatestCardReplacesOnlyStaleMapping(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		wantError bool
		wantNew   int
		wantCard  string
	}{
		{name: "stale", err: feishu.ErrStaleRenderRef, wantNew: 1, wantCard: "new-card"},
		{name: "transient", err: errors.New("network down"), wantError: true, wantCard: "old-card"},
		{name: "server", err: &feishu.FeishuAPIError{HTTPStatus: 500, Code: 999}, wantError: true, wantCard: "old-card"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "replies.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SetLatest("scope", &session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 2}); err != nil {
				t.Fatal(err)
			}
			target := &fakeTarget{rehydrated: &fakeResumable{ref: session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 2}, failCall: 1, err: tc.err}}
			run, err := NewPolicy(target, store).Begin(context.Background(), configMode("latest-card"), "scope", "run", "source")
			if err != nil {
				t.Fatal(err)
			}
			err = run.Render(card.Event{Type: "stream"})
			if (err != nil) != tc.wantError {
				t.Fatalf("Render() error = %v, wantError %t", err, tc.wantError)
			}
			got := store.GetLatest("scope")
			if target.newCalls != tc.wantNew || got == nil || got.CardID != tc.wantCard {
				t.Fatalf("new/latest = %d/%#v", target.newCalls, got)
			}
		})
	}
}

func configMode(value string) config.ReplyMode {
	return config.ReplyMode(value)
}

// TestCleanTerminalEventKeepsPanelsForThreeSectionLayout 锁死:三段布局(append-clean-card
// 走的新分支)在终态保留 thought/tool 的「最新一次」用于渲染折叠区,不再被 clean 清空;
// 旧的合并 panel_process 分支(ThreeSectionLayout=false)沿用「终态只留正文」的老语义。
func TestCleanTerminalEventKeepsPanelsForThreeSectionLayout(t *testing.T) {
	three := card.Event{
		Type:               "result",
		ThreeSectionLayout: true,
		ThoughtExpanded:    true,
		ToolsExpanded:      true,
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "最终答复"},
			{Kind: card.SegmentThought, Text: "最新一轮思考"},
			{Kind: card.SegmentTool, Text: "Bash(ls)"},
		},
	}
	out := cleanTerminalEvent(three)
	kinds := map[card.SegmentKind]bool{}
	for _, s := range out.Segments {
		kinds[s.Kind] = true
	}
	if !kinds[card.SegmentText] || !kinds[card.SegmentThought] || !kinds[card.SegmentTool] {
		t.Fatalf("three-section terminal should keep text+thought+tool, got %#v", out.Segments)
	}
	if out.HideAgentPanels {
		t.Fatalf("three-section terminal must not hide agent panels")
	}
	if out.Streaming || out.ThoughtExpanded || out.ToolsExpanded {
		t.Fatalf("three-section terminal must be non-streaming with panels folded, got %#v", out)
	}

	legacy := card.Event{
		Type: "result",
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "最终答复"},
			{Kind: card.SegmentThought, Text: "过程思考"},
			{Kind: card.SegmentTool, Text: "Bash(ls)"},
		},
	}
	out2 := cleanTerminalEvent(legacy)
	for _, s := range out2.Segments {
		if s.Kind == card.SegmentThought || s.Kind == card.SegmentTool {
			t.Fatalf("legacy clean terminal must drop thought/tool, got %#v", out2.Segments)
		}
	}
	if !out2.HideAgentPanels {
		t.Fatalf("legacy clean terminal should set HideAgentPanels")
	}
}
