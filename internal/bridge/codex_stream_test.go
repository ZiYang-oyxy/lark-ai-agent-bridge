package bridge

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/card"
)

func TestParseCodexStreamTranslatesThreadContentToolsAndUsage(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"cmd-1","type":"command_execution","command":"pwd"}}`,
		`{"type":"item.completed","item":{"id":"cmd-1","type":"command_execution","output":"/repo\n","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"reason-1","type":"reasoning","text":"inspect first"}}`,
		`{"type":"item.completed","item":{"id":"msg-1","type":"agent_message","text":"hello from codex"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":34,"cached_input_tokens":5,"reasoning_output_tokens":7}}`,
	}, "\n")
	var updates []AgentStreamUpdate
	result, err := parseCodexStream(strings.NewReader(input), nil, func(update AgentStreamUpdate) {
		updates = append(updates, update)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentSessionID != "thread-1" || result.Tokens != 46 || result.ToolCallCount != 1 {
		t.Fatalf("result metadata = %#v", result)
	}
	if len(result.AnswerSegments) != 1 || result.AnswerSegments[0] != "hello from codex" {
		t.Fatalf("answers = %#v", result.AnswerSegments)
	}
	assertCodexSegment(t, result.Segments, card.SegmentText, "hello from codex")
	assertCodexSegment(t, result.Segments, card.SegmentThought, "inspect first")
	assertCodexSegment(t, result.Segments, card.SegmentTool, "pwd")
	assertCodexSegment(t, result.Segments, card.SegmentTool, "/repo")
	toolUse := findCodexSegment(t, result.Segments, card.SegmentTool, "pwd")
	if got := toolUse.Tool; got == nil || got.ID != "cmd-1" || got.Name != "Bash" || got.Phase != "use" || got.Summary != "pwd" {
		t.Fatalf("tool use metadata = %#v", got)
	}
	toolResult := findCodexSegment(t, result.Segments, card.SegmentTool, "/repo")
	if got := toolResult.Tool; got == nil || got.ID != "cmd-1" || got.Name != "Bash" || got.Phase != "result" || got.Summary != "" || got.IsError {
		t.Fatalf("tool result metadata = %#v", got)
	}
	if len(updates) == 0 || updates[0].AgentSessionID != "thread-1" {
		t.Fatalf("updates = %#v", updates)
	}
	foundAnswerSnapshot := false
	for _, update := range updates {
		if update.AnswerSnapshot && len(update.Segments) == 1 && update.Segments[0].Text == "hello from codex" {
			foundAnswerSnapshot = true
		}
	}
	if !foundAnswerSnapshot {
		t.Fatalf("missing answer snapshot: %#v", updates)
	}
}

func TestParseCodexStreamAllowsRetryErrorBeforeCompletion(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-retry"}`,
		`{"type":"error","message":"Reconnecting... 2/5"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"recovered"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	result, err := parseCodexStream(strings.NewReader(input), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertCodexSegment(t, result.Segments, card.SegmentText, "recovered")
}

func TestParseCodexStreamRejectsFailedTurnAndMissingTerminal(t *testing.T) {
	t.Run("turn failed", func(t *testing.T) {
		_, err := parseCodexStream(strings.NewReader(`{"type":"turn.failed","error":{"message":"command denied"}}`), nil, nil)
		if err == nil || !strings.Contains(err.Error(), "command denied") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("EOF after retry error", func(t *testing.T) {
		input := "{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}\n{\"type\":\"error\",\"message\":\"transport failed\"}\n"
		_, err := parseCodexStream(strings.NewReader(input), nil, nil)
		if err == nil || !strings.Contains(err.Error(), "codex stream ended before a terminal event: transport failed") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestParseCodexStreamTracksProtocolDrift(t *testing.T) {
	input := strings.Join([]string{
		`not-json`,
		`{"type":"future.event","value":1}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	result, err := parseCodexStream(strings.NewReader(input), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProtocolUnknown != 1 || result.ProtocolAnomalies != 1 {
		t.Fatalf("protocol drift = %d/%d", result.ProtocolUnknown, result.ProtocolAnomalies)
	}
}

func assertCodexSegment(t *testing.T, segments []card.Segment, kind card.SegmentKind, contains string) {
	t.Helper()
	_ = findCodexSegment(t, segments, kind, contains)
}

func findCodexSegment(t *testing.T, segments []card.Segment, kind card.SegmentKind, contains string) card.Segment {
	t.Helper()
	for _, segment := range segments {
		if segment.Kind == kind && strings.Contains(segment.Text, contains) {
			return segment
		}
	}
	t.Fatalf("missing %s segment containing %q in %#v", kind, contains, segments)
	return card.Segment{}
}
