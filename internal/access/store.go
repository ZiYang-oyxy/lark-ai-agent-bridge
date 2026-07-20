package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const SchemaVersion = 1

type snapshot struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      uint64 `json:"revision"`
	Access        Policy `json:"access"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	revision uint64
	policy   Policy
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("access: empty store path")
	}
	store := &Store{path: path, policy: Policy{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read access store: %w", err)
	}
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("decode access store: %w", err)
	}
	if saved.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported access schema %d", saved.SchemaVersion)
	}
	store.revision = saved.Revision
	store.policy = normalizePolicy(saved.Access)
	return store, nil
}

func (s *Store) Get() Policy {
	if s == nil {
		return Policy{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clonePolicy(s.policy)
}

func (s *Store) Update(mutate func(*Policy)) error {
	if s == nil {
		return errors.New("access: store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clonePolicy(s.policy)
	mutate(&next)
	next = normalizePolicy(next)
	revision := s.revision + 1
	if err := saveSnapshot(s.path, snapshot{SchemaVersion: SchemaVersion, Revision: revision, Access: next}); err != nil {
		return err
	}
	s.policy = next
	s.revision = revision
	return nil
}

func saveSnapshot(path string, saved snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create access store directory: %w", err)
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("encode access store: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".access-*.tmp")
	if err != nil {
		return fmt.Errorf("create access store temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod access store temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write access store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync access store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close access store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace access store: %w", err)
	}
	return nil
}

func normalizePolicy(policy Policy) Policy {
	policy.AllowedUsers = uniqueStrings(policy.AllowedUsers)
	policy.AllowedChats = uniqueStrings(policy.AllowedChats)
	policy.Admins = uniqueStrings(policy.Admins)
	return policy
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func clonePolicy(policy Policy) Policy {
	return Policy{
		AllowedUsers: append([]string(nil), policy.AllowedUsers...),
		AllowedChats: append([]string(nil), policy.AllowedChats...),
		Admins:       append([]string(nil), policy.Admins...),
	}
}
