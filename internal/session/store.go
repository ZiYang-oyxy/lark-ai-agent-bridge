package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const SnapshotVersion = 2

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
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("set session snapshot directory permissions: %w", err)
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

	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Snapshot{}, fmt.Errorf("decode session snapshot: %w", err)
	}
	if header.SchemaVersion == 1 {
		migrated, err := migrateSnapshotV1(data)
		if err != nil {
			return Snapshot{}, fmt.Errorf("migrate session snapshot: %w", err)
		}
		data = migrated
	} else if header.SchemaVersion != SnapshotVersion {
		return Snapshot{}, fmt.Errorf("unsupported session snapshot schema %d", header.SchemaVersion)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode session snapshot: %w", err)
	}
	return snapshot, nil
}

func migrateSnapshotV1(data []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	sessions, _ := raw["sessions"].([]any)
	for _, value := range sessions {
		session, _ := value.(map[string]any)
		if session == nil {
			continue
		}
		if _, exists := session["AgentSessionID"]; !exists {
			if legacy, ok := session["ClaudeSessionID"]; ok {
				session["AgentSessionID"] = legacy
			}
		}
		delete(session, "ClaudeSessionID")
	}
	raw["schema_version"] = SnapshotVersion
	return json.Marshal(raw)
}

// AcceptMessage records a successfully handled non-run command message. A
// duplicate unexpired message is rejected without changing the snapshot.
func (m *Manager) AcceptMessage(messageID string, now time.Time, ttl time.Duration, maxReceipts int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if messageID == "" {
		return true, nil
	}

	receipts := pruneReceipts(m.receipts, now)
	if hasReceipt(receipts, messageID) {
		return false, nil
	}
	if ttl <= 0 {
		if len(receipts) == len(m.receipts) {
			return true, nil
		}
		return m.publishReceiptCandidateLocked(receipts)
	}
	receipts = appendReceipt(receipts, Receipt{MessageID: messageID, ExpiresAt: now.Add(ttl)}, maxReceipts)
	return m.publishReceiptCandidateLocked(receipts)
}

// AcceptAndEnqueue atomically records a message receipt and queues its input.
// A store-aware manager saves the complete candidate before publishing either
// mutation, so retryable persistence failures cannot create partial state.
func (m *Manager) AcceptAndEnqueue(key Key, input Input, now time.Time, ttl time.Duration, maxReceipts int, limits BatchLimits) (bool, EnqueueResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessions := cloneSessions(m.sessions)
	receipts := pruneReceipts(m.receipts, now)
	if input.ID != "" && hasReceipt(receipts, input.ID) {
		return false, EnqueueResult{}, nil
	}

	s := ensureSessionIn(sessions, key, input.WorkDir)
	pending := len(s.Queue)
	if s.ActiveBatch != nil {
		pending += len(s.ActiveBatch.Inputs)
	}
	if limits.MaxPending > 0 && pending >= limits.MaxPending {
		return false, EnqueueResult{}, fmt.Errorf("session: pending input limit %d reached", limits.MaxPending)
	}

	if input.Time.IsZero() {
		input.Time = now
	}
	if input.State == "" {
		input.State = InputQueued
	}
	s.Queue = append(s.Queue, input)
	if input.State == InputDebouncing && !input.DebounceUntil.IsZero() {
		extendCompatibleDebounceTail(s.Queue)
	}
	if input.ID != "" && ttl > 0 {
		receipts = appendReceipt(receipts, Receipt{MessageID: input.ID, ExpiresAt: now.Add(ttl)}, maxReceipts)
	}

	if m.storePath != "" {
		if err := m.persistCandidateLocked(sessions, receipts, m.revision); err != nil {
			return false, EnqueueResult{}, err
		}
	} else {
		m.sessions = sessions
		m.receipts = cloneReceipts(receipts)
	}
	return true, EnqueueResult{Position: len(s.Queue), Queued: true}, nil
}

func extendCompatibleDebounceTail(queue []Input) {
	last := len(queue) - 1
	newest := queue[last]
	if newest.Reset {
		return
	}
	for i := last - 1; i >= 0; i-- {
		preceding := &queue[i]
		if preceding.State != InputDebouncing || preceding.Reset || !compatibleBatchInput(*preceding, newest) {
			return
		}
		if preceding.DebounceUntil.Before(newest.DebounceUntil) {
			preceding.DebounceUntil = newest.DebounceUntil
		}
	}
}

func (m *Manager) publishReceiptCandidateLocked(receipts []Receipt) (bool, error) {
	if m.storePath != "" {
		if err := m.persistCandidateLocked(cloneSessions(m.sessions), receipts, m.revision); err != nil {
			return false, err
		}
		return true, nil
	}
	m.receipts = cloneReceipts(receipts)
	return true, nil
}

func pruneReceipts(receipts []Receipt, now time.Time) []Receipt {
	pruned := make([]Receipt, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt.ExpiresAt.After(now) {
			pruned = append(pruned, receipt)
		}
	}
	return pruned
}

func hasReceipt(receipts []Receipt, messageID string) bool {
	for _, receipt := range receipts {
		if receipt.MessageID == messageID {
			return true
		}
	}
	return false
}

func appendReceipt(receipts []Receipt, receipt Receipt, maxReceipts int) []Receipt {
	receipts = append(receipts, receipt)
	if maxReceipts <= 0 || len(receipts) <= maxReceipts {
		return receipts
	}

	// Preserve the receipt being accepted even when its expiry is tied with an
	// older entry. The remaining evictions are deterministic by expiry then ID.
	candidates := make([]int, len(receipts)-1)
	for i := range candidates {
		candidates[i] = i
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := receipts[candidates[i]], receipts[candidates[j]]
		if left.ExpiresAt.Equal(right.ExpiresAt) {
			return left.MessageID < right.MessageID
		}
		return left.ExpiresAt.Before(right.ExpiresAt)
	})
	remove := make(map[int]struct{}, len(receipts)-maxReceipts)
	for _, index := range candidates[:len(receipts)-maxReceipts] {
		remove[index] = struct{}{}
	}
	kept := make([]Receipt, 0, maxReceipts)
	for index, existing := range receipts {
		if _, drop := remove[index]; !drop {
			kept = append(kept, existing)
		}
	}
	return kept
}
