package reply

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/card"
)

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
