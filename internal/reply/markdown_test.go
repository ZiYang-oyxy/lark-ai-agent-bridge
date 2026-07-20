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

func TestRenderInlineTimelineOrdersRepliesAndPlainTextTools(t *testing.T) {
	got := RenderInlineTimeline(card.Event{
		Type:      "stream",
		Streaming: true,
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "先检查配置。"},
			{Kind: card.SegmentThought, Text: "private reasoning"},
			{Kind: card.SegmentTool, Text: "private input", Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "git status", Phase: "use"}},
			{Kind: card.SegmentText, Text: "配置正常。"},
			{Kind: card.SegmentTool, Text: "secret output", Tool: &card.ToolMeta{ID: "t1", Phase: "result"}},
		},
	})
	want := "先检查配置。\n\n> ✅ **Bash** · git status\n\n配置正常。"
	if got != want {
		t.Fatalf("timeline = %q, want %q", got, want)
	}
	for _, forbidden := range []string{"private reasoning", "private input", "secret output", "正在输出", "tokens:"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("timeline leaked %q: %q", forbidden, got)
		}
	}
}

func TestRenderInlineTimelineMapsPendingToolsAtTerminal(t *testing.T) {
	base := []card.Segment{{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "sleep 8", Phase: "use"}}}
	if got := RenderInlineTimeline(card.Event{Type: "stopped", Segments: base}); got != "> ⏹ **Bash** · sleep 8" {
		t.Fatalf("stopped = %q", got)
	}
	if got := RenderInlineTimeline(card.Event{Type: "error", Segments: base}); got != "> ⚠️ **Bash** · sleep 8" {
		t.Fatalf("failed = %q", got)
	}
	if got := RenderInlineTimeline(card.Event{Type: "stream", Streaming: true, Segments: base}); got != "> ⏳ **Bash** · sleep 8" {
		t.Fatalf("stream = %q", got)
	}
}

func TestRenderInlineTimelineDeduplicatesResultsAndUsesSafeFallbacks(t *testing.T) {
	got := RenderInlineTimeline(card.Event{Type: "result", Segments: []card.Segment{
		{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "orphan", Phase: "result"}, Text: "orphan secret"},
		{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Summary: "safe", Phase: "use"}, Text: "private input"},
		{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Phase: "result"}, Text: "first secret"},
		{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Phase: "result"}, Text: "second secret"},
	}})
	if got != "> ✅ **Tool** · safe" {
		t.Fatalf("timeline = %q", got)
	}
	for _, forbidden := range []string{"orphan", "private input", "first secret", "second secret"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("timeline leaked %q: %q", forbidden, got)
		}
	}
}

func TestMarkdownCardRendererKeepsFullCardStateAndUsesInlineTimeline(t *testing.T) {
	inner := &fakeMarkdownCardRenderer{ref: session.RenderRef{CardID: "card-1", ReplyMessageID: "reply-1"}}
	renderer := NewMarkdownCardRenderer(inner)
	input := card.Event{
		Type:           "stream",
		Streaming:      true,
		HeaderTitle:    "正在执行工具 · ⏱ 8s",
		HeaderTemplate: "blue",
		Segments: []card.Segment{
			{Kind: card.SegmentThought, Text: "reasoning"},
			{Kind: card.SegmentTool, Text: "secret", Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "pwd", Phase: "use"}},
			{Kind: card.SegmentText, Text: "done"},
		},
		Meta:       card.Meta{Agent: "codex", RunTokens: 10, TotalTokens: 20, User: "user", WorkDir: "/work"},
		StopButton: card.StopButton{Visible: true},
	}
	err := renderer.Render(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(inner.events) != 1 {
		t.Fatalf("events = %#v", inner.events)
	}
	got := inner.events[0]
	if !got.InlineTimelineLayout || got.MarkdownLayout || got.OrderedLayout || got.Markdown != "> ⏳ **Bash** · pwd\n\ndone" {
		t.Fatalf("markdown event = %#v", got)
	}
	if len(got.Segments) != len(input.Segments) || got.StopButton != input.StopButton || got.Meta != input.Meta ||
		got.HeaderTitle != input.HeaderTitle || got.HeaderTemplate != input.HeaderTemplate || got.Streaming != input.Streaming {
		t.Fatalf("markdown event cleared full card state: %#v", got)
	}
	if strings.Contains(got.Markdown, "reasoning") || strings.Contains(got.Markdown, "secret") || strings.Contains(got.Markdown, "tokens:") {
		t.Fatalf("inline timeline leaked shell/private state: %#v", got)
	}
	if ref := renderer.RenderRef(); ref.CardID != "card-1" || ref.ReplyMessageID != "reply-1" {
		t.Fatalf("render ref = %#v", ref)
	}
}
