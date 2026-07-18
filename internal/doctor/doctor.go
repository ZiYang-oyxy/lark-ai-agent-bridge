package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

type Check struct {
	Name    string
	OK      bool
	Warning bool
	Detail  string
}

func Run(cfg config.Config) []Check {
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
		mediaCacheWritable(cfg.MediaCacheDir),
		optionalEnv("E2E_CALLBACK_ADDR"),
		durationPositive("card_update_every", cfg.CardUpdateEvery),
		durationPositive("interaction_timeout", cfg.InteractionTimeout),
		intPositive("card_max_chars", cfg.CardMaxChars),
	}
	return checks
}

func mediaCacheWritable(path string) Check {
	if path == "" {
		return Check{Name: "media_cache", OK: false, Detail: "empty"}
	}
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return Check{Name: "media_cache", OK: false, Detail: "symlink_path: " + path}
	}
	if err != nil && !os.IsNotExist(err) {
		return Check{Name: "media_cache", OK: false, Detail: "stat_failed: " + path}
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return Check{Name: "media_cache", OK: false, Detail: "create_failed: " + path}
	}
	info, err = os.Lstat(path)
	if err != nil {
		return Check{Name: "media_cache", OK: false, Detail: "stat_failed: " + path}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Check{Name: "media_cache", OK: false, Detail: "symlink_path: " + path}
	}
	if !info.IsDir() {
		return Check{Name: "media_cache", OK: false, Detail: "not_directory: " + path}
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
	return Check{Name: "media_cache", OK: true, Detail: path}
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
