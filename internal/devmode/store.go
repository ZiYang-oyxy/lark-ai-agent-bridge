// Package devmode persists process-level developer flags for the bridge. The
// only flag today is Prerelease: when on, the update checker follows the opt-in
// prerelease (rc) channel instead of stable. It is a global, admin-controlled
// toggle (set via the hidden /.devel command), persisted so it survives the
// restart that an rc upgrade itself triggers.
package devmode

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

// State is the full developer-mode snapshot. Kept as a struct (not a bare bool)
// so future developer toggles can be added without a schema bump churn.
type State struct {
	Prerelease bool `json:"prerelease"`
}

type snapshot struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      uint64 `json:"revision"`
	DevMode       State  `json:"dev_mode"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	revision uint64
	state    State
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("devmode: empty store path")
	}
	store := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read devmode store: %w", err)
	}
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("decode devmode store: %w", err)
	}
	if saved.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported devmode schema %d", saved.SchemaVersion)
	}
	store.revision = saved.Revision
	store.state = saved.DevMode
	return store, nil
}

// Get returns the current state. A nil store reports the zero (all-off) state,
// so callers can treat "no store configured" as "developer mode off".
func (s *Store) Get() State {
	if s == nil {
		return State{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Prerelease is a convenience accessor for the sole flag; nil-safe.
func (s *Store) Prerelease() bool {
	return s.Get().Prerelease
}

// SetPrerelease persists the prerelease flag and returns the resulting state.
func (s *Store) SetPrerelease(on bool) (State, error) {
	if s == nil {
		return State{}, errors.New("devmode: store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := State{Prerelease: on}
	revision := s.revision + 1
	if err := saveSnapshot(s.path, snapshot{SchemaVersion: SchemaVersion, Revision: revision, DevMode: next}); err != nil {
		return State{}, err
	}
	s.state = next
	s.revision = revision
	return next, nil
}

func saveSnapshot(path string, saved snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create devmode store directory: %w", err)
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("encode devmode store: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".devmode-*.tmp")
	if err != nil {
		return fmt.Errorf("create devmode store temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod devmode store temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write devmode store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync devmode store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close devmode store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace devmode store: %w", err)
	}
	return nil
}
