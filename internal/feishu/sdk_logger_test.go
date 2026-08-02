package feishu

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRedactingLoggerRemovesWebSocketCredentials(t *testing.T) {
	var out bytes.Buffer
	logger := newSDKLogger(&out)
	logger.Info(context.Background(), "connected to wss://open.feishu.cn/callback?ticket=top-secret&service_id=42")

	got := out.String()
	for _, secret := range []string{"top-secret", "ticket=", "service_id=42"} {
		if strings.Contains(got, secret) {
			t.Fatalf("log leaked %q: %s", secret, got)
		}
	}
	for _, want := range []string{"[Info]", "connected to", "open.feishu.cn", "[REDACTED]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q: %s", want, got)
		}
	}
}

func TestRedactingLoggerRemovesNamedSecretsOutsideURLs(t *testing.T) {
	var out bytes.Buffer
	logger := newSDKLogger(&out)
	logger.Warn(context.Background(), "request app_secret=very-secret Authorization: Bearer bearer-secret failed")

	got := out.String()
	if strings.Contains(got, "very-secret") || strings.Contains(got, "bearer-secret") {
		t.Fatalf("log leaked named secret: %s", got)
	}
	if !strings.Contains(got, "request") || !strings.Contains(got, "failed") {
		t.Fatalf("log lost operational context: %s", got)
	}
}
