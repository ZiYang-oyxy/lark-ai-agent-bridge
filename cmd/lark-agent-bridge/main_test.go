package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
)

func TestNewServeAuditRecorderWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "audit.jsonl")
	recorder, closeFn, err := newServeAuditRecorder(config.Config{AuditLogPath: path})
	if err != nil {
		t.Fatalf("new recorder error: %v", err)
	}
	recorder.Record("u1", "run_input", "claude:chat", "token=secret-value")
	closeFn()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log error: %v", err)
	}
	log := string(data)
	if strings.Contains(log, "secret-value") {
		t.Fatalf("secret leaked in audit log: %s", log)
	}
	if !strings.Contains(log, `"Action":"run_input"`) || !strings.Contains(log, `[REDACTED]`) {
		t.Fatalf("audit log missing expected fields: %s", log)
	}
}

func TestApplyDefaultWorkDirPreservesExplicitAuditLog(t *testing.T) {
	t.Setenv("E2E_AUDIT_LOG", "/tmp/custom-audit.jsonl")
	cfg := config.Config{AuditLogPath: "/tmp/custom-audit.jsonl"}
	if err := applyDefaultWorkDir(&cfg, "/tmp/work"); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultWorkDir != "/tmp/work" {
		t.Fatalf("default workdir = %q, want /tmp/work", cfg.DefaultWorkDir)
	}
	if cfg.AuditLogPath != "/tmp/custom-audit.jsonl" {
		t.Fatalf("audit path = %q, want explicit path preserved", cfg.AuditLogPath)
	}
}

func TestApplyDefaultWorkDirRebasesImplicitSessionStore(t *testing.T) {
	t.Setenv("E2E_AUDIT_LOG", "")
	t.Setenv("E2E_SESSION_STORE", "")
	cfg := config.Config{}
	if err := applyDefaultWorkDir(&cfg, "/tmp/work"); err != nil {
		t.Fatal(err)
	}
	if cfg.SessionStorePath != filepath.Join("/tmp/work", ".lark-agent-bridge", "sessions.json") {
		t.Fatalf("session store = %q", cfg.SessionStorePath)
	}
}

func TestApplyDefaultWorkDirRebasesImplicitMediaCacheAsAbsolute(t *testing.T) {
	t.Setenv("E2E_MEDIA_CACHE_DIR", "")
	relative := filepath.Join("relative", "workspace")
	cfg := config.Config{}
	if err := applyDefaultWorkDir(&cfg, relative); err != nil {
		t.Fatal(err)
	}
	wantWorkDir, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wantWorkDir, ".lark-agent-bridge", "media")
	if cfg.MediaCacheDir != want || !filepath.IsAbs(cfg.MediaCacheDir) {
		t.Fatalf("media cache = %q, want absolute %q", cfg.MediaCacheDir, want)
	}
}

func TestApplyDefaultWorkDirPreservesExplicitMediaCache(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "media")
	t.Setenv("E2E_MEDIA_CACHE_DIR", explicit)
	cfg := config.Config{MediaCacheDir: explicit}
	if err := applyDefaultWorkDir(&cfg, filepath.Join("relative", "workspace")); err != nil {
		t.Fatal(err)
	}
	if cfg.MediaCacheDir != explicit {
		t.Fatalf("media cache = %q, want explicit %q", cfg.MediaCacheDir, explicit)
	}
}

func TestRuntimeCommandsRejectInvalidMediaEnvironment(t *testing.T) {
	t.Setenv("E2E_MEDIA_MAX_FILE_MB", "0")
	for _, tc := range []struct {
		name string
		run  func([]string) error
	}{
		{name: "simulate-action", run: runSimulateAction},
		{name: "doctor", run: runDoctor},
		{name: "simulate", run: runSimulate},
		{name: "serve", run: runServe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(nil); err == nil || !strings.Contains(err.Error(), "E2E_MEDIA_MAX_FILE_MB") {
				t.Fatalf("error = %v, want strict media config rejection", err)
			}
		})
	}
}

func TestNewServeMediaUsesConfiguredLimitsAndSharedTokenSource(t *testing.T) {
	tokens := feishu.NewTenantTokenSource("app", "secret")
	cfg := config.Config{
		MediaCacheDir:      filepath.Join(t.TempDir(), "media"),
		MediaMaxFileBytes:  25 << 20,
		MediaMaxBatchBytes: 100 << 20,
		MediaCacheMaxBytes: 500 << 20,
		MediaRetention:     72 * time.Hour,
	}
	wiring := newServeMedia(cfg, tokens)
	if wiring.cache == nil || wiring.gc == nil || wiring.downloader == nil {
		t.Fatalf("incomplete media wiring: %#v", wiring)
	}
	if wiring.downloader.Tokens != tokens {
		t.Fatal("media downloader did not reuse the shared tenant token source")
	}
}

func TestRunLongConnUntilStoppedReturnsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	client := fakeLongConnClient{
		run: func(ctx context.Context, _ func(context.Context, feishu.InboundMessage) error) error {
			close(started)
			<-ctx.Done()
			<-release
			return ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- runLongConnUntilStopped(ctx, client, func(context.Context, feishu.InboundMessage) error { return nil })
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runLongConnUntilStopped error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runLongConnUntilStopped did not return after context cancel")
	}
	close(release)
}

func TestRunLongConnUntilStoppedReturnsClientError(t *testing.T) {
	want := errors.New("connect failed")
	client := fakeLongConnClient{
		run: func(context.Context, func(context.Context, feishu.InboundMessage) error) error {
			return want
		},
	}
	err := runLongConnUntilStopped(context.Background(), client, func(context.Context, feishu.InboundMessage) error { return nil })
	if !errors.Is(err, want) {
		t.Fatalf("runLongConnUntilStopped error = %v, want %v", err, want)
	}
}

type fakeLongConnClient struct {
	run func(context.Context, func(context.Context, feishu.InboundMessage) error) error
}

func (f fakeLongConnClient) Run(ctx context.Context, handler func(context.Context, feishu.InboundMessage) error) error {
	return f.run(ctx, handler)
}
