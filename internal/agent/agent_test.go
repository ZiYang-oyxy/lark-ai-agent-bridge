package agent

import (
	"strings"
	"testing"
)

func TestBuildClaudeOneShotCommand(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "hello"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	got := strings.Join(cmd, " ")
	want := "claude -p --output-format stream-json --verbose --dangerously-skip-permissions --effort low hello"
	if got != want {
		t.Fatalf("one-shot command = %q, want %q", got, want)
	}
}

func TestBuildClaudeOneShotCommandResumesInternalSession(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "next", ClaudeSessionID: "sess-123"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	got := strings.Join(cmd, " ")
	want := "claude -p --output-format stream-json --verbose --dangerously-skip-permissions --effort low --resume sess-123 next"
	if got != want {
		t.Fatalf("one-shot command = %q, want %q", got, want)
	}
}

func TestBuildOneShotRejectsEmptyPrompt(t *testing.T) {
	_, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "   "})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty prompt error = %v, want empty prompt error", err)
	}
}

func TestBuildOneShotRejectsUnsupportedAgent(t *testing.T) {
	_, err := BuildOneShotCommand(OneShotConfig{Kind: Kind("codex"), Prompt: "hello"})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported agent error = %v, want unsupported", err)
	}
}
