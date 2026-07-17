package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const DefaultTmuxSession = "lark-agent-bridge"

type Config struct {
	TmuxSession        string
	DefaultAgent       string
	DefaultWorkDir     string
	CardUpdateEvery    time.Duration
	CardMaxChars       int
	InteractionTimeout time.Duration
	IdleReminderAfter  time.Duration
	IdleCheckEvery     time.Duration
	QueueQuietPolls    int
	AuditLogPath       string
}

func LoadFromEnv() Config {
	workDir := mustGetwd()
	cfg := Config{
		TmuxSession:        DefaultTmuxSession,
		DefaultAgent:       "claude",
		DefaultWorkDir:     workDir,
		CardUpdateEvery:    800 * time.Millisecond,
		CardMaxChars:       12000,
		InteractionTimeout: 120 * time.Second,
		IdleReminderAfter:  24 * time.Hour,
		IdleCheckEvery:     time.Hour,
		QueueQuietPolls:    0,
		AuditLogPath:       filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl"),
	}
	if v := os.Getenv("E2E_TMUX_SESSION"); v != "" {
		cfg.TmuxSession = v
	}
	if v := os.Getenv("E2E_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgent = v
	}
	if v := os.Getenv("E2E_DEFAULT_WORKDIR"); v != "" {
		cfg.DefaultWorkDir = v
		cfg.AuditLogPath = filepath.Join(v, ".lark-agent-bridge", "audit.jsonl")
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
	if v := os.Getenv("E2E_IDLE_REMINDER_AFTER_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.IdleReminderAfter = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("E2E_IDLE_CHECK_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.IdleCheckEvery = time.Duration(n) * time.Millisecond
		}
	}
	if v := os.Getenv("E2E_QUEUE_QUIET_POLLS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.QueueQuietPolls = n
		}
	}
	if v := os.Getenv("E2E_AUDIT_LOG"); v != "" {
		cfg.AuditLogPath = v
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
