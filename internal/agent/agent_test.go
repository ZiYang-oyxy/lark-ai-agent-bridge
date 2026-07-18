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
	want := "claude -p --output-format stream-json --verbose --include-partial-messages --dangerously-skip-permissions --effort low hello"
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
	want := "claude -p --output-format stream-json --verbose --include-partial-messages --dangerously-skip-permissions --effort low --resume sess-123 next"
	if got != want {
		t.Fatalf("one-shot command = %q, want %q", got, want)
	}
}

func TestBuildClaudeOneShotCommandUsesConfiguredModelAndEffort(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "hello", Model: "opus", Effort: "high"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--dangerously-skip-permissions", "--effort", "high", "--model", "opus", "hello"}
	if got := strings.Join(cmd, "\x00"); got != strings.Join(want, "\x00") {
		t.Fatalf("one-shot command = %#v, want %#v", cmd, want)
	}
}

func TestBuildClaudeOneShotCommandOmitsDefaultModelAndEffort(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "hello", Model: "default", Effort: "default"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	joined := strings.Join(cmd, " ")
	if strings.Contains(joined, "--model") || strings.Contains(joined, "--effort") {
		t.Fatalf("default preferences must omit model and effort flags: %#v", cmd)
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
