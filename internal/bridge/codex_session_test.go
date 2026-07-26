package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexTranscriptModelReadsCurrentSessionSettings(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "sessions", "2026", "07", "26", "rollout-2026-07-26-thread-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "{\"type\":\"session_meta\",\"payload\":{}}\n" +
		"{\"type\":\"event_msg\",\"payload\":{\"type\":\"thread_settings_applied\",\"thread_settings\":{\"model\":\"gpt-5.6-luna\"}}}\n" +
		"{\"type\":\"turn_context\",\"payload\":{\"model\":\"gpt-5.6-sol\"}}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codexTranscriptModel(home, "thread-1"); got != "gpt-5.6-sol" {
		t.Fatalf("model = %q", got)
	}
}

func TestCodexTranscriptModelIgnoresOtherSessionAndMalformedRecords(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "07", "26")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-other-session.jsonl"), []byte("{not json}\n{\"type\":\"turn_context\",\"payload\":{\"model\":\"wrong\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codexTranscriptModel(home, "thread-1"); got != "" {
		t.Fatalf("model = %q, want empty", got)
	}
}
