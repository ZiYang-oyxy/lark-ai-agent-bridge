package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DefaultAgent        string
	DefaultWorkDir      string
	ClaudeBin           string
	CardUpdateEvery     time.Duration
	CardMaxChars        int
	CardMinDeltaChars   int
	CardPreviewMaxChars int
	InteractionTimeout  time.Duration
	AuditLogPath        string
	SessionStorePath    string
	PreferenceStorePath string
	ReplyStorePath      string
	AgentsConfigPath    string
	AccessStorePath     string
	Model               string
	Effort              string
	ReplyMode           ReplyMode
	ConversationMode    ConversationMode
	AllowedModels       []string
	QueueMaxPending     int
	BatchMaxInputs      int
	BatchMaxTextRunes   int
	DedupTTL            time.Duration
	DedupMaxEntries     int
	ShutdownGrace       time.Duration
	MediaCacheDir       string
	MediaMaxFileBytes   int64
	MediaMaxBatchBytes  int64
	MediaCacheMaxBytes  int64
	MediaRetention      time.Duration
}

func LoadFromEnv() Config {
	workDir := mustGetwd()
	cfg := Config{
		DefaultAgent:        "claude",
		DefaultWorkDir:      workDir,
		ClaudeBin:           "claude",
		CardUpdateEvery:     800 * time.Millisecond,
		CardMaxChars:        12000,
		CardMinDeltaChars:   30,
		CardPreviewMaxChars: 2000,
		InteractionTimeout:  120 * time.Second,
		AuditLogPath:        filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl"),
		SessionStorePath:    filepath.Join(workDir, ".lark-agent-bridge", "sessions.json"),
		PreferenceStorePath: filepath.Join(workDir, ".lark-agent-bridge", "preferences.json"),
		ReplyStorePath:      filepath.Join(workDir, ".lark-agent-bridge", "replies.json"),
		AgentsConfigPath:    filepath.Join(workDir, ".lark-agent-bridge", "agents.json"),
		AccessStorePath:     filepath.Join(workDir, ".lark-agent-bridge", "access.json"),
		Model:               "default",
		Effort:              "low",
		ReplyMode:           ReplyModeAppend,
		ConversationMode:    ConversationModeChat,
		QueueMaxPending:     20,
		BatchMaxInputs:      10,
		BatchMaxTextRunes:   64 << 10,
		DedupTTL:            24 * time.Hour,
		DedupMaxEntries:     10000,
		ShutdownGrace:       5 * time.Second,
		MediaMaxFileBytes:   25 << 20,
		MediaMaxBatchBytes:  100 << 20,
		MediaCacheMaxBytes:  500 << 20,
		MediaRetention:      72 * time.Hour,
	}
	cfg.AllowedModels = append([]string(nil), builtinModels...)
	cfg.MediaCacheDir = defaultMediaCacheDir(cfg.DefaultWorkDir)
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
		cfg.PreferenceStorePath = filepath.Join(v, ".lark-agent-bridge", "preferences.json")
		cfg.ReplyStorePath = filepath.Join(v, ".lark-agent-bridge", "replies.json")
		cfg.AgentsConfigPath = filepath.Join(v, ".lark-agent-bridge", "agents.json")
		cfg.AccessStorePath = filepath.Join(v, ".lark-agent-bridge", "access.json")
		cfg.MediaCacheDir = defaultMediaCacheDir(v)
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
	if v := os.Getenv("E2E_CARD_MIN_DELTA_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CardMinDeltaChars = n
		}
	}
	if v := os.Getenv("E2E_CARD_PREVIEW_MAX_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CardPreviewMaxChars = n
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
	if v := os.Getenv("E2E_PREFERENCE_STORE"); v != "" {
		cfg.PreferenceStorePath = v
	}
	if v := os.Getenv("E2E_REPLY_STORE"); v != "" {
		cfg.ReplyStorePath = v
	}
	if v := os.Getenv("E2E_ACCESS_STORE"); v != "" {
		cfg.AccessStorePath = v
	}
	if v := os.Getenv("E2E_MODEL"); v != "" {
		cfg.Model = strings.TrimSpace(v)
	}
	if v := os.Getenv("E2E_EFFORT"); v != "" {
		cfg.Effort = strings.ToLower(strings.TrimSpace(v))
	}
	if v := os.Getenv("E2E_REPLY_MODE"); v != "" {
		cfg.ReplyMode = ReplyMode(strings.ToLower(strings.TrimSpace(v)))
	}
	if v := os.Getenv("E2E_CONVERSATION_MODE"); v != "" {
		cfg.ConversationMode = ConversationMode(strings.ToLower(strings.TrimSpace(v)))
	}
	if v := os.Getenv("E2E_ALLOWED_MODELS"); v != "" {
		additions := strings.Split(v, ",")
		if models, err := modelCatalog(additions); err == nil {
			cfg.AllowedModels = models
		}
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

// LoadFromEnvStrict returns the runtime configuration and rejects an explicit
// invalid media setting. New startup paths should use this function so an
// operator cannot silently weaken media storage limits through the environment.
func LoadFromEnvStrict() (Config, error) {
	cfg := LoadFromEnv()
	var err error
	if raw := os.Getenv("E2E_ALLOWED_MODELS"); raw != "" {
		if _, err := modelCatalog(strings.Split(raw, ",")); err != nil {
			return Config{}, fmt.Errorf("parse E2E_ALLOWED_MODELS: %w", err)
		}
	}
	if err := ValidateRuntimePreference(RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: cfg.ReplyMode, ConversationMode: cfg.ConversationMode}, cfg.AllowedModels...); err != nil {
		return Config{}, fmt.Errorf("validate runtime preference defaults: %w", err)
	}
	if cfg.CardMinDeltaChars, err = explicitPositiveInt("E2E_CARD_MIN_DELTA_CHARS", cfg.CardMinDeltaChars); err != nil {
		return Config{}, err
	}
	if cfg.CardPreviewMaxChars, err = explicitPositiveInt("E2E_CARD_PREVIEW_MAX_CHARS", cfg.CardPreviewMaxChars); err != nil {
		return Config{}, err
	}
	if cfg.MediaCacheDir, err = explicitMediaDir("E2E_MEDIA_CACHE_DIR", cfg.MediaCacheDir); err != nil {
		return Config{}, err
	}
	if cfg.MediaCacheDir, err = filepath.Abs(cfg.MediaCacheDir); err != nil {
		return Config{}, fmt.Errorf("resolve E2E_MEDIA_CACHE_DIR as an absolute path: %w", err)
	}
	cfg.MediaCacheDir = filepath.Clean(cfg.MediaCacheDir)
	if cfg.MediaMaxFileBytes, err = explicitMediaMiB("E2E_MEDIA_MAX_FILE_MB", cfg.MediaMaxFileBytes); err != nil {
		return Config{}, err
	}
	if cfg.MediaMaxBatchBytes, err = explicitMediaMiB("E2E_MEDIA_MAX_BATCH_MB", cfg.MediaMaxBatchBytes); err != nil {
		return Config{}, err
	}
	if cfg.MediaCacheMaxBytes, err = explicitMediaMiB("E2E_MEDIA_CACHE_MAX_MB", cfg.MediaCacheMaxBytes); err != nil {
		return Config{}, err
	}
	if cfg.MediaRetention, err = explicitMediaHours("E2E_MEDIA_RETENTION_HOURS", cfg.MediaRetention); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func explicitPositiveInt(name string, fallback int) (int, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

func defaultMediaCacheDir(workDir string) string {
	return filepath.Join(workDir, ".lark-agent-bridge", "media")
}

func explicitMediaDir(name, fallback string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must be a non-empty path", name)
	}
	return value, nil
}

func explicitMediaMiB(name string, fallback int64) (int64, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || n <= 0 || n > int64(^uint64(0)>>1)/(1<<20) {
		return 0, fmt.Errorf("%s must be a positive integer MiB", name)
	}
	return n << 20, nil
}

func explicitMediaHours(name string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || n <= 0 || n > int64((time.Duration(1<<63-1))/time.Hour) {
		return 0, fmt.Errorf("%s must be a positive integer hour count", name)
	}
	return time.Duration(n) * time.Hour, nil
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
