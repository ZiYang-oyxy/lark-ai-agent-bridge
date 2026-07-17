package agent

import "testing"

func TestParseRuntimeMeta(t *testing.T) {
	meta := ParseRuntimeMeta("model: claude-sonnet-4\nTokens: 1,234")
	if meta.Model != "claude-sonnet-4" {
		t.Fatalf("model = %q", meta.Model)
	}
	if meta.Tokens != 1234 {
		t.Fatalf("tokens = %d", meta.Tokens)
	}
}
