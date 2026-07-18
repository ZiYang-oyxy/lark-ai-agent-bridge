package reply

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"lark-agent-bridge/internal/session"
)

const StoreSchemaVersion = 1

type storeSnapshot struct {
	SchemaVersion int                          `json:"schema_version"`
	Revision      uint64                       `json:"revision"`
	LatestByScope map[string]session.RenderRef `json:"latest_by_scope"`
}

type Store struct {
	mu            sync.RWMutex
	path          string
	revision      uint64
	latestByScope map[string]session.RenderRef
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("reply: empty store path")
	}
	store := &Store{path: path, latestByScope: map[string]session.RenderRef{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read reply store: %w", err)
	}
	var snapshot storeSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode reply store: %w", err)
	}
	if snapshot.SchemaVersion != StoreSchemaVersion {
		return nil, fmt.Errorf("unsupported reply store schema %d", snapshot.SchemaVersion)
	}
	store.revision = snapshot.Revision
	for scope, ref := range snapshot.LatestByScope {
		if strings.TrimSpace(scope) == "" {
			return nil, errors.New("reply store contains empty scope")
		}
		store.latestByScope[scope] = ref
	}
	return store, nil
}

func (s *Store) GetLatest(scope string) *session.RenderRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.latestByScope[scope]
	if !ok {
		return nil
	}
	return cloneRenderRef(&ref)
}

func (s *Store) SetLatest(scope string, ref *session.RenderRef) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return errors.New("reply: empty scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := make(map[string]session.RenderRef, len(s.latestByScope)+1)
	for key, value := range s.latestByScope {
		candidate[key] = value
	}
	if ref == nil {
		delete(candidate, scope)
	} else {
		candidate[scope] = *cloneRenderRef(ref)
	}
	revision := s.revision + 1
	if err := saveSnapshot(s.path, storeSnapshot{SchemaVersion: StoreSchemaVersion, Revision: revision, LatestByScope: candidate}); err != nil {
		return err
	}
	s.latestByScope = candidate
	s.revision = revision
	return nil
}

func saveSnapshot(path string, snapshot storeSnapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create reply store directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode reply store: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".replies-*.tmp")
	if err != nil {
		return fmt.Errorf("create reply store temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set reply store temp permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write reply store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync reply store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close reply store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace reply store: %w", err)
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}

func cloneRenderRef(ref *session.RenderRef) *session.RenderRef {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}
