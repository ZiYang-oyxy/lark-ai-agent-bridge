package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromEnvParsesRuntimeTuning(t *testing.T) {
	t.Setenv("E2E_CARD_UPDATE_MS", "250")
	t.Setenv("E2E_CARD_MAX_CHARS", "4096")
	t.Setenv("E2E_CARD_MIN_DELTA_CHARS", "31")
	t.Setenv("E2E_CARD_PREVIEW_MAX_CHARS", "2001")
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
	if cfg.CardMinDeltaChars != 31 || cfg.CardPreviewMaxChars != 2001 {
		t.Fatalf("preview tuning = min delta %d max %d", cfg.CardMinDeltaChars, cfg.CardPreviewMaxChars)
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

func TestLoadFromEnvPreviewDefaults(t *testing.T) {
	cfg := LoadFromEnv()
	if cfg.CardMinDeltaChars != 30 || cfg.CardPreviewMaxChars != 2000 {
		t.Fatalf("preview defaults = min delta %d max %d", cfg.CardMinDeltaChars, cfg.CardPreviewMaxChars)
	}
}

func TestLoadFromEnvStrictRejectsInvalidPreviewTuning(t *testing.T) {
	for _, name := range []string{"E2E_CARD_MIN_DELTA_CHARS", "E2E_CARD_PREVIEW_MAX_CHARS"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "0")
			if _, err := LoadFromEnvStrict(); err == nil {
				t.Fatalf("LoadFromEnvStrict() error = nil for %s=0", name)
			}
		})
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

func TestLoadFromEnvRuntimePreferenceDefaultsAndOverrides(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "work")
	t.Setenv("E2E_DEFAULT_WORKDIR", workDir)
	t.Setenv("E2E_MODEL", "opus")
	t.Setenv("E2E_EFFORT", "high")
	t.Setenv("E2E_ALLOWED_MODELS", "claude-custom-1, sonnet,claude-custom-2")
	cfg := LoadFromEnv()
	if cfg.PreferenceStorePath != filepath.Join(workDir, ".lark-agent-bridge", "preferences.json") {
		t.Fatalf("preference store path = %q", cfg.PreferenceStorePath)
	}
	if cfg.Model != "opus" || cfg.Effort != "high" {
		t.Fatalf("runtime defaults = model %q effort %q", cfg.Model, cfg.Effort)
	}
	wantModels := []string{"default", "sonnet", "opus", "haiku", "claude-custom-1", "claude-custom-2"}
	if len(cfg.AllowedModels) != len(wantModels) {
		t.Fatalf("allowed models = %#v, want %#v", cfg.AllowedModels, wantModels)
	}
	for i := range wantModels {
		if cfg.AllowedModels[i] != wantModels[i] {
			t.Fatalf("allowed models = %#v, want %#v", cfg.AllowedModels, wantModels)
		}
	}

	customPath := filepath.Join(t.TempDir(), "custom-preferences.json")
	t.Setenv("E2E_PREFERENCE_STORE", customPath)
	if got := LoadFromEnv().PreferenceStorePath; got != customPath {
		t.Fatalf("preference store override = %q, want %q", got, customPath)
	}
}

func TestLoadFromEnvReplyDefaultsAndOverrides(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "work")
	t.Setenv("E2E_DEFAULT_WORKDIR", workDir)
	cfg := LoadFromEnv()
	if cfg.ReplyMode != ReplyModeAppend {
		t.Fatalf("default reply mode = %q, want %q", cfg.ReplyMode, ReplyModeAppend)
	}
	if cfg.ReplyStorePath != filepath.Join(workDir, ".lark-agent-bridge", "replies.json") {
		t.Fatalf("reply store path = %q", cfg.ReplyStorePath)
	}
	customPath := filepath.Join(t.TempDir(), "custom-replies.json")
	t.Setenv("E2E_REPLY_MODE", string(ReplyModeLatestCard))
	t.Setenv("E2E_REPLY_STORE", customPath)
	cfg = LoadFromEnv()
	if cfg.ReplyMode != ReplyModeLatestCard || cfg.ReplyStorePath != customPath {
		t.Fatalf("reply config = mode %q store %q", cfg.ReplyMode, cfg.ReplyStorePath)
	}
}

func TestLoadFromEnvStrictRejectsInvalidReplyMode(t *testing.T) {
	t.Setenv("E2E_REPLY_MODE", "replace-everything")
	if _, err := LoadFromEnvStrict(); err == nil {
		t.Fatal("LoadFromEnvStrict() error = nil for invalid reply mode")
	}
}

func TestLoadFromEnvStrictRejectsInvalidRuntimePreferenceDefaults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "unknown_model", env: "E2E_MODEL", value: "unknown"},
		{name: "invalid_effort", env: "E2E_EFFORT", value: "extreme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			if _, err := LoadFromEnvStrict(); err == nil {
				t.Fatalf("LoadFromEnvStrict() error = nil for %s=%q", tc.env, tc.value)
			}
		})
	}
}

func TestLoadFromEnvStrictMediaDefaultsFollowWorkdir(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "work")
	t.Setenv("E2E_DEFAULT_WORKDIR", workDir)

	cfg, err := LoadFromEnvStrict()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MediaCacheDir != filepath.Join(workDir, ".lark-agent-bridge", "media") {
		t.Fatalf("media cache dir = %q, want default under workdir", cfg.MediaCacheDir)
	}
	if cfg.MediaMaxFileBytes != 25<<20 || cfg.MediaMaxBatchBytes != 100<<20 || cfg.MediaCacheMaxBytes != 500<<20 || cfg.MediaRetention != 72*time.Hour {
		t.Fatalf("media configuration = %#v", cfg)
	}
}

func TestLoadFromEnvStrictParsesMediaOverrides(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("E2E_MEDIA_CACHE_DIR", cacheDir)
	t.Setenv("E2E_MEDIA_MAX_FILE_MB", "26")
	t.Setenv("E2E_MEDIA_MAX_BATCH_MB", "101")
	t.Setenv("E2E_MEDIA_CACHE_MAX_MB", "501")
	t.Setenv("E2E_MEDIA_RETENTION_HOURS", "73")

	cfg, err := LoadFromEnvStrict()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MediaCacheDir != cacheDir {
		t.Fatalf("media cache dir = %q, want %q", cfg.MediaCacheDir, cacheDir)
	}
	if cfg.MediaMaxFileBytes != 26<<20 || cfg.MediaMaxBatchBytes != 101<<20 || cfg.MediaCacheMaxBytes != 501<<20 || cfg.MediaRetention != 73*time.Hour {
		t.Fatalf("media configuration = %#v", cfg)
	}
}

func TestLoadFromEnvStrictMakesExplicitMediaCacheAbsolute(t *testing.T) {
	relative := filepath.Join("relative", "cache", "..", "media")
	t.Setenv("E2E_MEDIA_CACHE_DIR", relative)

	cfg, err := LoadFromEnvStrict()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MediaCacheDir != want || !filepath.IsAbs(cfg.MediaCacheDir) || cfg.MediaCacheDir != filepath.Clean(cfg.MediaCacheDir) {
		t.Fatalf("media cache dir = %q, want clean absolute %q", cfg.MediaCacheDir, want)
	}
}

func TestLoadFromEnvStrictMakesDefaultMediaCacheAbsoluteForRelativeWorkdir(t *testing.T) {
	relativeWorkDir := filepath.Join("relative", "work", "..", "work-final")
	t.Setenv("E2E_DEFAULT_WORKDIR", relativeWorkDir)
	unsetEnv(t, "E2E_MEDIA_CACHE_DIR")

	cfg, err := LoadFromEnvStrict()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Join(relativeWorkDir, ".lark-agent-bridge", "media"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultWorkDir != relativeWorkDir {
		t.Fatalf("default workdir = %q, want existing relative semantics %q", cfg.DefaultWorkDir, relativeWorkDir)
	}
	if cfg.MediaCacheDir != want || !filepath.IsAbs(cfg.MediaCacheDir) || cfg.MediaCacheDir != filepath.Clean(cfg.MediaCacheDir) {
		t.Fatalf("media cache dir = %q, want clean absolute %q", cfg.MediaCacheDir, want)
	}
}

func TestLoadFromEnvStrictRejectsInvalidExplicitMediaSettings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "empty_cache_dir", env: "E2E_MEDIA_CACHE_DIR", value: ""},
		{name: "non_number_file_limit", env: "E2E_MEDIA_MAX_FILE_MB", value: "many"},
		{name: "zero_batch_limit", env: "E2E_MEDIA_MAX_BATCH_MB", value: "0"},
		{name: "negative_cache_limit", env: "E2E_MEDIA_CACHE_MAX_MB", value: "-1"},
		{name: "non_number_retention", env: "E2E_MEDIA_RETENTION_HOURS", value: "tomorrow"},
		{name: "zero_retention", env: "E2E_MEDIA_RETENTION_HOURS", value: "0"},
		{name: "negative_retention", env: "E2E_MEDIA_RETENTION_HOURS", value: "-1"},
		{name: "file_limit_overflow", env: "E2E_MEDIA_MAX_FILE_MB", value: "8796093022208"},
		{name: "retention_overflow", env: "E2E_MEDIA_RETENTION_HOURS", value: "2562048"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			if _, err := LoadFromEnvStrict(); err == nil {
				t.Fatalf("LoadFromEnvStrict() error = nil, want failure for %s=%q", tc.env, tc.value)
			}
		})
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	value, ok := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(name, value)
			return
		}
		_ = os.Unsetenv(name)
	})
}
