package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/reply"
	"lark-agent-bridge/internal/session"
)

const nativeSequenceJournalSchemaVersion = 1

type nativeSequenceJournalSnapshot struct {
	SchemaVersion int                                    `json:"schema_version"`
	Intents       map[string]feishu.NativeSequenceIntent `json:"intents"`
}

type nativeSequenceJournal struct {
	mu       sync.Mutex
	path     string
	sessions *session.Manager
	replies  *reply.Store
	intents  map[string]feishu.NativeSequenceIntent
}

func NewNativeSequenceJournal(path string, sessions *session.Manager, replies *reply.Store) (*nativeSequenceJournal, error) {
	if strings.TrimSpace(path) == "" || sessions == nil || replies == nil {
		return nil, errors.New("native sequence journal requires path, session manager and reply store")
	}
	j := &nativeSequenceJournal{path: path, sessions: sessions, replies: replies, intents: map[string]feishu.NativeSequenceIntent{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read native sequence journal: %w", err)
	}
	var snapshot nativeSequenceJournalSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode native sequence journal: %w", err)
	}
	if snapshot.SchemaVersion != nativeSequenceJournalSchemaVersion {
		return nil, fmt.Errorf("unsupported native sequence journal schema %d", snapshot.SchemaVersion)
	}
	for key, intent := range snapshot.Intents {
		if err := validateNativeSequenceIntent(intent); err != nil || key != nativeSequenceIntentKey(intent) {
			return nil, fmt.Errorf("invalid native sequence journal intent %q", key)
		}
		j.intents[key] = intent
	}
	return j, nil
}

func (j *nativeSequenceJournal) PrepareNative(_ context.Context, intent feishu.NativeSequenceIntent) error {
	if err := validateNativeSequenceIntent(intent); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	key := nativeSequenceIntentKey(intent)
	if _, exists := j.intents[key]; exists {
		return fmt.Errorf("native sequence intent already exists")
	}
	candidate := cloneNativeIntents(j.intents)
	candidate[key] = intent
	if err := saveNativeSequenceJournal(j.path, candidate); err != nil {
		return err
	}
	j.intents = candidate
	pending := intent.Ref
	pending.SequenceUnknown = true
	pending.PendingSequence = intent.Candidate
	if err := j.sessions.ReplaceActiveBatchRenderRef(intent.SessionID, intent.BatchID, pending); err != nil {
		return err
	}
	if intent.LatestScope != "" {
		if err := j.replies.SetLatest(intent.LatestScope, &pending); err != nil {
			return err
		}
	}
	return nil
}

func (j *nativeSequenceJournal) ConfirmNative(_ context.Context, intent feishu.NativeSequenceIntent) error {
	confirmed := intent.Ref
	confirmed.Version = intent.Candidate
	confirmed.SequenceUnknown = false
	confirmed.PendingSequence = 0
	return j.finish(intent, confirmed)
}

func (j *nativeSequenceJournal) AbortNative(_ context.Context, intent feishu.NativeSequenceIntent) error {
	restored := intent.Ref
	restored.SequenceUnknown = false
	restored.PendingSequence = 0
	return j.finish(intent, restored)
}

func (j *nativeSequenceJournal) finish(intent feishu.NativeSequenceIntent, ref session.RenderRef) error {
	if err := validateNativeSequenceIntent(intent); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	key := nativeSequenceIntentKey(intent)
	stored, ok := j.intents[key]
	if !ok || stored.Ref.CardID != intent.Ref.CardID || stored.Ref.ReplyMessageID != intent.Ref.ReplyMessageID || stored.Ref.Version != intent.Ref.Version {
		return fmt.Errorf("native sequence intent not found or mismatched")
	}
	if err := j.sessions.ReplaceActiveBatchRenderRef(intent.SessionID, intent.BatchID, ref); err != nil {
		return err
	}
	if intent.LatestScope != "" {
		latest := j.replies.GetLatest(intent.LatestScope)
		if latest != nil && latest.CardID == intent.Ref.CardID && latest.ReplyMessageID == intent.Ref.ReplyMessageID {
			if err := j.replies.SetLatest(intent.LatestScope, &ref); err != nil {
				return err
			}
		}
	}
	candidate := cloneNativeIntents(j.intents)
	delete(candidate, key)
	if err := saveNativeSequenceJournal(j.path, candidate); err != nil {
		return err
	}
	j.intents = candidate
	return nil
}

func (j *nativeSequenceJournal) RenderRefSequenceUnknown(ref session.RenderRef) bool {
	if ref.SequenceUnknown {
		return true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, intent := range j.intents {
		if intent.Ref.CardID == ref.CardID && intent.Ref.ReplyMessageID == ref.ReplyMessageID && intent.Ref.Version == ref.Version {
			return true
		}
	}
	return false
}

func validateNativeSequenceIntent(intent feishu.NativeSequenceIntent) error {
	if strings.TrimSpace(intent.SessionID) == "" || strings.TrimSpace(intent.BatchID) == "" || strings.TrimSpace(intent.Ref.CardID) == "" || intent.Candidate <= intent.Ref.Version || intent.Candidate <= 0 {
		return errors.New("invalid native sequence intent")
	}
	return nil
}

func nativeSequenceIntentKey(intent feishu.NativeSequenceIntent) string {
	return intent.SessionID + "\x00" + intent.BatchID + "\x00" + intent.Ref.CardID + "\x00" + strconv.Itoa(intent.Candidate)
}

func cloneNativeIntents(source map[string]feishu.NativeSequenceIntent) map[string]feishu.NativeSequenceIntent {
	cloned := make(map[string]feishu.NativeSequenceIntent, len(source))
	for key, intent := range source {
		cloned[key] = intent
	}
	return cloned
}

func saveNativeSequenceJournal(path string, intents map[string]feishu.NativeSequenceIntent) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create native sequence journal directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure native sequence journal directory: %w", err)
	}
	data, err := json.MarshalIndent(nativeSequenceJournalSnapshot{SchemaVersion: nativeSequenceJournalSchemaVersion, Intents: intents}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode native sequence journal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".native-sequence-*.tmp")
	if err != nil {
		return fmt.Errorf("create native sequence journal temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}
