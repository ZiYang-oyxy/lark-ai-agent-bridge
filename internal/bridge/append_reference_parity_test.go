package bridge

import (
	"reflect"
	"strings"
	"testing"

	"lark-agent-bridge/internal/card"
)

func TestAppendReferenceCardClaudeAndCodexParity(t *testing.T) {
	claudeInput := strings.Join([]string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"claude-tool","name":"Bash","input":{"command":"pwd"}}]}}`,
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

	claudeElements := appendReferenceComparableElements(card.BuildLarkCard(card.Event{
		Type: "result", ReferenceCardLayout: true, Segments: claudeResult.OrderedSegments,
	}))
	codexElements := appendReferenceComparableElements(card.BuildLarkCard(card.Event{
		Type: "result", ReferenceCardLayout: true, Segments: codexResult.OrderedSegments,
	}))
	if !reflect.DeepEqual(claudeElements, codexElements) {
		t.Fatalf("Claude/Codex reference cards diverged:\nClaude: %#v\nCodex:  %#v", claudeElements, codexElements)
	}
}

func appendReferenceComparableElements(payload map[string]any) []any {
	body, _ := payload["body"].(map[string]any)
	elements, _ := body["elements"].([]any)
	return elements
}
