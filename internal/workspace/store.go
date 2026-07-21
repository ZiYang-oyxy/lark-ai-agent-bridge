// Package workspace stores per-scope working directories and named aliases,
// persisted to workspaces.json with atomic writes. A "scope" is the topic-level
// session identity (chatID or chatID:threadID); it is the sole authority for a
// topic's cwd.
package workspace

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

// aliasSep joins scope and alias name into a scoped key. \x1f (US, the unit
// separator control char) cannot appear in a scope or user-typed name.
const aliasSep = "\x1f"

type ScopeWorkspace struct {
	Cwd string `json:"cwd"`
}

type snapshot struct {
	SchemaVersion int                       `json:"schema_version"`
	Scopes        map[string]ScopeWorkspace `json:"scopes,omitempty"`
	Named         map[string]string         `json:"named,omitempty"`
}

type Store struct {
	mu     sync.RWMutex
	path   string
	scopes map[string]ScopeWorkspace
	named  map[string]string
}

func OpenWorkspaceStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("workspace: empty store path")
	}
	s := &Store{path: path, scopes: map[string]ScopeWorkspace{}, named: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read workspace store: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("decode workspace store: %w", err)
	}
	if snap.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported workspace schema %d", snap.SchemaVersion)
	}
	if snap.Scopes != nil {
		s.scopes = snap.Scopes
	}
	if snap.Named != nil {
		s.named = snap.Named
	}
	return s, nil
}

func (s *Store) CwdFor(scope string) (string, bool) {
	scope = strings.TrimSpace(scope)
	s.mu.RLock()
	defer s.mu.RUnlock()
	ws, ok := s.scopes[scope]
	if !ok || ws.Cwd == "" {
		return "", false
	}
	return ws.Cwd, true
}

func (s *Store) SetCwd(scope, cwd string) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return errors.New("workspace: empty scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneScopes(s.scopes)
	next[scope] = ScopeWorkspace{Cwd: cwd}
	if err := s.saveLocked(next, s.named); err != nil {
		return err
	}
	s.scopes = next
	return nil
}

func (s *Store) ClearCwd(scope string) error {
	scope = strings.TrimSpace(scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.scopes[scope]; !ok {
		return nil
	}
	next := cloneScopes(s.scopes)
	delete(next, scope)
	if err := s.saveLocked(next, s.named); err != nil {
		return err
	}
	s.scopes = next
	return nil
}

func aliasKey(scope, name string) string {
	return strings.TrimSpace(scope) + aliasSep + strings.TrimSpace(name)
}

func (s *Store) SaveNamed(scope, name, cwd string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("workspace: empty alias name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneNamed(s.named)
	next[aliasKey(scope, name)] = cwd
	if err := s.saveLocked(s.scopes, next); err != nil {
		return err
	}
	s.named = next
	return nil
}

func (s *Store) UseNamed(scope, name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cwd, ok := s.named[aliasKey(scope, name)]
	if !ok {
		// legacy fallback: alias stored without scope prefix
		cwd, ok = s.named[strings.TrimSpace(name)]
	}
	return cwd, ok
}

func (s *Store) RemoveNamed(scope, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := aliasKey(scope, name)
	if _, ok := s.named[key]; !ok {
		return nil
	}
	next := cloneNamed(s.named)
	delete(next, key)
	if err := s.saveLocked(s.scopes, next); err != nil {
		return err
	}
	s.named = next
	return nil
}

// ListNamed returns the aliases visible to a scope (bare name -> cwd).
func (s *Store) ListNamed(scope string) map[string]string {
	scope = strings.TrimSpace(scope)
	prefix := scope + aliasSep
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]string{}
	for k, v := range s.named {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

func cloneScopes(in map[string]ScopeWorkspace) map[string]ScopeWorkspace {
	out := make(map[string]ScopeWorkspace, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneNamed(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *Store) saveLocked(scopes map[string]ScopeWorkspace, named map[string]string) error {
	snap := snapshot{SchemaVersion: SchemaVersion, Scopes: scopes, Named: named}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workspace store: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".workspaces-*.tmp")
	if err != nil {
		return fmt.Errorf("create workspace temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set workspace temp permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write workspace store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync workspace store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close workspace store: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace workspace store: %w", err)
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}
