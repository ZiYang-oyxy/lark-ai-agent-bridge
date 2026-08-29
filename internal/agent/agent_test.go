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
	want := "claude -p --output-format stream-json --verbose --include-partial-messages --dangerously-skip-permissions --effort low"
	if got != want {
		t.Fatalf("one-shot command = %q, want %q", got, want)
	}
}

func TestBuildClaudeOneShotCommandForksFromSourceWhenNoOwnSession(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "topic first", ForkFromAgentSessionID: "src-uuid"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	got := strings.Join(cmd, " ")
	want := "claude -p --output-format stream-json --verbose --include-partial-messages --dangerously-skip-permissions --effort low --resume src-uuid --fork-session"
	if got != want {
		t.Fatalf("fork command = %q, want %q", got, want)
	}
}

func TestBuildClaudeOneShotCommandPrefersOwnSessionOverFork(t *testing.T) {
	// After the seed run the forked session owns its own AgentSessionID and
	// subsequent runs must resume it directly instead of re-forking the source.
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "topic later", AgentSessionID: "own", ForkFromAgentSessionID: "src-uuid"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	got := strings.Join(cmd, " ")
	want := "claude -p --output-format stream-json --verbose --include-partial-messages --dangerously-skip-permissions --effort low --resume own"
	if got != want {
		t.Fatalf("resume command = %q, want %q", got, want)
	}
}

func TestBuildCodexOneShotCommandIgnoresForkHint(t *testing.T) {
	// Codex CLI has no headless fork; a stray ForkFromAgentSessionID must be
	// silently discarded so a Codex session simply starts fresh.
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Codex, Prompt: "hello", ForkFromAgentSessionID: "src-uuid"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	got := strings.Join(cmd, " ")
	if strings.Contains(got, "fork") || strings.Contains(got, "src-uuid") {
		t.Fatalf("codex command leaked fork hint: %q", got)
	}
}

func TestBuildClaudeOneShotCommandResumesInternalSession(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "next", AgentSessionID: "sess-123"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	got := strings.Join(cmd, " ")
	want := "claude -p --output-format stream-json --verbose --include-partial-messages --dangerously-skip-permissions --effort low --resume sess-123"
	if got != want {
		t.Fatalf("one-shot command = %q, want %q", got, want)
	}
}

func TestClaudeOneShotAppendsBridgeSystemPromptBeforeResume(t *testing.T) {
	got, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "hello", AgentSessionID: "sess", ClaudeSystemPrompt: "bridge\nrules\n"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--dangerously-skip-permissions", "--effort", "low", "--append-system-prompt", "bridge\nrules\n", "--resume", "sess"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeOneShotCommandUsesConfiguredModelAndEffort(t *testing.T) {
	cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "hello", Model: "opus", Effort: "high"})
	if err != nil {
		t.Fatalf("one-shot command error: %v", err)
	}
	want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--dangerously-skip-permissions", "--effort", "high", "--model", "opus"}
	if got := strings.Join(cmd, "\x00"); got != strings.Join(want, "\x00") {
		t.Fatalf("one-shot command = %#v, want %#v", cmd, want)
	}
}

func TestBuildClaudeOneShotCommandSupportsExtendedEfforts(t *testing.T) {
	for _, effort := range []string{"xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Claude, Prompt: "hello", Effort: effort})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--dangerously-skip-permissions", "--effort", effort}
			if !reflect.DeepEqual(cmd, want) {
				t.Fatalf("one-shot command = %#v, want %#v", cmd, want)
			}
		})
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

func TestBuildCodexOneShotCommandUsesConfiguredEffort(t *testing.T) {
	tests := []struct {
		name string
		cfg  OneShotConfig
		want []string
	}{
		{
			name: "fresh",
			cfg:  OneShotConfig{Kind: Codex, Prompt: "inspect", Effort: "high"},
			want: []string{"codex", "exec", "-c", `model_reasoning_effort="high"`, "--json", "-"},
		},
		{
			name: "resume",
			cfg:  OneShotConfig{Kind: Codex, Prompt: "next", AgentSessionID: "thread-1", Effort: "medium"},
			want: []string{"codex", "exec", "-c", `model_reasoning_effort="medium"`, "resume", "--json", "thread-1", "-"},
		},
		{
			name: "xhigh",
			cfg:  OneShotConfig{Kind: Codex, Prompt: "inspect", Effort: "xhigh"},
			want: []string{"codex", "exec", "-c", `model_reasoning_effort="xhigh"`, "--json", "-"},
		},
		{
			name: "max",
			cfg:  OneShotConfig{Kind: Codex, Prompt: "inspect", Effort: "max"},
			want: []string{"codex", "exec", "-c", `model_reasoning_effort="max"`, "--json", "-"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildOneShotCommand(tt.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("command = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildCodexOneShotCommandOmitsDefaultEffort(t *testing.T) {
	for _, effort := range []string{"", "default", " DEFAULT "} {
		cmd, err := BuildOneShotCommand(OneShotConfig{Kind: Codex, Prompt: "inspect", Effort: effort})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(cmd, "\x00"), "model_reasoning_effort") {
			t.Fatalf("effort %q must inherit Codex config: %#v", effort, cmd)
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

func TestCodexResumeUsesOneDeveloperInstructionsOverrideBeforeResume(t *testing.T) {
	got, err := BuildOneShotCommand(OneShotConfig{Kind: Codex, Prompt: "next", AgentSessionID: "thread", DeveloperInstructions: "bridge\n\"quoted\""})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "-c", `developer_instructions="bridge\n\"quoted\""`, "resume", "--json", "thread", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if strings.Contains(strings.Join(got, "\x00"), "next") {
		t.Fatalf("user prompt leaked into argv: %#v", got)
	}
}

func TestCodexFreshUsesOneDeveloperInstructionsOverrideBeforeProtocolArgs(t *testing.T) {
	got, err := BuildOneShotCommand(OneShotConfig{Kind: Codex, Prompt: "hello", DeveloperInstructions: "bridge"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "-c", `developer_instructions="bridge"`, "--json", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if strings.Contains(strings.Join(got, "\x00"), "hello") {
		t.Fatalf("user prompt leaked into argv: %#v", got)
	}
}

// TestBuildClaudeOneShotKeepsPromptOffArgv locks in the E2BIG fix: the user
// prompt must never appear in argv (a single argv element is capped by the
// kernel MAX_ARG_STRLEN ~128KB), and Claude must be flagged to read it on
// stdin — mirroring the Codex contract above.
func TestBuildClaudeOneShotKeepsPromptOffArgv(t *testing.T) {
	if !PromptOnStdin(Claude) {
		t.Fatal("Claude prompt must be delivered on stdin, not argv")
	}
	marker := strings.Repeat("PROMPT_MARKER_", 20000) // ~280KB, well past MAX_ARG_STRLEN
	for _, cfg := range []OneShotConfig{
		{Kind: Claude, Prompt: marker},
		{Kind: Claude, Prompt: marker, AgentSessionID: "sess-123"},
		{Kind: Claude, Prompt: marker, ForkFromAgentSessionID: "src-uuid"},
	} {
		got, err := BuildOneShotCommand(cfg)
		if err != nil {
			t.Fatalf("config error: %v", err)
		}
		if strings.Contains(strings.Join(got, "\x00"), "PROMPT_MARKER_") {
			t.Fatalf("user prompt leaked into argv: len=%d", len(strings.Join(got, "")))
		}
	}
}

func TestBuildOneShotRejectsNULInBridgeInstructions(t *testing.T) {
	for _, cfg := range []OneShotConfig{
		{Kind: Claude, Prompt: "hello", ClaudeSystemPrompt: "bridge\x00rules"},
		{Kind: Codex, Prompt: "hello", DeveloperInstructions: "bridge\x00rules"},
	} {
		if _, err := BuildOneShotCommand(cfg); err == nil || !strings.Contains(err.Error(), "NUL") {
			t.Fatalf("config %#v error = %v", cfg, err)
		}
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

func TestContextCacheEnv(t *testing.T) {
	if env := ContextCacheEnv(Claude, ""); env != nil {
		t.Fatalf("empty cache dir must inject no env, got %#v", env)
	}
	if got := ContextCacheEnv(Claude, "/ctx/claude"); !reflect.DeepEqual(got, []string{"CLAUDE_CONTEXT_CACHE_DIR=/ctx/claude"}) {
		t.Fatalf("Claude context cache env = %#v", got)
	}
	if got := ContextCacheEnv(Codex, "/ctx/codex"); !reflect.DeepEqual(got, []string{"CODEX_CONTEXT_CACHE_DIR=/ctx/codex"}) {
		t.Fatalf("Codex context cache env = %#v", got)
	}
	if env := ContextCacheEnv(Kind("gemini"), "/ctx/unknown"); env != nil {
		t.Fatalf("unknown kind must inject no context cache env, got %#v", env)
	}
}
