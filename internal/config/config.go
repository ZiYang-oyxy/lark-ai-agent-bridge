package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	DefaultAgent       string
	DefaultWorkDir     string
	ClaudeBin          string
	CardUpdateEvery    time.Duration
	CardMaxChars       int
	InteractionTimeout time.Duration
	AuditLogPath       string
	SessionStorePath   string
	QueueMaxPending    int
	BatchMaxInputs     int
	BatchMaxTextRunes  int
	DedupTTL           time.Duration
	DedupMaxEntries    int
	ShutdownGrace      time.Duration
}

func LoadFromEnv() Config {
	workDir := mustGetwd()
	cfg := Config{
		DefaultAgent:       "claude",
		DefaultWorkDir:     workDir,
		ClaudeBin:          "claude",
		CardUpdateEvery:    800 * time.Millisecond,
		CardMaxChars:       12000,
		InteractionTimeout: 120 * time.Second,
		AuditLogPath:       filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl"),
		SessionStorePath:   filepath.Join(workDir, ".lark-agent-bridge", "sessions.json"),
		QueueMaxPending:    20,
		BatchMaxInputs:     10,
		BatchMaxTextRunes:  64 << 10,
		DedupTTL:           24 * time.Hour,
		DedupMaxEntries:    10000,
		ShutdownGrace:      5 * time.Second,
	}
	if v := os.Getenv("E2E_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgent = v
	}
	if v := os.Getenv("E2E_CLAUDE_BIN"); v != "" {
		cfg.ClaudeBin = v
	}
	if v := os.Getenv("E2E_DEFAULT_WORKDIR"); v != "" {
		cfg.DefaultWorkDir = v
		cfg.AuditLogPath = filepath.Join(v, ".lark-agent-bridge", "audit.jsonl")
		cfg.SessionStorePath = filepath.Join(v, ".lark-agent-bridge", "sessions.json")
	}
	if v := os.Getenv("E2E_CARD_MAX_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CardMaxChars = n
		}
	}
	if v := os.Getenv("E2E_CARD_UPDATE_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CardUpdateEvery = time.Duration(n) * time.Millisecond
		}
	}
	if v := os.Getenv("E2E_INTERACTION_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.InteractionTimeout = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("E2E_AUDIT_LOG"); v != "" {
		cfg.AuditLogPath = v
	}
	if v := os.Getenv("E2E_SESSION_STORE"); v != "" {
		cfg.SessionStorePath = v
	}
	if v := os.Getenv("E2E_QUEUE_MAX_PENDING"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.QueueMaxPending = n
		}
	}
	if v := os.Getenv("E2E_BATCH_MAX_INPUTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BatchMaxInputs = n
		}
	}
	if v := os.Getenv("E2E_BATCH_MAX_TEXT_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BatchMaxTextRunes = n
		}
	}
	if v := os.Getenv("E2E_DEDUP_TTL_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.DedupTTL = time.Duration(n) * time.Hour
		}
	}
	if v := os.Getenv("E2E_DEDUP_MAX_ENTRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.DedupMaxEntries = n
		}
	}
	if v := os.Getenv("E2E_SHUTDOWN_GRACE_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ShutdownGrace = time.Duration(n) * time.Second
		}
	}
	return cfg
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
