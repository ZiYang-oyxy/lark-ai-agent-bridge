package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// TopicAliasSchemaVersion is the on-disk snapshot schema. Bump when the
// serialised shape changes so an incompatible file is rejected on load rather
// than silently misread.
const TopicAliasSchemaVersion = 1

// SyntheticTopicThreadPrefix is prepended to a Feishu message_id to build a
// per-mention synthetic thread key. Kept as a package constant so the
// sessionKeyForMode router and TopicJoinObserver alias binder always agree.
const SyntheticTopicThreadPrefix = "@bot:"

// TopicAliasStore remembers that a Feishu thread_id (the real omt_* id assigned
// by Feishu when the bot's first CardKit reply lands) is an alias for a
// synthetic "@bot:<msg_id>" thread key we used to run the top-level @bot
// message on. Without this alias, follow-up messages inside the topic (which
// arrive with a real thread_id) would land on a different, empty session and
// lose continuity with the run they replied to.
//
// The store is optionally durable: when opened with OpenTopicAliasStore it
// atomically persists every Bind to a JSON snapshot (temp-file + rename), so
// aliases survive a supervisor restart or upgrade. Stores constructed via
// NewTopicAliasStore have an empty path and stay purely in-memory (Bind only
// mutates the map, never touches disk) — that keeps tests and no-persistence
// callers working unchanged.
type TopicAliasStore struct {
	mu         sync.RWMutex
	path       string
	maxEntries int
	revision   uint64
	entries    map[topicAliasKey]string
	// lastErr records the most recent persistence failure for audit. Bind is
	// signature-locked (no return), so a caller that wants to observe save
	// failures can read this. Best-effort: never blocks or panics the binder.
	lastErr error
}

type topicAliasKey struct {
	ChatID   string
	ThreadID string
}

type topicAliasEntry struct {
	ChatID          string `json:"chat_id"`
	ThreadID        string `json:"thread_id"`
	SyntheticThread string `json:"synthetic_thread"`
}

type topicAliasSnapshot struct {
	SchemaVersion int               `json:"schema_version"`
	Revision      uint64            `json:"revision"`
	Entries       []topicAliasEntry `json:"entries"`
}

// NewTopicAliasStore constructs an empty, in-memory-only alias store. Its path
// is empty, so Bind updates the map without ever writing to disk. Use this for
// tests and no-persistence callers; use OpenTopicAliasStore for a durable one.
func NewTopicAliasStore() *TopicAliasStore {
	return &TopicAliasStore{entries: make(map[topicAliasKey]string)}
}

// OpenTopicAliasStore opens a durable alias store backed by path. A missing
// file yields an empty store (not an error). An existing snapshot is loaded and
// validated: an unknown schema, a duplicate key, or a missing field is rejected
// so a corrupt file never silently drops or mis-binds aliases.
func OpenTopicAliasStore(path string, maxEntries int) (*TopicAliasStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("topic alias: empty store path")
	}
	if maxEntries <= 0 {
		return nil, errors.New("topic alias: max entries must be positive")
	}
	s := &TopicAliasStore{path: path, maxEntries: maxEntries, entries: make(map[topicAliasKey]string)}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read topic alias store: %w", err)
	}
	var saved topicAliasSnapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("decode topic alias store: %w", err)
	}
	if saved.SchemaVersion != TopicAliasSchemaVersion {
		return nil, fmt.Errorf("unsupported topic alias schema %d", saved.SchemaVersion)
	}
	for _, entry := range saved.Entries {
		if entry.ChatID == "" || entry.ThreadID == "" || entry.SyntheticThread == "" {
			return nil, errors.New("invalid topic alias entry")
		}
		key := topicAliasKey{ChatID: entry.ChatID, ThreadID: entry.ThreadID}
		if _, exists := s.entries[key]; exists {
			return nil, fmt.Errorf("duplicate topic alias entry %q/%q", entry.ChatID, entry.ThreadID)
		}
		s.entries[key] = entry.SyntheticThread
	}
	if len(s.entries) > maxEntries {
		return nil, fmt.Errorf("topic alias store has %d entries, max %d", len(s.entries), maxEntries)
	}
	s.revision = saved.Revision
	return s, nil
}

// Bind records that (chatID, realThreadID) should resolve to syntheticThread.
// Nil receivers and empty inputs are ignored, so callers can wire this
// unconditionally from CardKit reply callbacks. On a durable store it also
// persists the snapshot best-effort: a disk failure updates the in-memory map,
// records the error on the store for audit, and returns without panicking or
// blocking the caller (the alias is still live for this process lifetime).
func (s *TopicAliasStore) Bind(chatID, realThreadID, syntheticThread string) {
	if s == nil || chatID == "" || realThreadID == "" || syntheticThread == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := topicAliasKey{ChatID: chatID, ThreadID: realThreadID}
	prev, hadPrev := s.entries[key]
	if hadPrev && prev == syntheticThread {
		return
	}
	s.entries[key] = syntheticThread
	s.evictToLimitLocked()
	if s.path == "" {
		return
	}
	revision := s.revision + 1
	if err := saveTopicAliasSnapshot(s.path, s.snapshotLocked(revision)); err != nil {
		// Best-effort: keep the in-memory binding, remember the error for
		// audit, but do not revert or fail the caller.
		s.lastErr = err
		return
	}
	s.revision = revision
	s.lastErr = nil
}

// Resolve returns the synthetic thread previously bound for (chatID,
// realThreadID). Second return is false if no alias is registered.
func (s *TopicAliasStore) Resolve(chatID, realThreadID string) (string, bool) {
	if s == nil || chatID == "" || realThreadID == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.entries[topicAliasKey{ChatID: chatID, ThreadID: realThreadID}]
	return v, ok
}

// LastError returns the most recent persistence error (nil if the last Bind
// persisted cleanly or the store is in-memory only). Intended for audit.
func (s *TopicAliasStore) LastError() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

// evictToLimitLocked drops the lexicographically-smallest keys until the store
// is within maxEntries. maxEntries<=0 (in-memory stores) means unbounded. The
// eviction order is deterministic but arbitrary; alias entries are cheap and
// this bound only guards against unbounded growth, not recency.
func (s *TopicAliasStore) evictToLimitLocked() {
	if s.maxEntries <= 0 {
		return
	}
	for len(s.entries) > s.maxEntries {
		var victim topicAliasKey
		first := true
		for key := range s.entries {
			if first || lessTopicAliasKey(key, victim) {
				victim, first = key, false
			}
		}
		delete(s.entries, victim)
	}
}

func (s *TopicAliasStore) snapshotLocked(revision uint64) topicAliasSnapshot {
	entries := make([]topicAliasEntry, 0, len(s.entries))
	for key, syn := range s.entries {
		entries = append(entries, topicAliasEntry{ChatID: key.ChatID, ThreadID: key.ThreadID, SyntheticThread: syn})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ChatID != entries[j].ChatID {
			return entries[i].ChatID < entries[j].ChatID
		}
		return entries[i].ThreadID < entries[j].ThreadID
	})
	return topicAliasSnapshot{SchemaVersion: TopicAliasSchemaVersion, Revision: revision, Entries: entries}
}

func lessTopicAliasKey(a, b topicAliasKey) bool {
	if a.ChatID != b.ChatID {
		return a.ChatID < b.ChatID
	}
	return a.ThreadID < b.ThreadID
}

func saveTopicAliasSnapshot(path string, saved topicAliasSnapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create topic alias directory: %w", err)
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("encode topic alias store: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".topic-aliases-*.tmp")
	if err != nil {
		return fmt.Errorf("create topic alias temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set topic alias temp permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write topic alias store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync topic alias store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close topic alias store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace topic alias store: %w", err)
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}
