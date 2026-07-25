package contextusage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestReadAfterRejectsStaleSidecar(t *testing.T) {
	dir := t.TempDir()
	started := time.Now()
	writeFixture(t, dir, "stale.json", `{"session_id":"stale","used_percentage":85,"updated_at":1}`)
	writeFixture(t, dir, "fresh.json", fmt.Sprintf(`{"session_id":"fresh","used_percentage":85,"updated_at":%d}`, time.Now().Add(time.Minute).UnixMilli()))

	if got := ReadAfter(dir, "stale", started); got.Reason != ReasonStale || got.OK {
		t.Fatalf("stale ReadAfter = %+v, want stale", got)
	}
	if got := ReadAfter(dir, "fresh", started); !got.OK || got.UsedPercent != 85 {
		t.Fatalf("fresh ReadAfter = %+v, want 85%%", got)
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
	writeFixture(t, dir, "codex-current.json", `{"session_id":"codex-current","used_percentage":null,"total_tokens":999999,"context_tokens":50000,"context_window_size":200000,"model":"gpt-5.6-sol","reasoning_effort":"high"}`)

	tests := []struct {
		name      string
		dir       string
		sessionID string
		want      Usage
	}{
		{"ok", dir, "sess-ok", Usage{OK: true, UsedPercent: 42, TotalTokens: 84000, ContextWindow: 200000}},
		{"null-pct-computed", dir, "sess-null-pct", Usage{OK: true, UsedPercent: 25, TotalTokens: 50000, ContextWindow: 200000}},
		{"percent-only-no-window", dir, "sess-nowin", Usage{OK: true, UsedPercent: 77}},
		{"codex-current-context", dir, "codex-current", Usage{OK: true, UsedPercent: 25, TotalTokens: 50000, ContextWindow: 200000, Model: "gpt-5.6-sol", ReasoningEffort: "high"}},
		{"all-null", dir, "sess-all-null", Usage{OK: false, Reason: ReasonEmpty}},
		{"session-mismatch", dir, "sess-mismatch", Usage{OK: false, Reason: ReasonMismatch}},
		{"bad-json", dir, "sess-bad", Usage{OK: false, Reason: ReasonInvalid}},
		{"missing-file", dir, "sess-absent", Usage{OK: false, Reason: ReasonMissing}},
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

func TestReadLatestForWorkDirSelectsNewestValidCanonicalMatch(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(workDir, alias); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dir, "older.json", fmt.Sprintf(`{"session_id":"older","cwd":%q,"used_percentage":20,"model":"old","updated_at":10}`, workDir))
	writeFixture(t, dir, "newer.json", fmt.Sprintf(`{"session_id":"newer","cwd":%q,"used_percentage":45,"context_tokens":90000,"context_window_size":200000,"model":"gpt-new","updated_at":20}`, alias))
	writeFixture(t, dir, "other.json", `{"session_id":"other","cwd":"/other","used_percentage":99,"updated_at":30}`)
	writeFixture(t, dir, "invalid.json", fmt.Sprintf(`{"session_id":"invalid","cwd":%q,"used_percentage":null,"updated_at":40}`, workDir))

	got := ReadLatestForWorkDir(dir, workDir)
	if !got.OK || got.UsedPercent != 45 || got.TotalTokens != 90000 || got.ContextWindow != 200000 || got.Model != "gpt-new" {
		t.Fatalf("ReadLatestForWorkDir = %+v", got)
	}
	if got := ReadLatestForWorkDir(dir, t.TempDir()); got.OK || got.Reason != ReasonMissing {
		t.Fatalf("unmatched workdir = %+v, want missing", got)
	}
}
