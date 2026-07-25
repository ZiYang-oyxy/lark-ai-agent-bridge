package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTopicAliasStorePersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "topic-aliases.json")

	store, err := OpenTopicAliasStore(path, 10)
	if err != nil {
		t.Fatalf("open empty store: %v", err)
	}
	store.Bind("chat", "omt_real", SyntheticTopicThreadPrefix+"m1")
	if err := store.LastError(); err != nil {
		t.Fatalf("unexpected persistence error: %v", err)
	}

	// A fresh store opened on the same path must recover the binding.
	reopened, err := OpenTopicAliasStore(path, 10)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	syn, ok := reopened.Resolve("chat", "omt_real")
	if !ok || syn != SyntheticTopicThreadPrefix+"m1" {
		t.Fatalf("reopened Resolve = (%q, %v), want (%q, true)", syn, ok, SyntheticTopicThreadPrefix+"m1")
	}
	// Snapshot file must be present with restrictive permissions.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("snapshot perm = %o, want 600", perm)
	}
}

func TestTopicAliasStoreRejectsBadSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "topic-aliases.json")
	bad := topicAliasSnapshot{SchemaVersion: TopicAliasSchemaVersion + 1, Entries: []topicAliasEntry{{ChatID: "c", ThreadID: "t", SyntheticThread: "@bot:x"}}}
	data, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("marshal bad snapshot: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write bad snapshot: %v", err)
	}
	if _, err := OpenTopicAliasStore(path, 10); err == nil {
		t.Fatalf("expected schema-mismatch error, got nil")
	}
}

func TestTopicAliasStoreRejectsMissingField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "topic-aliases.json")
	bad := topicAliasSnapshot{SchemaVersion: TopicAliasSchemaVersion, Entries: []topicAliasEntry{{ChatID: "c", ThreadID: "t"}}}
	data, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := OpenTopicAliasStore(path, 10); err == nil {
		t.Fatalf("expected missing-field error, got nil")
	}
}

func TestTopicAliasStoreRejectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "topic-aliases.json")
	bad := topicAliasSnapshot{SchemaVersion: TopicAliasSchemaVersion, Entries: []topicAliasEntry{
		{ChatID: "c", ThreadID: "t", SyntheticThread: "@bot:x"},
		{ChatID: "c", ThreadID: "t", SyntheticThread: "@bot:y"},
	}}
	data, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := OpenTopicAliasStore(path, 10); err == nil {
		t.Fatalf("expected duplicate-entry error, got nil")
	}
}

func TestOpenTopicAliasStoreValidatesArgs(t *testing.T) {
	if _, err := OpenTopicAliasStore("", 10); err == nil {
		t.Fatalf("empty path should error")
	}
	if _, err := OpenTopicAliasStore(filepath.Join(t.TempDir(), "x.json"), 0); err == nil {
		t.Fatalf("non-positive maxEntries should error")
	}
}

func TestTopicAliasStoreNilReceiverSafe(t *testing.T) {
	var store *TopicAliasStore
	// Must not panic on a nil receiver.
	store.Bind("chat", "omt_real", "@bot:m1")
	if syn, ok := store.Resolve("chat", "omt_real"); ok || syn != "" {
		t.Fatalf("nil Resolve = (%q, %v), want empty/false", syn, ok)
	}
	if err := store.LastError(); err != nil {
		t.Fatalf("nil LastError = %v, want nil", err)
	}
}

func TestNewTopicAliasStoreInMemoryBindDoesNotPersist(t *testing.T) {
	store := NewTopicAliasStore()
	// Bind on an in-memory store must not panic and must not write any file.
	store.Bind("chat", "omt_real", SyntheticTopicThreadPrefix+"m1")
	if err := store.LastError(); err != nil {
		t.Fatalf("in-memory Bind should not record error, got %v", err)
	}
	if syn, ok := store.Resolve("chat", "omt_real"); !ok || syn != SyntheticTopicThreadPrefix+"m1" {
		t.Fatalf("in-memory Resolve = (%q, %v), want bound value", syn, ok)
	}
}
