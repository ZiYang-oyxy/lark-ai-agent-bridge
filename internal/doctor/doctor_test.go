package doctor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lark-agent-bridge/internal/config"
)

func TestRunChecksRuntimeConfig(t *testing.T) {
	workDir := t.TempDir()
	auditPath := filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl")
	t.Setenv("E2E_CALLBACK_ADDR", ":18080")

	checks := Run(config.Config{
		DefaultAgent:       "claude",
		DefaultWorkDir:     workDir,
		CardUpdateEvery:    time.Second,
		InteractionTimeout: 2 * time.Second,
		CardMaxChars:       12000,
		AuditLogPath:       auditPath,
	})

	assertCheck(t, checks, "default_agent", true, "claude")
	assertCheck(t, checks, "default_workdir", true, workDir)
	assertCheck(t, checks, "audit_log", true, auditPath)
	assertCheck(t, checks, "E2E_CALLBACK_ADDR", true, ":18080")
	assertCheck(t, checks, "card_update_every", true, "1s")
	assertCheck(t, checks, "interaction_timeout", true, "2s")
	assertCheck(t, checks, "card_max_chars", true, "12000")

	if _, err := os.Stat(auditPath); err != nil {
		t.Fatalf("audit log was not created: %v", err)
	}
}

func TestRunReportsMissingWorkdir(t *testing.T) {
	baseDir := t.TempDir()
	missingDir := filepath.Join(baseDir, "missing")

	checks := Run(config.Config{
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
