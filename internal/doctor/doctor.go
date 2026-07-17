package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"lark-agent-bridge/internal/config"
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
		optionalEnv("E2E_CALLBACK_ADDR"),
		durationPositive("card_update_every", cfg.CardUpdateEvery),
		durationPositive("interaction_timeout", cfg.InteractionTimeout),
		intPositive("card_max_chars", cfg.CardMaxChars),
	}
	return checks
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
