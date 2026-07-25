package bridge

import "testing"

func TestClaudeToolMetaCarriesStructuredLifecycle(t *testing.T) {
	input := map[string]any{"command": "git status", "description": "inspect"}
	use := claudeToolUseMeta(map[string]any{
		"id": "tool-1", "name": "Bash", "input": input,
	})
	if use.ID != "tool-1" || use.Name != "Bash" || use.Phase != "use" || use.Summary != "git status" || use.Input["command"] != "git status" {
		t.Fatalf("tool use metadata = %#v", use)
	}
	input["command"] = "mutated"
	if use.Input["command"] != "git status" {
		t.Fatalf("tool input aliases parser map: %#v", use.Input)
	}

	result := claudeToolResultMeta(map[string]any{
		"tool_use_id": "tool-1", "content": "clean", "is_error": true,
	})
	if result.ID != "tool-1" || result.Phase != "result" || !result.IsError || result.Output != "clean" {
		t.Fatalf("tool result metadata = %#v", result)
	}
}
