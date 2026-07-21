package contextusage

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "sess-ok.json", `{"session_id":"sess-ok","used_percentage":42,"total_tokens":84000,"context_window_size":200000,"extra":"ignored"}`)
	writeFixture(t, dir, "sess-null-pct.json", `{"session_id":"sess-null-pct","used_percentage":null,"total_tokens":50000,"context_window_size":200000}`)
	writeFixture(t, dir, "sess-all-null.json", `{"session_id":"sess-all-null","used_percentage":null,"total_tokens":null,"context_window_size":null}`)
	writeFixture(t, dir, "sess-mismatch.json", `{"session_id":"different","used_percentage":30}`)
	writeFixture(t, dir, "sess-bad.json", `{not-json`)
	writeFixture(t, dir, "sess-nowin.json", `{"session_id":"sess-nowin","used_percentage":77,"total_tokens":null,"context_window_size":null}`)

	tests := []struct {
		name      string
		dir       string
		sessionID string
		want      Usage
	}{
		{"ok", dir, "sess-ok", Usage{OK: true, UsedPercent: 42, TotalTokens: 84000, ContextWindow: 200000}},
		{"null-pct-computed", dir, "sess-null-pct", Usage{OK: true, UsedPercent: 25, TotalTokens: 50000, ContextWindow: 200000}},
		{"percent-only-no-window", dir, "sess-nowin", Usage{OK: true, UsedPercent: 77}},
		{"all-null", dir, "sess-all-null", Usage{OK: false}},
		{"session-mismatch", dir, "sess-mismatch", Usage{OK: false}},
		{"bad-json", dir, "sess-bad", Usage{OK: false}},
		{"missing-file", dir, "sess-absent", Usage{OK: false}},
		{"empty-dir", "", "sess-ok", Usage{OK: false}},
		{"empty-session", dir, "", Usage{OK: false}},
		{"path-traversal", dir, "../sess-ok", Usage{OK: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Read(tt.dir, tt.sessionID)
			if got != tt.want {
				t.Fatalf("Read(%q,%q) = %+v, want %+v", tt.dir, tt.sessionID, got, tt.want)
			}
		})
	}
}
