package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lark-agent-bridge/internal/agent"
)

const catalogSchemaVersion = 1

type CatalogIdentity struct {
	Agent   agent.Kind
	WorkDir string
}

type CatalogEntry struct {
	SessionID                 string     `json:"session_id"`
	Agent                     agent.Kind `json:"agent"`
	WorkDir                   string     `json:"workdir"`
	UpdatedAt                 time.Time  `json:"updated_at"`
	BridgeInstructionsVersion string     `json:"bridge_instructions_version,omitempty"`
	Summary                   string     `json:"summary,omitempty"`
}

type catalogSnapshot struct {
	SchemaVersion int            `json:"schema_version"`
	SavedAt       time.Time      `json:"saved_at"`
	Entries       []CatalogEntry `json:"entries"`
}

type Catalog struct {
	mu      sync.Mutex
	path    string
	entries map[string]CatalogEntry
}

func CanonicalWorkDir(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("session catalog: empty workdir")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("session catalog: absolute workdir: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("session catalog: resolve workdir: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("session catalog: stat workdir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("session catalog: workdir is not a directory: %s", real)
	}
	return filepath.Clean(real), nil
}

func OpenCatalog(path string) (*Catalog, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("session catalog: empty path")
	}
	catalog := &Catalog{path: path, entries: map[string]CatalogEntry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return catalog, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session catalog: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot catalogSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode session catalog: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode session catalog: %w", err)
	}
	if snapshot.SchemaVersion != catalogSchemaVersion {
		return nil, fmt.Errorf("unsupported session catalog schema %d", snapshot.SchemaVersion)
	}
	for _, entry := range snapshot.Entries {
		if err := validateCatalogEntry(entry); err != nil {
			return nil, fmt.Errorf("invalid session catalog entry: %w", err)
		}
		key := catalogKey(entry.Agent, entry.WorkDir, entry.SessionID)
		if _, exists := catalog.entries[key]; exists {
			return nil, fmt.Errorf("duplicate session catalog entry %q", entry.SessionID)
		}
		catalog.entries[key] = entry
	}
	return catalog, nil
}

func (c *Catalog) Recent(identity CatalogIdentity, limit int) []CatalogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit <= 0 {
		return nil
	}
	entries := make([]CatalogEntry, 0)
	for _, entry := range c.entries {
		if entry.Agent == identity.Agent && entry.WorkDir == identity.WorkDir {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].SessionID < entries[j].SessionID
		}
		return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return append([]CatalogEntry(nil), entries...)
}

func (c *Catalog) Resolve(identity CatalogIdentity, sessionID string) (CatalogEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[catalogKey(identity.Agent, identity.WorkDir, strings.TrimSpace(sessionID))]
	return entry, ok
}

func (c *Catalog) Upsert(entry CatalogEntry) error {
	if err := validateCatalogEntry(entry); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	candidate := cloneCatalogEntries(c.entries)
	candidate[catalogKey(entry.Agent, entry.WorkDir, entry.SessionID)] = entry
	if err := c.saveLocked(candidate); err != nil {
		return err
	}
	c.entries = candidate
	return nil
}

func (c *Catalog) Touch(identity CatalogIdentity, sessionID string, now time.Time) (CatalogEntry, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := catalogKey(identity.Agent, identity.WorkDir, strings.TrimSpace(sessionID))
	entry, ok := c.entries[key]
	if !ok {
		return CatalogEntry{}, fmt.Errorf("session catalog: session not found")
	}
	entry.UpdatedAt = now
	candidate := cloneCatalogEntries(c.entries)
	candidate[key] = entry
	if err := c.saveLocked(candidate); err != nil {
		return CatalogEntry{}, err
	}
	c.entries = candidate
	return entry, nil
}

func (c *Catalog) saveLocked(entries map[string]CatalogEntry) error {
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session catalog directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("set session catalog directory permissions: %w", err)
	}
	list := make([]CatalogEntry, 0, len(entries))
	for _, entry := range entries {
		list = append(list, entry)
	}
	sort.Slice(list, func(i, j int) bool {
		return catalogKey(list[i].Agent, list[i].WorkDir, list[i].SessionID) < catalogKey(list[j].Agent, list[j].WorkDir, list[j].SessionID)
	})
	data, err := json.MarshalIndent(catalogSnapshot{SchemaVersion: catalogSchemaVersion, SavedAt: time.Now().UTC(), Entries: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session catalog: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".session-catalog-*.tmp")
	if err != nil {
		return fmt.Errorf("create session catalog temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set session catalog temp permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write session catalog: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync session catalog: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close session catalog: %w", err)
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return fmt.Errorf("replace session catalog: %w", err)
	}
	parent, err := os.Open(dir)
	if err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}

func validateCatalogEntry(entry CatalogEntry) error {
	if entry.Agent != agent.Claude && entry.Agent != agent.Codex {
		return fmt.Errorf("session catalog: invalid agent %q", entry.Agent)
	}
	if strings.TrimSpace(entry.SessionID) == "" || entry.SessionID != strings.TrimSpace(entry.SessionID) {
		return errors.New("session catalog: invalid session id")
	}
	canonical, err := CanonicalWorkDir(entry.WorkDir)
	if err != nil {
		return err
	}
	if canonical != entry.WorkDir {
		return fmt.Errorf("session catalog: non-canonical workdir %q", entry.WorkDir)
	}
	if entry.UpdatedAt.IsZero() {
		return errors.New("session catalog: zero updated_at")
	}
	return nil
}

func catalogKey(kind agent.Kind, workDir, sessionID string) string {
	return string(kind) + "\x1f" + workDir + "\x1f" + sessionID
}

func cloneCatalogEntries(entries map[string]CatalogEntry) map[string]CatalogEntry {
	cloned := make(map[string]CatalogEntry, len(entries))
	for key, entry := range entries {
		cloned[key] = entry
	}
	return cloned
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON value")
}
