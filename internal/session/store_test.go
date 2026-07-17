package session

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots", "sessions.json")
	in := Snapshot{
		SchemaVersion: SnapshotVersion,
		Revision:      7,
		SavedAt:       time.Unix(7, 0).UTC(),
		Sessions:      []Session{{ID: "session-1"}},
		Receipts: []Receipt{{
			MessageID: "message-1",
			ExpiresAt: time.Unix(17, 0).UTC(),
		}},
	}

	if err := SaveSnapshot(path, in); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SnapshotVersion || got.Revision != in.Revision {
		t.Fatalf("snapshot = %#v", got)
	}
	if !got.SavedAt.Equal(in.SavedAt) {
		t.Fatalf("saved at = %s, want %s", got.SavedAt, in.SavedAt)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "session-1" {
		t.Fatalf("sessions = %#v", got.Sessions)
	}
	if len(got.Receipts) != 1 || got.Receipts[0].MessageID != "message-1" || !got.Receipts[0].ExpiresAt.Equal(in.Receipts[0].ExpiresAt) {
		t.Fatalf("receipts = %#v", got.Receipts)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if parent.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %v, want 0700", parent.Mode().Perm())
	}
}

func TestLoadSnapshotReturnsCurrentEmptySnapshotWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")

	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SnapshotVersion {
		t.Fatalf("schema version = %d, want %d", got.SchemaVersion, SnapshotVersion)
	}
}

func TestLoadSnapshotRejectsMalformedDataWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	want := []byte(`{"schema_version":`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadSnapshot(path); err == nil {
		t.Fatal("expected decode error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file changed: got %q, want %q", got, want)
	}
}

func TestLoadSnapshotRejectsUnknownVersionWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	want := []byte(`{"schema_version":99}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadSnapshot(path); err == nil {
		t.Fatal("expected schema error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file changed: got %q, want %q", got, want)
	}
}
