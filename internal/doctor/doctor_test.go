package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/config"
)

func TestRunChecksRuntimeConfig(t *testing.T) {
	workDir := t.TempDir()
	prepareCodexConfig(t, workDir, "[features]\nhooks = true\n")
	auditPath := filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl")
	t.Setenv("E2E_CALLBACK_ADDR", ":18080")

	checks := Run(config.Config{
		TmuxSession:        config.DefaultTmuxSession,
		DefaultAgent:       "claude",
		DefaultWorkDir:     workDir,
		CardUpdateEvery:    time.Second,
		InteractionTimeout: 2 * time.Second,
		IdleReminderAfter:  24 * time.Hour,
		IdleCheckEvery:     time.Hour,
		CardMaxChars:       12000,
		QueueQuietPolls:    2,
		AuditLogPath:       auditPath,
	})

	assertCheck(t, checks, "tmux_session", true, config.DefaultTmuxSession)
	assertCheck(t, checks, "default_agent", true, "claude")
	assertCheck(t, checks, "default_workdir", true, workDir)
	assertCheck(t, checks, "audit_log", true, auditPath)
	assertCheck(t, checks, "E2E_CALLBACK_ADDR", true, ":18080")
	assertCheck(t, checks, "card_update_every", true, "1s")
	assertCheck(t, checks, "interaction_timeout", true, "2s")
	assertCheck(t, checks, "idle_reminder_after", true, "24h0m0s")
	assertCheck(t, checks, "idle_check_every", true, "1h0m0s")
	assertCheck(t, checks, "card_max_chars", true, "12000")
	assertCheck(t, checks, "queue_quiet_polls", true, "2")
	assertCheckContains(t, checks, "codex_config_hooks", true, "no deprecated codex_hooks")

	if _, err := os.Stat(auditPath); err != nil {
		t.Fatalf("audit log was not created: %v", err)
	}
}

func TestRunReportsDeprecatedCodexHooksConfig(t *testing.T) {
	home := t.TempDir()
	prepareCodexConfig(t, home, "[features]\nhooks = true\ncodex_hooks = true\n")

	checks := Run(config.Config{
		TmuxSession:     config.DefaultTmuxSession,
		DefaultAgent:    "claude",
		DefaultWorkDir:  home,
		CardUpdateEvery: time.Second,
		CardMaxChars:    12000,
		AuditLogPath:    filepath.Join(home, "audit.jsonl"),
	})

	assertCheckContains(t, checks, "codex_config_hooks", true, "deprecated codex_hooks")
	assertCheckWarning(t, checks, "codex_config_hooks", true)
}

func TestRunAcceptsCleanCodexHooksConfig(t *testing.T) {
	home := t.TempDir()
	prepareCodexConfig(t, home, "[features]\nhooks = true\n")

	checks := Run(config.Config{
		TmuxSession:     config.DefaultTmuxSession,
		DefaultAgent:    "claude",
		DefaultWorkDir:  home,
		CardUpdateEvery: time.Second,
		CardMaxChars:    12000,
		AuditLogPath:    filepath.Join(home, "audit.jsonl"),
	})

	assertCheckContains(t, checks, "codex_config_hooks", true, "no deprecated codex_hooks")
}

func TestRunReportsMissingWorkdir(t *testing.T) {
	baseDir := t.TempDir()
	prepareCodexConfig(t, baseDir, "[features]\nhooks = true\n")
	missingDir := filepath.Join(baseDir, "missing")

	checks := Run(config.Config{
		TmuxSession:     config.DefaultTmuxSession,
		DefaultAgent:    "claude",
		DefaultWorkDir:  missingDir,
		CardUpdateEvery: time.Second,
		CardMaxChars:    12000,
		AuditLogPath:    filepath.Join(baseDir, "audit.jsonl"),
	})

	check := findCheck(t, checks, "default_workdir")
	if check.OK {
		t.Fatalf("default_workdir check OK = true, want false")
	}
}

func TestSummaryFormatsChecks(t *testing.T) {
	got := Summary([]Check{
		{Name: "a", OK: true, Detail: "ready"},
		{Name: "c", OK: true, Warning: true, Detail: "warning"},
		{Name: "b", OK: false, Detail: "missing"},
	})
	want := "ok a: ready\nwarn c: warning\nfail b: missing"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func assertCheck(t *testing.T, checks []Check, name string, ok bool, detail string) {
	t.Helper()
	check := findCheck(t, checks, name)
	if check.OK != ok || check.Detail != detail {
		t.Fatalf("%s check = (%t, %q), want (%t, %q)", name, check.OK, check.Detail, ok, detail)
	}
}

func prepareCodexConfig(t *testing.T, home, contents string) {
	t.Helper()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertCheckContains(t *testing.T, checks []Check, name string, ok bool, detailPart string) {
	t.Helper()
	check := findCheck(t, checks, name)
	if check.OK != ok || !strings.Contains(check.Detail, detailPart) {
		t.Fatalf("%s check = (%t, %q), want ok=%t and detail containing %q", name, check.OK, check.Detail, ok, detailPart)
	}
}

func assertCheckWarning(t *testing.T, checks []Check, name string, warning bool) {
	t.Helper()
	check := findCheck(t, checks, name)
	if check.Warning != warning {
		t.Fatalf("%s warning = %t, want %t", name, check.Warning, warning)
	}
}

func findCheck(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check %q not found in %#v", name, checks)
	return Check{}
}
