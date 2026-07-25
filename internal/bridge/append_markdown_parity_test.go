package bridge

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/reply"
)

func TestAppendMarkdownClaudeAndCodexToolProjection(t *testing.T) {
	claudeInput := strings.Join([]string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private plan"},{"type":"tool_use","id":"claude-tool","name":"Bash","input":{"command":"pwd"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"claude-tool","content":"/repo"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"result","result":"done"}`,
	}, "\n")
	claudeResult, err := parseClaudeStream(strings.NewReader(claudeInput), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	codexInput := strings.Join([]string{
		`{"type":"thread.started","thread_id":"codex-thread"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"codex-tool","type":"command_execution","command":"pwd"}}`,
		`{"type":"item.completed","item":{"id":"codex-tool","type":"command_execution","output":"/repo","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"message","type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	codexResult, err := parseCodexStream(strings.NewReader(codexInput), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	claudeMarkdown := reply.RenderInlineTimeline(card.Event{Type: "result", Segments: claudeResult.OrderedSegments})
	codexMarkdown := reply.RenderInlineTimeline(card.Event{Type: "result", Segments: codexResult.OrderedSegments})
	claudeWant := "> ✅ **Bash** — pwd\n\ndone"
	codexWant := "> ✅ **command\\_execution** — pwd\n\ndone"
	if claudeMarkdown != claudeWant || codexMarkdown != codexWant {
		t.Fatalf("append markdown projection:\nClaude: %q\nCodex:  %q\nWant Claude: %q\nWant Codex: %q", claudeMarkdown, codexMarkdown, claudeWant, codexWant)
	}
	for _, hidden := range []string{"private plan", "/repo"} {
		if strings.Contains(claudeMarkdown, hidden) || strings.Contains(codexMarkdown, hidden) {
			t.Fatalf("append markdown leaked %q: Claude=%q Codex=%q", hidden, claudeMarkdown, codexMarkdown)
		}
	}
}

func TestAppendMarkdownCodexEmptyToolResultsReachTerminalState(t *testing.T) {
	for _, test := range []struct {
		name       string
		resultLine string
		status     string
	}{
		{name: "success", resultLine: `{"type":"item.completed","item":{"id":"codex-tool","type":"command_execution","output":"","exit_code":0}}`, status: "✅"},
		{name: "failure", resultLine: `{"type":"item.completed","item":{"id":"codex-tool","type":"command_execution","output":"","exit_code":1}}`, status: "❌"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Join([]string{
				`{"type":"item.started","item":{"id":"codex-tool","type":"command_execution","command":"touch marker"}}`,
				test.resultLine,
				`{"type":"turn.completed"}`,
			}, "\n")
			var streamed []card.Segment
			result, err := parseCodexStream(strings.NewReader(input), nil, func(update AgentStreamUpdate) {
				for _, segment := range update.Segments {
					streamed = appendOrderedSegment(streamed, segment, update.Incremental)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			want := "> " + test.status + " **command\\_execution** — touch marker"
			if got := reply.RenderInlineTimeline(card.Event{Type: "result", Segments: result.OrderedSegments}); got != want {
				t.Fatalf("terminal markdown = %q, want %q", got, want)
			}
			if got := reply.RenderInlineTimeline(card.Event{Type: "result", Segments: streamed}); got != want {
				t.Fatalf("streamed markdown = %q, want %q", got, want)
			}
			stream := &agentCardStream{ordered: streamed}
			if got := reply.RenderInlineTimeline(card.Event{Type: "result", Segments: stream.orderedSegmentsLocked()}); got != want {
				t.Fatalf("card projection = %q, want %q", got, want)
			}
		})
	}
}

func TestAppendMarkdownClaudeToolSummariesMatchReference(t *testing.T) {
	for _, test := range []struct {
		name  string
		input map[string]any
		want  string
	}{
		{name: "Grep", input: map[string]any{"pattern": "TODO", "path": "src"}, want: "TODO in src"},
		{name: "Glob", input: map[string]any{"pattern": "**/*.go"}, want: "**/*.go"},
		{name: "Task", input: map[string]any{"description": "inspect parser"}, want: "inspect parser"},
		{name: "Read", input: map[string]any{"file_path": "/repo/main.go"}, want: "/repo/main.go"},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := claudeToolUseMeta(map[string]any{"id": "tool", "name": test.name, "input": test.input})
			if meta.Summary != test.want {
				t.Fatalf("summary = %q, want %q", meta.Summary, test.want)
			}
		})
	}
}
