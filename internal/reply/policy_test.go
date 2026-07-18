package reply

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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
