package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/reply"
	"lark-agent-bridge/internal/session"
)

type Check struct {
	Name    string
	OK      bool
	Warning bool
	Detail  string
}

type RunOptions struct {
	WrapperPreflight bool
	Strict           bool
	PreflightTimeout time.Duration
}

func Run(cfg config.Config) []Check {
	return runStatic(cfg)
}

func RunWithOptions(ctx context.Context, cfg config.Config, opts RunOptions) []Check {
	checks := runStatic(cfg)
	if !opts.WrapperPreflight {
		return checks
	}
	timeout := opts.PreflightTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	preflightCtx, cancel := context.WithTimeout(ctx, timeout)
	check := ClaudeWrapperPreflight(preflightCtx, cfg)
	cancel()
	if opts.Strict && check.Warning {
		check.OK = false
		check.Warning = false
	}
	return append(checks, check)
}

func runStatic(cfg config.Config) []Check {
	claudeBin := cfg.ClaudeBin
	if claudeBin == "" {
		claudeBin = "claude"
	}
	checks := []Check{
		lookPath("claude", claudeBin),
		envPresent("LARK_APP_ID"),
		envPresent("LARK_APP_SECRET"),
		{Name: "default_agent", OK: cfg.DefaultAgent != "", Detail: cfg.DefaultAgent},
		dirExists("default_workdir", cfg.DefaultWorkDir),
		auditLogWritable(cfg.AuditLogPath),
		sessionStoreWritable(cfg.SessionStorePath),
		preferenceStoreWritable(cfg),
		replyStoreWritable(cfg.ReplyStorePath),
		mediaCacheWritable(cfg.MediaCacheDir),
		optionalEnv("E2E_CALLBACK_ADDR"),
		durationPositive("card_update_every", cfg.CardUpdateEvery),
		durationPositive("interaction_timeout", cfg.InteractionTimeout),
		intPositive("card_max_chars", cfg.CardMaxChars),
		intPositive("card_min_delta_chars", cfg.CardMinDeltaChars),
		intPositive("card_preview_max_chars", cfg.CardPreviewMaxChars),
		agentsConfigCheck(cfg),
	}
	return checks
}

// agentsConfigCheck verifies agents.json parses. It is a soft check: an
// unreadable or invalid document is a warning, not a failure, because the
// service falls back to the built-in default catalogue.
func agentsConfigCheck(cfg config.Config) Check {
	agents, err := config.LoadAgentsConfig(cfg.AgentsConfigPath)
	if err != nil {
		return Check{Name: "agents_config", OK: true, Warning: true, Detail: "using built-in default: " + err.Error()}
	}
	detail := fmt.Sprintf("%d agent(s): %s", len(agents.Agents), strings.Join(agents.Kinds(), ","))
	if _, ok := agents.Find("codex"); ok {
		detail += "; Codex policy and model come from executable configuration"
	}
	return Check{Name: "agents_config", OK: true, Detail: detail}
}

func ClaudeWrapperPreflight(ctx context.Context, cfg config.Config) Check {
	const name = "wrapper-preflight"
	claudeBin := cfg.ClaudeBin
	if claudeBin == "" {
		claudeBin = "claude"
	}
	path, err := exec.LookPath(claudeBin)
	if err != nil {
		return Check{Name: name, OK: true, Warning: true, Detail: "skipped: claude executable not found"}
	}
	if info, err := os.Stat(cfg.DefaultWorkDir); err != nil || !info.IsDir() {
		return Check{Name: name, OK: true, Warning: true, Detail: "skipped: default workdir unavailable"}
	}
	cmd := exec.CommandContext(ctx, path,
		"-p", "--output-format", "stream-json", "--verbose", "--effort", "low",
		"Reply with exactly OK. Do not use tools.",
	)
	cmd.Dir = cfg.DefaultWorkDir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err = cmd.Run()
	if err == nil {
		return Check{Name: name, OK: true, Detail: "passed"}
	}
	if ctx.Err() != nil {
		return Check{Name: name, OK: true, Warning: true, Detail: "timed out"}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 78 {
			return Check{Name: name, OK: true, Warning: true, Detail: "exit 78: check wrapper .env mode 0600 and prerequisites"}
		}
		return Check{Name: name, OK: true, Warning: true, Detail: fmt.Sprintf("failed with exit %d", exitErr.ExitCode())}
	}
	return Check{Name: name, OK: true, Warning: true, Detail: "failed to start"}
}

func mediaCacheWritable(path string) Check {
	if path == "" {
		return Check{Name: "media_cache", OK: false, Detail: "empty"}
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return Check{Name: "media_cache", OK: false, Detail: "not_absolute: " + path}
	}
	if err := preparePrivateMediaCache(path); err != nil {
		return Check{Name: "media_cache", OK: false, Detail: "unsafe_path: " + path}
	}
	probe, err := os.CreateTemp(path, ".media-cache-probe-*")
	if err != nil {
		return Check{Name: "media_cache", OK: false, Detail: "write_probe_failed: " + path}
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return Check{Name: "media_cache", OK: false, Detail: "write_probe_failed: " + path}
	}
	if err := os.Remove(probePath); err != nil {
		return Check{Name: "media_cache", OK: false, Detail: "write_probe_cleanup_failed: " + path}
	}
	if err := walkMediaCachePath(path, false); err != nil {
		return Check{Name: "media_cache", OK: false, Detail: "unsafe_path: " + path}
	}
	return Check{Name: "media_cache", OK: true, Detail: path}
}

func preparePrivateMediaCache(path string) error {
	if err := walkMediaCachePath(path, true); err != nil {
		return err
	}
	return walkMediaCachePath(path, false)
}

func walkMediaCachePath(path string, createMissing bool) error {
	volume := filepath.VolumeName(path)
	root := volume + string(os.PathSeparator)
	components := []string{root}
	current := root
	for _, component := range strings.Split(strings.TrimPrefix(path, root), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		components = append(components, current)
	}
	for _, component := range components {
		info, err := os.Lstat(component)
		created := false
		if os.IsNotExist(err) {
			if !createMissing {
				return fmt.Errorf("path component does not exist: %s", component)
			}
			if err := os.Mkdir(component, 0o700); err != nil {
				return fmt.Errorf("create path component %s: %w", component, err)
			}
			created = true
			info, err = os.Lstat(component)
		}
		if err != nil {
			return fmt.Errorf("inspect path component %s: %w", component, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component is a symlink: %s", component)
		}
		if !info.IsDir() {
			return fmt.Errorf("path component is not a directory: %s", component)
		}
		if (created || component == path) && info.Mode().Perm() != 0o700 {
			return fmt.Errorf("private path component has mode %o: %s", info.Mode().Perm(), component)
		}
	}
	return nil
}

func sessionStoreWritable(path string) Check {
	if path == "" {
		return Check{Name: "session_store", OK: false, Detail: "empty"}
	}
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Check{Name: "session_store", OK: false, Detail: "parent_create_failed: " + path}
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return Check{Name: "session_store", OK: false, Detail: "parent_stat_failed: " + path}
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Check{Name: "session_store", OK: false, Detail: "parent_not_directory: " + path}
	}
	if info.Mode().Perm() != 0o700 {
		return Check{Name: "session_store", OK: false, Detail: "parent_insecure_permissions: " + path}
	}
	probe, err := os.CreateTemp(dir, ".session-store-probe-*")
	if err != nil {
		return Check{Name: "session_store", OK: false, Detail: "write_probe_failed: " + path}
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return Check{Name: "session_store", OK: false, Detail: "write_probe_failed: " + path}
	}
	if err := os.Remove(probePath); err != nil {
		return Check{Name: "session_store", OK: false, Detail: "write_probe_cleanup_failed: " + path}
	}

	info, err = os.Lstat(path)
	if os.IsNotExist(err) {
		return Check{Name: "session_store", OK: true, Detail: path}
	}
	if err != nil {
		return Check{Name: "session_store", OK: false, Detail: "store_stat_failed: " + path}
	}
	if !info.Mode().IsRegular() {
		return Check{Name: "session_store", OK: false, Detail: "not_regular: " + path}
	}
	if info.Mode().Perm() != 0o600 {
		return Check{Name: "session_store", OK: false, Detail: "insecure_permissions: " + path}
	}
	if _, err := session.LoadSnapshot(path); err != nil {
		return Check{Name: "session_store", OK: false, Detail: "invalid_snapshot: " + path}
	}
	return Check{Name: "session_store", OK: true, Detail: path}
}

func preferenceStoreWritable(cfg config.Config) Check {
	path := cfg.PreferenceStorePath
	if path == "" {
		return Check{Name: "preference_store", OK: false, Detail: "empty"}
	}
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Check{Name: "preference_store", OK: false, Detail: "parent_create_failed: " + path}
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return Check{Name: "preference_store", OK: false, Detail: "parent_stat_failed: " + path}
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Check{Name: "preference_store", OK: false, Detail: "parent_not_directory: " + path}
	}
	if info.Mode().Perm() != 0o700 {
		return Check{Name: "preference_store", OK: false, Detail: "parent_insecure_permissions: " + path}
	}
	probe, err := os.CreateTemp(dir, ".preference-store-probe-*")
	if err != nil {
		return Check{Name: "preference_store", OK: false, Detail: "write_probe_failed: " + path}
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return Check{Name: "preference_store", OK: false, Detail: "write_probe_failed: " + path}
	}
	if err := os.Remove(probePath); err != nil {
		return Check{Name: "preference_store", OK: false, Detail: "write_probe_cleanup_failed: " + path}
	}

	info, err = os.Lstat(path)
	if os.IsNotExist(err) {
		return Check{Name: "preference_store", OK: true, Detail: path}
	}
	if err != nil {
		return Check{Name: "preference_store", OK: false, Detail: "store_stat_failed: " + path}
	}
	if !info.Mode().IsRegular() {
		return Check{Name: "preference_store", OK: false, Detail: "not_regular: " + path}
	}
	if info.Mode().Perm() != 0o600 {
		return Check{Name: "preference_store", OK: false, Detail: "insecure_permissions: " + path}
	}
	defaults := config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: cfg.ReplyMode, ConversationMode: cfg.ConversationMode}
	if _, err := config.OpenPreferenceStore(path, defaults, cfg.AllowedModels); err != nil {
		return Check{Name: "preference_store", OK: false, Detail: "invalid_snapshot: " + path}
	}
	return Check{Name: "preference_store", OK: true, Detail: path}
}

func replyStoreWritable(path string) Check {
	const name = "reply_store"
	if path == "" {
		return Check{Name: name, OK: false, Detail: "empty"}
	}
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Check{Name: name, OK: false, Detail: "parent_create_failed: " + path}
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return Check{Name: name, OK: false, Detail: "parent_stat_failed: " + path}
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Check{Name: name, OK: false, Detail: "parent_not_directory: " + path}
	}
	if info.Mode().Perm() != 0o700 {
		return Check{Name: name, OK: false, Detail: "parent_insecure_permissions: " + path}
	}
	probe, err := os.CreateTemp(dir, ".reply-store-probe-*")
	if err != nil {
		return Check{Name: name, OK: false, Detail: "write_probe_failed: " + path}
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return Check{Name: name, OK: false, Detail: "write_probe_failed: " + path}
	}
	if err := os.Remove(probePath); err != nil {
		return Check{Name: name, OK: false, Detail: "write_probe_cleanup_failed: " + path}
	}
	info, err = os.Lstat(path)
	if os.IsNotExist(err) {
		return Check{Name: name, OK: true, Detail: path}
	}
	if err != nil {
		return Check{Name: name, OK: false, Detail: "store_stat_failed: " + path}
	}
	if !info.Mode().IsRegular() {
		return Check{Name: name, OK: false, Detail: "not_regular: " + path}
	}
	if info.Mode().Perm() != 0o600 {
		return Check{Name: name, OK: false, Detail: "insecure_permissions: " + path}
	}
	if _, err := reply.OpenStore(path); err != nil {
		return Check{Name: name, OK: false, Detail: "invalid_snapshot: " + path}
	}
	return Check{Name: name, OK: true, Detail: path}
}

func Summary(checks []Check) string {
	var b strings.Builder
	for _, c := range checks {
		status := "ok"
		if !c.OK {
			status = "fail"
		} else if c.Warning {
			status = "warn"
		}
		fmt.Fprintf(&b, "%s %s: %s\n", status, c.Name, c.Detail)
	}
	return strings.TrimSpace(b.String())
}

func lookPath(name, bin string) Check {
	path, err := exec.LookPath(bin)
	if err != nil {
		return Check{Name: name, OK: false, Detail: "not found in PATH"}
	}
	return Check{Name: name, OK: true, Detail: path}
}

func envPresent(name string) Check {
	if os.Getenv(name) == "" {
		return Check{Name: name, OK: false, Detail: "not set"}
	}
	return Check{Name: name, OK: true, Detail: "set"}
}

func optionalEnv(name string) Check {
	if value := os.Getenv(name); value != "" {
		return Check{Name: name, OK: true, Detail: value}
	}
	return Check{Name: name, OK: true, Detail: "not set"}
}

func dirExists(name, path string) Check {
	if path == "" {
		return Check{Name: name, OK: false, Detail: "empty"}
	}
	info, err := os.Stat(path)
	if err != nil {
		return Check{Name: name, OK: false, Detail: err.Error()}
	}
	if !info.IsDir() {
		return Check{Name: name, OK: false, Detail: "not a directory: " + path}
	}
	return Check{Name: name, OK: true, Detail: path}
}

func auditLogWritable(path string) Check {
	if path == "" {
		return Check{Name: "audit_log", OK: false, Detail: "empty"}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Check{Name: "audit_log", OK: false, Detail: err.Error()}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return Check{Name: "audit_log", OK: false, Detail: err.Error()}
	}
	if err := file.Close(); err != nil {
		return Check{Name: "audit_log", OK: false, Detail: err.Error()}
	}
	return Check{Name: "audit_log", OK: true, Detail: path}
}

func durationPositive(name string, value time.Duration) Check {
	return Check{Name: name, OK: value > 0, Detail: value.String()}
}

func intPositive(name string, value int) Check {
	return Check{Name: name, OK: value > 0, Detail: fmt.Sprint(value)}
}
