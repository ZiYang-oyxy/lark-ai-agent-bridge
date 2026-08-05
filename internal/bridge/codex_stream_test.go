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
	if got := toolUse.Tool; got == nil || got.ID != "cmd-1" || got.Name != "command_execution" || got.Phase != "use" || got.Summary != "pwd" {
		t.Fatalf("tool use metadata = %#v", got)
	}
	toolResult := findCodexSegment(t, result.Segments, card.SegmentTool, "/repo")
	if got := toolResult.Tool; got == nil || got.ID != "cmd-1" || got.Name != "command_execution" || got.Phase != "result" || got.Summary != "" || got.IsError {
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

func TestParseCodexStreamPreservesProcessMessagesBeforeFinalAnswer(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-process"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"msg-1","type":"agent_message","text":"I will inspect the parser first."}}`,
		`{"type":"item.started","item":{"id":"cmd-1","type":"command_execution","command":"rg reasoning internal/bridge"}}`,
		`{"type":"item.completed","item":{"id":"cmd-1","type":"command_execution","aggregated_output":"one match\n","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"msg-2","type":"agent_message","text":"The parser handles reasoning items."}}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	var updates []AgentStreamUpdate
	result, err := parseCodexStream(strings.NewReader(input), nil, func(update AgentStreamUpdate) {
		updates = append(updates, update)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AnswerSegments) != 1 || result.AnswerSegments[0] != "The parser handles reasoning items." {
		t.Fatalf("answers = %#v, want only the final agent message", result.AnswerSegments)
	}
	wantKinds := []card.SegmentKind{card.SegmentThought, card.SegmentTool, card.SegmentTool, card.SegmentText}
	if len(result.OrderedSegments) != len(wantKinds) {
		t.Fatalf("ordered segments = %#v", result.OrderedSegments)
	}
	for i, want := range wantKinds {
		if got := result.OrderedSegments[i].Kind; got != want {
			t.Fatalf("ordered segment %d kind = %s, want %s", i, got, want)
		}
	}
	if got := result.OrderedSegments[0].Text; got != "I will inspect the parser first." {
		t.Fatalf("process message = %q", got)
	}
	processVisible := false
	processMisclassifiedAsAnswer := false
	for _, update := range updates {
		if len(update.Segments) != 1 || update.Segments[0].Text != "I will inspect the parser first." {
			continue
		}
		processVisible = update.Segments[0].Kind == card.SegmentThought && update.Activity == streamActivityReasoning
		processMisclassifiedAsAnswer = update.AnswerSnapshot
	}
	if !processVisible || processMisclassifiedAsAnswer {
		t.Fatalf("process message updates = %#v", updates)
	}
}

func TestParseCodexStreamTreatsOnlyLastConsecutiveAgentMessageAsFinal(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"agent_message","text":"First process update"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Second and final answer"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	result, err := parseCodexStream(strings.NewReader(input), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AnswerSegments) != 1 || result.AnswerSegments[0] != "Second and final answer" {
		t.Fatalf("answers = %#v", result.AnswerSegments)
	}
	assertCodexSegment(t, result.Segments, card.SegmentThought, "First process update")
	assertCodexSegment(t, result.Segments, card.SegmentText, "Second and final answer")
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
	t.Run("EOF after final message remains pending", func(t *testing.T) {
		input := "{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"final candidate\"}}\n"
		result, err := parseCodexStream(strings.NewReader(input), nil, nil)
		if !isCodexTerminalMissingError(err) {
			t.Fatalf("error = %T %v, want missing terminal", err, err)
		}
		if result.codexPendingMessage != "final candidate" || len(result.AnswerSegments) != 0 {
			t.Fatalf("missing terminal result = %#v", result)
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
