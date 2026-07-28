package config

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DefaultAgent                string
	DefaultWorkDir              string
	ClaudeBin                   string
	CodexBin                    string
	CardUpdateEvery             time.Duration
	CardHeartbeatEvery          time.Duration
	CardMaxChars                int
	CardMinDeltaChars           int
	CardPreviewMaxChars         int
	InteractionTimeout          time.Duration
	AuditLogPath                string
	AgentRequestLogPath         string
	SessionStorePath            string
	WorkspaceStorePath          string
	PreferenceStorePath         string
	ReplyStorePath              string
	AgentsConfigPath            string
	AccessStorePath             string
	ActionGrantStorePath        string
	ParticipatedTopicsStorePath string
	TopicAliasStorePath         string
	ScheduleStorePath           string
	ScheduleSocketPath          string
	ScheduleDraftTTL            time.Duration
	ScheduleCatchUp             time.Duration
	ScheduleTimeout             time.Duration
	ScheduleRetention           time.Duration
	UpdateManifestURL           string
	UpdatePrereleaseManifestURL string
	DevModeStorePath            string
	Model                       string
	Effort                      string
	ReplyMode                   ReplyMode
	ConversationMode            ConversationMode
	TopicSeedMode               TopicSeedMode
	GroupMessageMode            GroupMessageMode
	AppendOverflowMode          AppendOverflowMode
	RespondToBots               bool
	AllowedModels               []string
	QueueMaxPending             int
	BatchMaxInputs              int
	BatchMaxTextRunes           int
	DedupTTL                    time.Duration
	DedupMaxEntries             int
	ShutdownGrace               time.Duration
	MediaCacheDir               string
	MediaMaxFileBytes           int64
	MediaMaxBatchBytes          int64
	MediaCacheMaxBytes          int64
	MediaRetention              time.Duration
	ClaudeContextUsageDir       string
	CodexContextUsageDir        string
}

func LoadFromEnv() Config {
	workDir := mustGetwd()
	cfg := Config{
		DefaultAgent:                "claude",
		DefaultWorkDir:              workDir,
		ClaudeBin:                   "claude",
		CodexBin:                    "codex",
		CardUpdateEvery:             800 * time.Millisecond,
		CardHeartbeatEvery:          5 * time.Second,
		CardMaxChars:                12000,
		CardMinDeltaChars:           30,
		CardPreviewMaxChars:         2000,
		InteractionTimeout:          120 * time.Second,
		AuditLogPath:                filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl"),
		AgentRequestLogPath:         filepath.Join(workDir, ".lark-agent-bridge", "agent-requests.jsonl"),
		SessionStorePath:            filepath.Join(workDir, ".lark-agent-bridge", "sessions.json"),
		WorkspaceStorePath:          filepath.Join(workDir, ".lark-agent-bridge", "workspaces.json"),
		PreferenceStorePath:         filepath.Join(workDir, ".lark-agent-bridge", "preferences.json"),
		ReplyStorePath:              filepath.Join(workDir, ".lark-agent-bridge", "replies.json"),
		AgentsConfigPath:            filepath.Join(workDir, ".lark-agent-bridge", "agents.json"),
		AccessStorePath:             filepath.Join(workDir, ".lark-agent-bridge", "access.json"),
		ActionGrantStorePath:        filepath.Join(workDir, ".lark-agent-bridge", "action-grants.json"),
		DevModeStorePath:            filepath.Join(workDir, ".lark-agent-bridge", "dev-mode.json"),
		ParticipatedTopicsStorePath: filepath.Join(workDir, ".lark-agent-bridge", "participated-topics.json"),
		TopicAliasStorePath:         filepath.Join(workDir, ".lark-agent-bridge", "topic-aliases.json"),
		ScheduleStorePath:           filepath.Join(workDir, ".lark-agent-bridge", "schedules.json"),
		ScheduleSocketPath:          DefaultScheduleSocketPath(workDir),
		ScheduleDraftTTL:            10 * time.Minute,
		ScheduleCatchUp:             5 * time.Minute,
		ScheduleTimeout:             30 * time.Minute,
		ScheduleRetention:           30 * 24 * time.Hour,
		Model:                       "default",
		Effort:                      "low",
		ReplyMode:                   ReplyModeCoder,
		ConversationMode:            ConversationModeChat,
		TopicSeedMode:               TopicSeedModeQuote,
		GroupMessageMode:            GroupMessageModeMentionOnly,
		AppendOverflowMode:          AppendOverflowModeTruncate,
		QueueMaxPending:             20,
		BatchMaxInputs:              10,
		BatchMaxTextRunes:           64 << 10,
		DedupTTL:                    24 * time.Hour,
		DedupMaxEntries:             10000,
		ShutdownGrace:               5 * time.Second,
		MediaMaxFileBytes:           25 << 20,
		MediaMaxBatchBytes:          100 << 20,
		MediaCacheMaxBytes:          500 << 20,
		MediaRetention:              72 * time.Hour,
	}
	cfg.AllowedModels = append([]string(nil), builtinModels...)
	cfg.MediaCacheDir = defaultMediaCacheDir(cfg.DefaultWorkDir)
	if v := os.Getenv("E2E_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgent = v
	}
	if v := os.Getenv("LAB_CLAUDE_BIN"); v != "" {
		cfg.ClaudeBin = v
	}
	if v := os.Getenv("E2E_CLAUDE_BIN"); v != "" {
		cfg.ClaudeBin = v
	}
	if v := os.Getenv("LAB_CODEX_BIN"); v != "" {
		cfg.CodexBin = v
	}
	if v := os.Getenv("E2E_CODEX_BIN"); v != "" {
		cfg.CodexBin = v
	}
	if v := os.Getenv("E2E_DEFAULT_WORKDIR"); v != "" {
		cfg.DefaultWorkDir = v
		cfg.AuditLogPath = filepath.Join(v, ".lark-agent-bridge", "audit.jsonl")
		cfg.AgentRequestLogPath = filepath.Join(v, ".lark-agent-bridge", "agent-requests.jsonl")
		cfg.SessionStorePath = filepath.Join(v, ".lark-agent-bridge", "sessions.json")
		cfg.WorkspaceStorePath = filepath.Join(v, ".lark-agent-bridge", "workspaces.json")
		cfg.PreferenceStorePath = filepath.Join(v, ".lark-agent-bridge", "preferences.json")
		cfg.ReplyStorePath = filepath.Join(v, ".lark-agent-bridge", "replies.json")
		cfg.AgentsConfigPath = filepath.Join(v, ".lark-agent-bridge", "agents.json")
		cfg.AccessStorePath = filepath.Join(v, ".lark-agent-bridge", "access.json")
		cfg.ActionGrantStorePath = filepath.Join(v, ".lark-agent-bridge", "action-grants.json")
		cfg.DevModeStorePath = filepath.Join(v, ".lark-agent-bridge", "dev-mode.json")
		cfg.ParticipatedTopicsStorePath = filepath.Join(v, ".lark-agent-bridge", "participated-topics.json")
		cfg.TopicAliasStorePath = filepath.Join(v, ".lark-agent-bridge", "topic-aliases.json")
		cfg.ScheduleStorePath = filepath.Join(v, ".lark-agent-bridge", "schedules.json")
		cfg.ScheduleSocketPath = DefaultScheduleSocketPath(v)
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
	if v := os.Getenv("E2E_CARD_HEARTBEAT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CardHeartbeatEvery = time.Duration(n) * time.Second
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
	if v := os.Getenv("E2E_AGENT_REQUEST_LOG"); v != "" {
		cfg.AgentRequestLogPath = v
	}
	if v := os.Getenv("E2E_SESSION_STORE"); v != "" {
		cfg.SessionStorePath = v
	}
	if v := os.Getenv("E2E_WORKSPACE_STORE"); v != "" {
		cfg.WorkspaceStorePath = v
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
	if v := os.Getenv("E2E_ACTION_GRANT_STORE"); v != "" {
		cfg.ActionGrantStorePath = v
	}
	if v := os.Getenv("E2E_PARTICIPATED_TOPICS_STORE"); v != "" {
		cfg.ParticipatedTopicsStorePath = v
	}
	if v := os.Getenv("E2E_TOPIC_ALIAS_STORE"); v != "" {
		cfg.TopicAliasStorePath = v
	}
	if v := os.Getenv("E2E_SCHEDULE_STORE"); v != "" {
		cfg.ScheduleStorePath = v
	}
	if v := os.Getenv("E2E_SCHEDULE_SOCKET"); v != "" {
		cfg.ScheduleSocketPath = v
	}
	cfg.UpdateManifestURL = strings.TrimSpace(os.Getenv("LAB_UPDATE_MANIFEST_URL"))
	cfg.UpdatePrereleaseManifestURL = strings.TrimSpace(os.Getenv("LAB_UPDATE_PRERELEASE_MANIFEST_URL"))
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
	if v := os.Getenv("E2E_TOPIC_SEED_MODE"); v != "" {
		cfg.TopicSeedMode = TopicSeedMode(strings.ToLower(strings.TrimSpace(v)))
	}
	if v := os.Getenv("E2E_GROUP_MESSAGE_MODE"); v != "" {
		cfg.GroupMessageMode = GroupMessageMode(strings.ToLower(strings.TrimSpace(v)))
	}
	if v := os.Getenv("E2E_APPEND_OVERFLOW_MODE"); v != "" {
		cfg.AppendOverflowMode = AppendOverflowMode(strings.ToLower(strings.TrimSpace(v)))
	}
	if v := os.Getenv("E2E_RESPOND_TO_BOTS"); strings.EqualFold(strings.TrimSpace(v), "true") {
		cfg.RespondToBots = true
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
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("E2E_SCHEDULE_DRAFT_TTL_MIN"))); err == nil && n > 0 {
		cfg.ScheduleDraftTTL = time.Duration(n) * time.Minute
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("E2E_SCHEDULE_CATCHUP_MIN"))); err == nil && n > 0 {
		cfg.ScheduleCatchUp = time.Duration(n) * time.Minute
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("E2E_SCHEDULE_TIMEOUT_MIN"))); err == nil && n > 0 {
		cfg.ScheduleTimeout = time.Duration(n) * time.Minute
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("E2E_SCHEDULE_RETENTION_DAYS"))); err == nil && n > 0 {
		cfg.ScheduleRetention = time.Duration(n) * 24 * time.Hour
	}
	cfg.ClaudeContextUsageDir = resolveContextUsageDir("E2E_CLAUDE_CONTEXT_USAGE_DIR", "LAB_CLAUDE_CONTEXT_USAGE_DIR", "CLAUDE_CONFIG_DIR")
	cfg.CodexContextUsageDir = resolveContextUsageDir("E2E_CODEX_CONTEXT_USAGE_DIR", "LAB_CODEX_CONTEXT_USAGE_DIR", "CODEX_HOME")
	return cfg
}

// resolveContextUsageDir picks an explicit context-usage directory from the E2E
// or LAB override, else derives <home>/context-usage from the agent home env
// var. Returns "" when nothing is configured (feature disabled for that kind).
func resolveContextUsageDir(e2eEnv, labEnv, homeEnv string) string {
	if v := strings.TrimSpace(os.Getenv(e2eEnv)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(labEnv)); v != "" {
		return v
	}
	if home := strings.TrimSpace(os.Getenv(homeEnv)); home != "" {
		return filepath.Join(home, "context-usage")
	}
	return ""
}

// LoadFromEnvStrict returns the runtime configuration and rejects an explicit
// invalid media setting. New startup paths should use this function so an
// operator cannot silently weaken media storage limits through the environment.
func LoadFromEnvStrict() (Config, error) {
	cfg := LoadFromEnv()
	var err error
	if cfg.UpdateManifestURL != "" {
		parsed, parseErr := url.Parse(cfg.UpdateManifestURL)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return Config{}, fmt.Errorf("LAB_UPDATE_MANIFEST_URL must be an absolute HTTPS URL without userinfo")
		}
	}
	if cfg.UpdatePrereleaseManifestURL != "" {
		parsed, parseErr := url.Parse(cfg.UpdatePrereleaseManifestURL)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return Config{}, fmt.Errorf("LAB_UPDATE_PRERELEASE_MANIFEST_URL must be an absolute HTTPS URL without userinfo")
		}
	}
	if raw := os.Getenv("E2E_ALLOWED_MODELS"); raw != "" {
		if _, err := modelCatalog(strings.Split(raw, ",")); err != nil {
			return Config{}, fmt.Errorf("parse E2E_ALLOWED_MODELS: %w", err)
		}
	}
	if err := ValidateRuntimePreference(RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: cfg.ReplyMode, ConversationMode: cfg.ConversationMode, TopicSeedMode: cfg.TopicSeedMode, GroupMessageMode: cfg.GroupMessageMode, AppendOverflowMode: cfg.AppendOverflowMode, RespondToBots: cfg.RespondToBots}, cfg.AllowedModels...); err != nil {
		return Config{}, fmt.Errorf("validate runtime preference defaults: %w", err)
	}
	if raw, ok := os.LookupEnv("E2E_RESPOND_TO_BOTS"); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true":
			cfg.RespondToBots = true
		case "false":
			cfg.RespondToBots = false
		default:
			return Config{}, fmt.Errorf("E2E_RESPOND_TO_BOTS must be true or false")
		}
	}
	if cfg.CardMinDeltaChars, err = explicitPositiveInt("E2E_CARD_MIN_DELTA_CHARS", cfg.CardMinDeltaChars); err != nil {
		return Config{}, err
	}
	if cfg.CardPreviewMaxChars, err = explicitPositiveInt("E2E_CARD_PREVIEW_MAX_CHARS", cfg.CardPreviewMaxChars); err != nil {
		return Config{}, err
	}
	if cfg.CardHeartbeatEvery, err = explicitPositiveDuration("E2E_CARD_HEARTBEAT_SEC", time.Second, cfg.CardHeartbeatEvery); err != nil {
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
	if cfg.ScheduleDraftTTL, err = explicitPositiveDuration("E2E_SCHEDULE_DRAFT_TTL_MIN", time.Minute, cfg.ScheduleDraftTTL); err != nil {
		return Config{}, err
	}
	if cfg.ScheduleCatchUp, err = explicitPositiveDuration("E2E_SCHEDULE_CATCHUP_MIN", time.Minute, cfg.ScheduleCatchUp); err != nil {
		return Config{}, err
	}
	if cfg.ScheduleTimeout, err = explicitPositiveDuration("E2E_SCHEDULE_TIMEOUT_MIN", time.Minute, cfg.ScheduleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ScheduleRetention, err = explicitPositiveDuration("E2E_SCHEDULE_RETENTION_DAYS", 24*time.Hour, cfg.ScheduleRetention); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func explicitPositiveDuration(name string, unit, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return time.Duration(n) * unit, nil
}

// DefaultScheduleSocketPath returns a short, per-workspace Unix socket path.
func DefaultScheduleSocketPath(workDir string) string {
	canonical, err := filepath.Abs(workDir)
	if err != nil {
		canonical = filepath.Clean(workDir)
	}
	sum := sha256.Sum256([]byte(canonical))
	return filepath.Join("/tmp", fmt.Sprintf("lark-agent-%d-%x", os.Getuid(), sum[:6]), "schedule.sock")
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
