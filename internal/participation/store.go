package participation

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
	"time"
)

const SchemaVersion = 1

var ErrNotParticipated = errors.New("topic is not participated")

type Entry struct {
	ChatID              string    `json:"chat_id"`
	ThreadID            string    `json:"thread_id"`
	FirstParticipatedAt time.Time `json:"first_participated_at"`
	LastSeenAt          time.Time `json:"last_seen_at"`
}

type snapshot struct {
	SchemaVersion int     `json:"schema_version"`
	Revision      uint64  `json:"revision"`
	Topics        []Entry `json:"topics"`
}

type Store struct {
	mu         sync.RWMutex
	path       string
	maxEntries int
	revision   uint64
	entries    map[string]Entry
}

func OpenStore(path string, maxEntries int) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("participation: empty store path")
	}
	if maxEntries <= 0 {
		return nil, errors.New("participation: max entries must be positive")
	}
	s := &Store{path: path, maxEntries: maxEntries, entries: make(map[string]Entry)}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read participation store: %w", err)
	}
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("decode participation store: %w", err)
	}
	if saved.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported participation schema %d", saved.SchemaVersion)
	}
	for _, entry := range saved.Topics {
		if entry.ChatID == "" || entry.ThreadID == "" || entry.FirstParticipatedAt.IsZero() || entry.LastSeenAt.IsZero() {
			return nil, errors.New("invalid participation entry")
		}
		key := topicKey(entry.ChatID, entry.ThreadID)
		if _, exists := s.entries[key]; exists {
			return nil, fmt.Errorf("duplicate participation entry %q", key)
		}
		s.entries[key] = entry
	}
	if len(s.entries) > maxEntries {
		return nil, fmt.Errorf("participation store has %d entries, max %d", len(s.entries), maxEntries)
	}
	s.revision = saved.Revision
	return s, nil
}

func (s *Store) Has(chatID, threadID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.entries[topicKey(chatID, threadID)]
	return ok
}

func (s *Store) Entry(chatID, threadID string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[topicKey(chatID, threadID)]
	return entry, ok
}

func (s *Store) Entries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedEntries(s.entries)
}

func (s *Store) Mark(chatID, threadID string, now time.Time) error {
	if chatID == "" || threadID == "" || now.IsZero() {
		return errors.New("participation: chat, thread and timestamp are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneEntries(s.entries)
	key := topicKey(chatID, threadID)
	entry, exists := next[key]
	if !exists {
		entry = Entry{ChatID: chatID, ThreadID: threadID, FirstParticipatedAt: now}
	}
	entry.LastSeenAt = now
	next[key] = entry
	evictToLimit(next, s.maxEntries)
	return s.publishLocked(next)
}

func (s *Store) Touch(chatID, threadID string, now time.Time) error {
	if now.IsZero() {
		return errors.New("participation: timestamp is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := topicKey(chatID, threadID)
	entry, exists := s.entries[key]
	if !exists {
		return ErrNotParticipated
	}
	next := cloneEntries(s.entries)
	entry.LastSeenAt = now
	next[key] = entry
	return s.publishLocked(next)
}

func (s *Store) publishLocked(next map[string]Entry) error {
	revision := s.revision + 1
	if err := saveSnapshot(s.path, snapshot{SchemaVersion: SchemaVersion, Revision: revision, Topics: sortedEntries(next)}); err != nil {
		return err
	}
	s.entries = next
	s.revision = revision
	return nil
}

func saveSnapshot(path string, saved snapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create participation directory: %w", err)
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("encode participation store: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".participation-*.tmp")
	if err != nil {
		return fmt.Errorf("create participation temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set participation temp permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write participation store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync participation store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close participation store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace participation store: %w", err)
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}

func evictToLimit(entries map[string]Entry, maxEntries int) {
	for len(entries) > maxEntries {
		var oldestKey string
		var oldest Entry
		first := true
		for key, entry := range entries {
			if first || entry.LastSeenAt.Before(oldest.LastSeenAt) || entry.LastSeenAt.Equal(oldest.LastSeenAt) && key < oldestKey {
				oldestKey, oldest, first = key, entry, false
			}
		}
		delete(entries, oldestKey)
	}
}

func sortedEntries(entries map[string]Entry) []Entry {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Entry, 0, len(keys))
	for _, key := range keys {
		out = append(out, entries[key])
	}
	return out
}

func cloneEntries(entries map[string]Entry) map[string]Entry {
	out := make(map[string]Entry, len(entries))
	for key, entry := range entries {
		out[key] = entry
	}
	return out
}

func topicKey(chatID, threadID string) string {
	return chatID + "\x00" + threadID
}
