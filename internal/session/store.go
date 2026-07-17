package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const SnapshotVersion = 1

type Receipt struct {
	MessageID string    `json:"message_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      uint64    `json:"revision"`
	SavedAt       time.Time `json:"saved_at"`
	Sessions      []Session `json:"sessions"`
	Receipts      []Receipt `json:"dedup_receipts,omitempty"`
}

func SaveSnapshot(path string, snapshot Snapshot) error {
	if path == "" {
		return errors.New("session: empty snapshot path")
	}

	snapshot.SchemaVersion = SnapshotVersion
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session snapshot directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session snapshot: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("create session snapshot temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set session snapshot temp file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write session snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync session snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close session snapshot: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace session snapshot: %w", err)
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}

func LoadSnapshot(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{SchemaVersion: SnapshotVersion}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read session snapshot: %w", err)
	}

	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode session snapshot: %w", err)
	}
	if snapshot.SchemaVersion != SnapshotVersion {
		return Snapshot{}, fmt.Errorf("unsupported session snapshot schema %d", snapshot.SchemaVersion)
	}
	return snapshot, nil
}
