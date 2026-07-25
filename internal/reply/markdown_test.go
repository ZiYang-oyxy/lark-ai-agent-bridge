package reply

import (
	"strings"
	"testing"
	"unicode/utf8"

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
	want := "checking\n\n> ✅ **Bash** — git status\n\ndone"
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
	want := "> ⏳ **Read** — README.md\n\n_🧰 正在调用工具…_"
	if got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
}

func TestRenderMarkdownCompleteTerminalFooter(t *testing.T) {
	got := RenderMarkdown(card.Event{
		Type:     "result",
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "done"}},
		Meta: card.Meta{
			Agent:       "claude",
			SessionID:   "a1b2c3d4-e5f6",
			ModelInfo:   card.ModelInfo{Actual: "claude-opus-4-8[1m]", Effort: "default"},
			RunTokens:   21_743_200,
			TotalTokens: 29_261_500,
			User:        "developer",
			IP:          "192.0.2.10",
			WorkDir:     "/workspace/lark-agent-workspace",
		},
	})
	// meta footer(agent/会话ID/模型/tokens · user/ip/workdir)已整体停用,只剩正文。
	want := "done"
	if got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
}

// meta footer 已整体停用,仅有 meta 而无正文时应回落到"未返回内容"占位,不再拼 footer。
func TestRenderMarkdownMetaOnlyHasNoFooter(t *testing.T) {
	got := RenderMarkdown(card.Event{Type: "result", Meta: card.Meta{
		Agent: "claude", SessionID: "a1b2c3d4", User: "developer", WorkDir: "/repo",
	}})
	if strings.ContainsAny(got, "🍊⚙️🧠🔢👤🖥️") || strings.Contains(got, "📁") {
		t.Fatalf("markdown still carries meta footer: %q", got)
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
			if !strings.Contains(got, "— "+tt.want+"\n\n_🧰 正在调用工具…_") {
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
			want := "> " + test.status + " **Bash** — " + wantSummary
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
	want := "> ❌ **Bash** — false\n\n_⏹ 已停止_"
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
	want := "先检查配置。\n\n> ✅ **Bash** — git status\n\n配置正常。\n\n_🧠 正在思考…_"
	if got != want {
		t.Fatalf("timeline = %q, want %q", got, want)
	}
	for _, forbidden := range []string{"private reasoning", "private input", "secret output", "tokens:"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("timeline leaked %q: %q", forbidden, got)
		}
	}
}

func TestRenderInlineTimelineMapsPendingToolsAtTerminal(t *testing.T) {
	base := []card.Segment{{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "sleep 8", Phase: "use"}}}
	if got := RenderInlineTimeline(card.Event{Type: "stopped", Segments: base}); got != "> ⏳ **Bash** — sleep 8\n\n_⏹ 已被中断_" {
		t.Fatalf("stopped = %q", got)
	}
	if got := RenderInlineTimeline(card.Event{Type: "error", Segments: base}); got != "> ⏳ **Bash** — sleep 8\n\n_⚠️ 执行失败_" {
		t.Fatalf("failed = %q", got)
	}
	if got := RenderInlineTimeline(card.Event{Type: "stream", Streaming: true, Activity: "tool", Segments: base}); got != "> ⏳ **Bash** — sleep 8\n\n_🧰 正在调用工具…_" {
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
	if got != "> ✅ **Tool** — safe" {
		t.Fatalf("timeline = %q", got)
	}
	for _, forbidden := range []string{"orphan", "private input", "first secret", "second secret"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("timeline leaked %q: %q", forbidden, got)
		}
	}
}

func TestRenderInlineTimelineBoundsAndRedactsToolSummary(t *testing.T) {
	summary := "Authorization: Bearer abc.def " + strings.Repeat("界", 100)
	got := RenderInlineTimeline(card.Event{Type: "stream", Segments: []card.Segment{{
		Kind: card.SegmentTool,
		Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: summary, Phase: "use"},
	}}})
	const prefix = "> ⏳ **Bash** — "
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("tool line = %q", got)
	}
	visibleSummary := strings.TrimPrefix(got, prefix)
	if utf8.RuneCountInString(visibleSummary) > toolHeaderSummaryMaxRunes+3 {
		t.Fatalf("summary not bounded: runes=%d value=%q", utf8.RuneCountInString(visibleSummary), visibleSummary)
	}
	if strings.Contains(got, "abc.def") || !strings.Contains(got, "REDACTED") || !strings.HasSuffix(got, "…") {
		t.Fatalf("summary not redacted/truncated: %q", got)
	}
}

func TestRenderInlineTimelineDropsOldEntriesAndKeepsFinalReply(t *testing.T) {
	final := "FINAL_REPLY"
	oldBeginning := "OLD_BEGINNING_MUST_BE_DROPPED"
	got := RenderInlineTimeline(card.Event{Type: "result", Segments: []card.Segment{
		{Kind: card.SegmentText, Text: oldBeginning + strings.Repeat("旧", inlineTimelineMaxRunes)},
		{Kind: card.SegmentTool, Tool: &card.ToolMeta{ID: "t1", Name: "Bash", Summary: "status", Phase: "use"}},
		{Kind: card.SegmentText, Text: final},
	}})
	if utf8.RuneCountInString(got) > inlineTimelineMaxRunes {
		t.Fatalf("timeline exceeds budget: runes=%d", utf8.RuneCountInString(got))
	}
	if !strings.Contains(got, "较早过程已省略") || !strings.HasSuffix(got, final) {
		t.Fatalf("timeline did not preserve final reply: %q", got)
	}
	if strings.Contains(got, oldBeginning) {
		t.Fatalf("oldest timeline prefix survived: %q", got)
	}
	if !strings.Contains(got, "> ⏳ **Bash** — status") {
		t.Fatalf("complete tool line was not preserved: %q", got)
	}
}

func TestRenderInlineTimelineKeepsTailOfOversizedLatestReply(t *testing.T) {
	beginning := "BEGINNING_MUST_BE_DROPPED"
	ending := "LATEST_TAIL_MUST_SURVIVE"
	final := beginning + strings.Repeat("中", inlineTimelineMaxRunes) + ending
	got := RenderInlineTimeline(card.Event{
		Type:      "stream",
		Streaming: true,
		Activity:  "answering",
		Segments:  []card.Segment{{Kind: card.SegmentText, Text: final}},
	})
	if utf8.RuneCountInString(got) > inlineTimelineMaxRunes {
		t.Fatalf("timeline exceeds budget: runes=%d", utf8.RuneCountInString(got))
	}
	if !strings.Contains(got, "较早过程已省略") || strings.Contains(got, beginning) || !strings.Contains(got, ending) {
		t.Fatalf("timeline did not keep the latest tail: %q", got)
	}
	if !strings.HasSuffix(got, "_✍️ 正在输出…_") {
		t.Fatalf("timeline lost the current status footer: %q", got)
	}
}

func TestMarkdownCardRendererHidesThinkingWithoutMutatingCaller(t *testing.T) {
	inner := &fakeMarkdownCardRenderer{}
	renderer := NewMarkdownCardRenderer(inner)
	original := "private reasoning"
	event := card.Event{Type: "stream", Streaming: true, Activity: "reasoning", Segments: []card.Segment{{Kind: card.SegmentThought, Text: original}}}
	if err := renderer.Render(event); err != nil {
		t.Fatal(err)
	}
	got := inner.events[0]
	if got.Markdown != "_🧠 正在思考…_" || strings.Contains(got.Markdown, original) {
		t.Fatalf("thinking projection = %#v", got)
	}
	if got.Segments[0].Text != original || event.Segments[0].Text != original {
		t.Fatal("adapter mutated caller segments")
	}
}

func TestMarkdownCardRendererKeepsStateAndUsesLightweightMarkdownLayout(t *testing.T) {
	inner := &fakeMarkdownCardRenderer{ref: session.RenderRef{CardID: "card-1", ReplyMessageID: "reply-1"}}
	renderer := NewMarkdownCardRenderer(inner)
	input := card.Event{
		Type:           "stream",
		Streaming:      true,
		Activity:       "tool",
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
	if got.InlineTimelineLayout || !got.MarkdownLayout || got.OrderedLayout || got.Markdown != "> ⏳ **Bash** — pwd\n\ndone\n\n_🧰 正在调用工具…_" {
		t.Fatalf("markdown event = %#v", got)
	}
	if len(got.Segments) != len(input.Segments) || got.StopButton != input.StopButton || got.Meta != input.Meta ||
		got.HeaderTitle != input.HeaderTitle || got.HeaderTemplate != input.HeaderTemplate || got.Streaming != input.Streaming {
		t.Fatalf("markdown event cleared full card state: %#v", got)
	}
	if strings.Contains(got.Markdown, "reasoning") || strings.Contains(got.Markdown, "secret") || strings.Contains(got.Markdown, "tokens:") {
		t.Fatalf("inline timeline leaked shell/private state: %#v", got)
	}
	payload := card.BuildLarkCard(got)
	if _, exists := payload["header"]; exists {
		t.Fatalf("lightweight append card unexpectedly has a header: %#v", payload)
	}
	elements := payload["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 2 || elements[0].(map[string]any)["element_id"] != "card_title_actions" {
		t.Fatalf("lightweight append title row = %#v", elements)
	}
	row := elements[0].(map[string]any)
	columns := row["columns"].([]any)
	buttonColumn := columns[1].(map[string]any)
	button := buttonColumn["elements"].([]any)[0].(map[string]any)
	if button["element_id"] != "btn_stop" || button["disabled"] != false {
		t.Fatalf("lightweight append stop button = %#v", button)
	}
	answer := elements[1].(map[string]any)
	if answer["tag"] != "markdown" || answer["content"] != got.Markdown {
		t.Fatalf("lightweight append answer = %#v", answer)
	}
	if ref := renderer.RenderRef(); ref.CardID != "card-1" || ref.ReplyMessageID != "reply-1" {
		t.Fatalf("render ref = %#v", ref)
	}
}
