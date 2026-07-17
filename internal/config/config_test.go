package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromEnvParsesRuntimeTuning(t *testing.T) {
	t.Setenv("E2E_CARD_UPDATE_MS", "250")
	t.Setenv("E2E_CARD_MAX_CHARS", "4096")
	t.Setenv("E2E_INTERACTION_TIMEOUT_SEC", "15")
	t.Setenv("E2E_DEFAULT_WORKDIR", "/tmp/lab-work")
	t.Setenv("E2E_AUDIT_LOG", "/tmp/custom-audit.jsonl")
	cfg := LoadFromEnv()
	if cfg.CardUpdateEvery != 250*time.Millisecond {
		t.Fatalf("card update interval = %s, want 250ms", cfg.CardUpdateEvery)
	}
	if cfg.CardMaxChars != 4096 {
		t.Fatalf("card max chars = %d, want 4096", cfg.CardMaxChars)
	}
	if cfg.InteractionTimeout != 15*time.Second {
		t.Fatalf("interaction timeout = %s, want 15s", cfg.InteractionTimeout)
	}
	if cfg.DefaultWorkDir != "/tmp/lab-work" {
		t.Fatalf("workdir = %q, want /tmp/lab-work", cfg.DefaultWorkDir)
	}
	if cfg.AuditLogPath != "/tmp/custom-audit.jsonl" {
		t.Fatalf("audit log path = %q, want custom path", cfg.AuditLogPath)
	}
}

func TestLoadFromEnvDefaultsAuditLogUnderWorkdir(t *testing.T) {
	t.Setenv("E2E_DEFAULT_WORKDIR", "/tmp/lab-work")
	t.Setenv("E2E_AUDIT_LOG", "")
	cfg := LoadFromEnv()
	want := filepath.Join("/tmp/lab-work", ".lark-agent-bridge", "audit.jsonl")
	if cfg.AuditLogPath != want {
		t.Fatalf("audit log path = %q, want %q", cfg.AuditLogPath, want)
	}
}

func TestLoadFromEnvDurableSchedulerDefaultsAndOverrides(t *testing.T) {
	t.Setenv("E2E_DEFAULT_WORKDIR", "/tmp/lab-work")
	t.Setenv("E2E_SESSION_STORE", "")
	t.Setenv("E2E_QUEUE_MAX_PENDING", "21")
	t.Setenv("E2E_BATCH_MAX_INPUTS", "11")
	t.Setenv("E2E_BATCH_MAX_TEXT_CHARS", "65537")
	t.Setenv("E2E_DEDUP_TTL_HOURS", "25")
	t.Setenv("E2E_DEDUP_MAX_ENTRIES", "10001")
	t.Setenv("E2E_SHUTDOWN_GRACE_SEC", "6")
	cfg := LoadFromEnv()
	if cfg.SessionStorePath != filepath.Join("/tmp/lab-work", ".lark-agent-bridge", "sessions.json") {
		t.Fatalf("store path = %q", cfg.SessionStorePath)
	}
	if cfg.QueueMaxPending != 21 || cfg.BatchMaxInputs != 11 || cfg.BatchMaxTextRunes != 65537 || cfg.DedupTTL != 25*time.Hour || cfg.DedupMaxEntries != 10001 || cfg.ShutdownGrace != 6*time.Second {
		t.Fatalf("durable config = %#v", cfg)
	}
}
