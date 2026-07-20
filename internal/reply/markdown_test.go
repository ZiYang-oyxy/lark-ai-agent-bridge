package reply

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/session"
)

type fakeMarkdownCardRenderer struct {
	events []card.Event
	ref    session.RenderRef
}

func (f *fakeMarkdownCardRenderer) Render(event card.Event) error {
	f.events = append(f.events, event)
	return nil
}

func (f *fakeMarkdownCardRenderer) RenderRef() session.RenderRef { return f.ref }

func TestRenderMarkdownHidesThinkingAndCollapsesToolLifecycle(t *testing.T) {
	got := RenderMarkdown(card.Event{
		Type: "result",
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "checking"},
			{Kind: card.SegmentThought, Text: "private reasoning"},
			{Kind: card.SegmentTool, Text: "private input", Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "git status", Phase: "use"}},
			{Kind: card.SegmentTool, Text: "secret output", Tool: &card.ToolMeta{ID: "t1", Phase: "result"}},
			{Kind: card.SegmentText, Text: "done"},
		},
		Meta: card.Meta{Agent: "codex", RunTokens: 107600, TotalTokens: 107600},
	})
	want := "checking\n\n> ✅ **Bash** · git status\n\ndone\n\n🤖 codex · 🔢 tokens: ▶ 107.6k / ∑ 107.6k"
	if got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
	for _, private := range []string{"private reasoning", "private input", "secret output"} {
		if strings.Contains(got, private) {
			t.Fatalf("markdown leaked %q: %q", private, got)
		}
	}
}

func TestRenderMarkdownShowsOneDerivedRunningStatus(t *testing.T) {
	got := RenderMarkdown(card.Event{
		Type:      "stream",
		Streaming: true,
		Activity:  "tool",
		Segments: []card.Segment{
			{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Name: "Read", Summary: "README.md", Phase: "use"}, Text: "private"},
		},
		Meta: card.Meta{Agent: "claude", RunTokens: 9, TotalTokens: 10},
	})
	want := "> ⏳ **Read** · README.md\n\n_🧰 正在调用工具…_"
	if got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
}

func TestRenderMarkdownBoundsInlineToolSummary(t *testing.T) {
	longASCII := strings.Repeat("a", 81)
	longUnicode := strings.Repeat("界", 81)
	tests := []struct {
		name    string
		summary string
		want    string
	}{
		{name: "ascii", summary: longASCII, want: strings.Repeat("a", 80) + "…"},
		{name: "unicode", summary: longUnicode, want: strings.Repeat("界", 80) + "…"},
		{name: "whitespace", summary: "git  status\n--short", want: "git status --short"},
		{name: "markdown", summary: strings.Repeat("x", 79) + "*tail", want: strings.Repeat("x", 79) + "\\*…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderMarkdown(card.Event{
				Type:      "stream",
				Streaming: true,
				Activity:  "tool",
				Segments: []card.Segment{{
					Kind: card.SegmentTool,
					Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: tt.summary, Phase: "use"},
				}},
			})
			if !strings.Contains(got, "· "+tt.want+"\n\n_🧰 正在调用工具…_") {
				t.Fatalf("markdown = %q, want summary %q", got, tt.want)
			}
		})
	}
}

func TestRenderMarkdownKeepsBoundedSummaryAcrossToolLifecycle(t *testing.T) {
	summary := strings.Repeat("c", 81)
	wantSummary := strings.Repeat("c", 80) + "…"
	for _, test := range []struct {
		name    string
		isError bool
		status  string
	}{
		{name: "done", status: "✅"},
		{name: "error", isError: true, status: "❌"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := RenderMarkdown(card.Event{
				Type: "result",
				Segments: []card.Segment{
					{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: summary, Phase: "use"}},
					{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Phase: "result", IsError: test.isError}},
				},
			})
			want := "> " + test.status + " **Bash** · " + wantSummary
			if got != want {
				t.Fatalf("markdown = %q, want %q", got, want)
			}
		})
	}
}

func TestRenderMarkdownShowsFailedToolAndTerminalState(t *testing.T) {
	got := RenderMarkdown(card.Event{
		Type: "stopped",
		Segments: []card.Segment{
			{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "false", Phase: "use"}, Text: "private"},
			{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Phase: "result", IsError: true}, Text: "secret"},
		},
		Meta: card.Meta{Agent: "claude"},
	})
	want := "> ❌ **Bash** · false\n\n_⏹ 已停止_\n\n🤖 Claude"
	if got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
}

func TestRenderMarkdownHandlesErrorAndEmptyResult(t *testing.T) {
	if got := RenderMarkdown(card.Event{Type: "error", Segments: []card.Segment{{Kind: card.SegmentError, Text: "boom"}}}); got != "⚠️ Agent 执行失败：boom" {
		t.Fatalf("error markdown = %q", got)
	}
	if got := RenderMarkdown(card.Event{Type: "result"}); got != "_（未返回内容）_" {
		t.Fatalf("empty markdown = %q", got)
	}
}

func TestMarkdownCardRendererConvertsRunToMinimalLayout(t *testing.T) {
	inner := &fakeMarkdownCardRenderer{ref: session.RenderRef{CardID: "card-1", ReplyMessageID: "reply-1"}}
	renderer := NewMarkdownCardRenderer(inner)
	err := renderer.Render(card.Event{
		Type:      "result",
		Streaming: false,
		Segments: []card.Segment{
			{Kind: card.SegmentThought, Text: "private"},
			{Kind: card.SegmentTool, Text: "secret", Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "pwd", Phase: "use"}},
			{Kind: card.SegmentTool, Text: "/private/output", Tool: &card.ToolMeta{ID: "t1", Phase: "result"}},
			{Kind: card.SegmentText, Text: "done"},
		},
		Meta:       card.Meta{Agent: "codex", RunTokens: 10, TotalTokens: 20},
		StopButton: card.StopButton{Visible: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inner.events) != 1 {
		t.Fatalf("events = %#v", inner.events)
	}
	got := inner.events[0]
	if !got.MarkdownLayout || got.Markdown != "> ✅ **Bash** · pwd\n\ndone\n\n🤖 codex · 🔢 tokens: ▶ 10 / ∑ 20" {
		t.Fatalf("markdown event = %#v", got)
	}
	if len(got.Segments) != 0 || got.StopButton.Visible || got.Meta != (card.Meta{}) || strings.Contains(got.Markdown, "private") {
		t.Fatalf("markdown event leaked card state: %#v", got)
	}
	if ref := renderer.RenderRef(); ref.CardID != "card-1" || ref.ReplyMessageID != "reply-1" {
		t.Fatalf("render ref = %#v", ref)
	}
}
