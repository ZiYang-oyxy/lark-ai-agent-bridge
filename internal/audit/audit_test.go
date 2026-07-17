package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRecorderWritesRedactedJSONL(t *testing.T) {
	var buf bytes.Buffer
	recorder := NewRecorderWithWriter(&buf)
	recorder.Record("u1", "run_input", "claude:chat", "token=secret-value password:123")
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("audit log line is empty")
	}
	if strings.Contains(line, "secret-value") || strings.Contains(line, "123") {
		t.Fatalf("secret leaked in audit log: %s", line)
	}
	var event Event
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("audit log is not JSON: %v", err)
	}
	if event.Detail != "token=[REDACTED] password:[REDACTED]" {
		t.Fatalf("detail = %q, want redacted detail", event.Detail)
	}
}
