package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

func TestRunChecksRuntimeConfig(t *testing.T) {
	workDir := t.TempDir()
	auditPath := filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl")
	storePath := filepath.Join(workDir, ".lark-agent-bridge", "sessions.json")
	t.Setenv("E2E_CALLBACK_ADDR", ":18080")

	checks := Run(config.Config{
		DefaultAgent:       "claude",
		DefaultWorkDir:     workDir,
		CardUpdateEvery:    time.Second,
		InteractionTimeout: 2 * time.Second,
		CardMaxChars:       12000,
		AuditLogPath:       auditPath,
		SessionStorePath:   storePath,
	})

	assertCheck(t, checks, "default_agent", true, "claude")
	assertCheck(t, checks, "default_workdir", true, workDir)
	assertCheck(t, checks, "audit_log", true, auditPath)
	assertCheck(t, checks, "session_store", true, storePath)
	assertCheck(t, checks, "E2E_CALLBACK_ADDR", true, ":18080")
	assertCheck(t, checks, "card_update_every", true, "1s")
	assertCheck(t, checks, "interaction_timeout", true, "2s")
	assertCheck(t, checks, "card_max_chars", true, "12000")

	if _, err := os.Stat(auditPath); err != nil {
		t.Fatalf("audit log was not created: %v", err)
	}
	info, err := os.Stat(filepath.Dir(storePath))
	if err != nil {
		t.Fatalf("session store parent was not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("session store parent permissions = %o, want 700", got)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("session store probe unexpectedly created store: %v", err)
	}
}

func TestRunReportsMissingWorkdir(t *testing.T) {
	baseDir := t.TempDir()
	missingDir := filepath.Join(baseDir, "missing")

	checks := Run(config.Config{
		DefaultAgent:     "claude",
		DefaultWorkDir:   missingDir,
		CardUpdateEvery:  time.Second,
		CardMaxChars:     12000,
		AuditLogPath:     filepath.Join(baseDir, "audit.jsonl"),
		SessionStorePath: filepath.Join(baseDir, "sessions.json"),
	})

	check := findCheck(t, checks, "default_workdir")
	if check.OK {
		t.Fatalf("default_workdir check OK = true, want false")
	}
}

func TestRunRejectsMalformedSessionStoreWithoutLeakingContents(t *testing.T) {
	workDir := t.TempDir()
	storePath := filepath.Join(workDir, "sessions.json")
	const secret = "DO_NOT_PRINT_SESSION_SECRET"
	if err := os.WriteFile(storePath, []byte(`{"schema_version":`+secret), 0o600); err != nil {
		t.Fatal(err)
	}

	check := findCheck(t, Run(doctorTestConfig(workDir, storePath)), "session_store")
	if check.OK {
		t.Fatalf("session_store check = %#v, want failure", check)
	}
	if strings.Contains(check.Detail, secret) || strings.Contains(Summary([]Check{check}), secret) {
		t.Fatalf("session store contents leaked: %#v", check)
	}
}

func TestRunRejectsUnsupportedSessionStoreSchema(t *testing.T) {
	workDir := t.TempDir()
	storePath := filepath.Join(workDir, "sessions.json")
	if err := os.WriteFile(storePath, []byte(`{"schema_version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}

	check := findCheck(t, Run(doctorTestConfig(workDir, storePath)), "session_store")
	if check.OK {
		t.Fatalf("session_store check = %#v, want failure", check)
	}
}

func TestRunRejectsInsecureSessionStoreFile(t *testing.T) {
	workDir := t.TempDir()
	storePath := filepath.Join(workDir, "sessions.json")
	if err := session.SaveSnapshot(storePath, session.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(storePath, 0o644); err != nil {
		t.Fatal(err)
	}

	check := findCheck(t, Run(doctorTestConfig(workDir, storePath)), "session_store")
	if check.OK {
		t.Fatalf("session_store check = %#v, want failure", check)
	}
}

func doctorTestConfig(workDir, storePath string) config.Config {
	return config.Config{
		DefaultAgent:       "claude",
		DefaultWorkDir:     workDir,
		CardUpdateEvery:    time.Second,
		InteractionTimeout: time.Second,
		CardMaxChars:       12000,
		AuditLogPath:       filepath.Join(workDir, "audit.jsonl"),
		SessionStorePath:   storePath,
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
