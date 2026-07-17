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

func TestDecodeFlagEscapes(t *testing.T) {
	got := decodeFlagEscapes(`done\n>\tready`)
	want := "done\n>\tready"
	if got != want {
		t.Fatalf("decoded = %q, want %q", got, want)
	}
}

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
