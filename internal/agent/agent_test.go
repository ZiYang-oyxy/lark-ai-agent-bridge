package agent

import (
	"reflect"
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
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "next", AgentSessionID: "sess-123"})
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
	_, err := BuildOneShotCommand(OneShotConfig{Kind: Kind("gemini"), Prompt: "hello"})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported agent error = %v, want unsupported", err)
	}
}

func TestBuildCodexOneShotCommandUsesOnlyProtocolArguments(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Codex, Bin: "/w/bin/cx3", Prompt: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/w/bin/cx3", "exec", "--json", "-"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("command = %#v, want %#v", cmd, want)
	}
	joined := strings.Join(cmd, " ")
	for _, forbidden := range []string{"--sandbox", "approval_policy", "--model", "--profile", "--ignore-rules", "--skip-git-repo-check"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unexpected %q in %#v", forbidden, cmd)
		}
	}
}

func TestParseKindAcceptsCodex(t *testing.T) {
	got, ok := ParseKind(" CODEX ")
	if !ok || got != Codex {
		t.Fatalf("ParseKind(CODEX) = %q/%v, want codex/true", got, ok)
	}
}

func TestBuildCodexOneShotCommandResumesAndAddsImages(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{
		Kind:           Codex,
		Prompt:         "next",
		AgentSessionID: "thread-1",
		Images:         []string{"/cache/a.png", "/cache/b.jpg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "resume", "--json", "--image", "/cache/a.png", "--image", "/cache/b.jpg", "thread-1", "-"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("command = %#v, want %#v", cmd, want)
	}
}

func TestBuildCodexOneShotCommandSeparatesFreshImagesFromPromptMarker(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Codex, Prompt: "inspect", Images: []string{"/cache/a.png"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "--json", "--image", "/cache/a.png", "--", "-"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("command = %#v, want %#v", cmd, want)
	}
}

func TestBuildClaudeOneShotCommandUsesCustomBin(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "hi", Bin: "/opt/wrap/cc"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	if cmd[0] != "/opt/wrap/cc" {
		t.Fatalf("custom bin not used: %#v", cmd)
	}
}

func TestAgentEnv(t *testing.T) {
	if env := AgentEnv(Claude, ""); env != nil {
		t.Fatalf("empty home must inject no env, got %#v", env)
	}
	if env := AgentEnv(Claude, "  "); env != nil {
		t.Fatalf("blank home must inject no env, got %#v", env)
	}
	if env := AgentEnv(Claude, "/data/home-a"); len(env) != 1 || env[0] != "CLAUDE_CONFIG_DIR=/data/home-a" {
		t.Fatalf("claude env = %#v", env)
	}
	if env := AgentEnv(Codex, "/data/codex-a"); len(env) != 1 || env[0] != "CODEX_HOME=/data/codex-a" {
		t.Fatalf("codex env = %#v", env)
	}
	if env := AgentEnv(Kind("gemini"), "/x"); env != nil {
		t.Fatalf("unknown kind must inject no env, got %#v", env)
	}
}
