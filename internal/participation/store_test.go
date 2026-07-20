package participation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsParticipationAndTouchesAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "topics.json")
	store, err := OpenStore(path, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Unix(100, 0).UTC()
	last := time.Unix(200, 0).UTC()
	if err := store.Mark("chat", "thread", first); err != nil {
		t.Fatal(err)
	}
	if err := store.Touch("chat", "thread", last); err != nil {
		t.Fatal(err)
	}
	if !store.Has("chat", "thread") {
		t.Fatal("store lost marked topic")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	reopened, err := OpenStore(path, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reopened.Entry("chat", "thread")
	if !ok || !entry.FirstParticipatedAt.Equal(first) || !entry.LastSeenAt.Equal(last) {
		t.Fatalf("reopened entry = %#v, ok=%t", entry, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		SchemaVersion int    `json:"schema_version"`
		Revision      uint64 `json:"revision"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatal(err)
	}
	if header.SchemaVersion != SchemaVersion || header.Revision != 2 {
		t.Fatalf("snapshot header = %#v", header)
	}
}

func TestStoreRejectsCorruptAndUnsupportedSnapshots(t *testing.T) {
	for _, raw := range []string{`{`, `{"schema_version":99,"revision":1,"topics":[]}`} {
		path := filepath.Join(t.TempDir(), "topics.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(path, 10_000); err == nil {
			t.Fatalf("OpenStore(%q) error = nil", raw)
		}
	}
}

func TestStoreEvictsDeterministicLeastRecentlySeenEntry(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "topics.json"), 2)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(100, 0).UTC()
	if err := store.Mark("chat-b", "thread", stamp); err != nil {
		t.Fatal(err)
	}
	if err := store.Mark("chat-a", "thread", stamp); err != nil {
		t.Fatal(err)
	}
	if err := store.Mark("chat-c", "thread", stamp.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if store.Has("chat-a", "thread") {
		t.Fatal("lexicographically first tied LRU entry was not evicted")
	}
	if !store.Has("chat-b", "thread") || !store.Has("chat-c", "thread") {
		t.Fatalf("unexpected survivors: %#v", store.Entries())
	}
}

func TestStoreDoesNotPublishFailedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "topics.json")
	store, err := OpenStore(path, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mark("chat", "thread-1", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Mark("chat", "thread-2", time.Unix(200, 0)); err == nil {
		t.Fatal("Mark error = nil")
	}
	if store.Has("chat", "thread-2") {
		t.Fatal("failed Mark was published in memory")
	}
}

func TestStoreTouchRequiresExistingParticipation(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "topics.json"), 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Touch("chat", "missing", time.Now()); !errors.Is(err, ErrNotParticipated) {
		t.Fatalf("Touch error = %v", err)
	}
}
