package session

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
)

func TestSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots", "sessions.json")
	in := Snapshot{
		SchemaVersion: SnapshotVersion,
		Revision:      7,
		SavedAt:       time.Unix(7, 0).UTC(),
		Sessions:      []Session{{ID: "session-1"}},
		Receipts: []Receipt{{
			MessageID: "message-1",
			ExpiresAt: time.Unix(17, 0).UTC(),
		}},
	}

	if err := SaveSnapshot(path, in); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SnapshotVersion || got.Revision != in.Revision {
		t.Fatalf("snapshot = %#v", got)
	}
	if !got.SavedAt.Equal(in.SavedAt) {
		t.Fatalf("saved at = %s, want %s", got.SavedAt, in.SavedAt)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "session-1" {
		t.Fatalf("sessions = %#v", got.Sessions)
	}
	if len(got.Receipts) != 1 || got.Receipts[0].MessageID != "message-1" || !got.Receipts[0].ExpiresAt.Equal(in.Receipts[0].ExpiresAt) {
		t.Fatalf("receipts = %#v", got.Receipts)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if parent.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %v, want 0700", parent.Mode().Perm())
	}
}

func TestOldSnapshotBackfillsBridgeInstructionsVersionOnNextBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := Key{Agent: agent.Claude, ChatID: "old-snapshot"}
	seed := Session{Key: key, ID: key.ID(), AgentSessionID: "existing", State: StateIdle}
	if err := SaveSnapshot(path, Snapshot{Sessions: []Session{seed}}); err != nil {
		t.Fatal(err)
	}

	m := NewManagerWithStoreVersion(path, "v1")
	if _, err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(300, 0)
	if _, _, err := m.EnqueueDurable(key, Input{ID: "next", Text: "hello", State: InputQueued, Time: now}, "/w", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	_, frozen, err := m.FreezeReadyBatch(key, now, BatchLimits{})
	if err != nil || frozen == nil {
		t.Fatalf("freeze batch=%#v err=%v", frozen, err)
	}
	got, batch, err := m.MarkBatchRunning(key, frozen.ID, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.BridgeInstructionsVersion != "v1" || batch.Inputs[0].BridgeInstructionsVersion != "v1" {
		t.Fatalf("backfilled session=%q input=%q", got.BridgeInstructionsVersion, batch.Inputs[0].BridgeInstructionsVersion)
	}

	snapshot, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].BridgeInstructionsVersion != "v1" || snapshot.Sessions[0].ActiveBatch == nil || snapshot.Sessions[0].ActiveBatch.Inputs[0].BridgeInstructionsVersion != "v1" {
		t.Fatalf("saved snapshot = %#v", snapshot)
	}
}

func TestSnapshotRoundTripPreservesDebounceWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	want := 875 * time.Millisecond
	snapshot := Snapshot{Sessions: []Session{{
		Key:   Key{Agent: agent.Claude, ChatID: "debounce-window"},
		ID:    "claude:debounce-window",
		Queue: []Input{{ID: "rich", State: InputDebouncing, DebounceWindow: want}},
	}}}
	if err := SaveSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 1 || len(got.Sessions[0].Queue) != 1 || got.Sessions[0].Queue[0].DebounceWindow != want {
		t.Fatalf("snapshot = %#v, want debounce window %s", got, want)
	}
}

func TestLoadSnapshotV2WithoutDebounceWindowKeepsZeroValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	data := []byte(`{"schema_version":2,"sessions":[{"Key":{"Agent":"claude","ChatID":"legacy"},"ID":"claude:legacy","Queue":[{"ID":"legacy","State":"debouncing"}]}]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 1 || len(got.Sessions[0].Queue) != 1 || got.Sessions[0].Queue[0].DebounceWindow != 0 {
		t.Fatalf("legacy snapshot = %#v", got)
	}
}

func TestSaveSnapshotRestrictsExistingParentDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "snapshots")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := SaveSnapshot(filepath.Join(dir, "sessions.json"), Snapshot{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestSaveSnapshotRenameFailurePreservesTargetDirectoryAndCleansTempFile(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "sessions.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	keepPath := filepath.Join(path, "keep")
	want := []byte("keep")
	if err := os.WriteFile(keepPath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SaveSnapshot(path, Snapshot{}); err == nil {
		t.Fatal("expected rename error")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("target mode = %v, want directory", info.Mode())
	}
	got, err := os.ReadFile(keepPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("target content = %q, want %q", got, want)
	}
	tmpFiles, err := filepath.Glob(filepath.Join(parent, ".sessions-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpFiles) != 0 {
		t.Fatalf("temporary snapshot files remain: %v", tmpFiles)
	}
}

func TestLoadSnapshotReturnsCurrentEmptySnapshotWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")

	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SnapshotVersion {
		t.Fatalf("schema version = %d, want %d", got.SchemaVersion, SnapshotVersion)
	}
}

func TestLoadSnapshotRejectsMalformedDataWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	want := []byte(`{"schema_version":`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadSnapshot(path); err == nil {
		t.Fatal("expected decode error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file changed: got %q, want %q", got, want)
	}
}

func TestLoadSnapshotRejectsUnknownVersionWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	want := []byte(`{"schema_version":99}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadSnapshot(path); err == nil {
		t.Fatal("expected schema error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file changed: got %q, want %q", got, want)
	}
}

func TestLoadSnapshotMigratesV1ClaudeSessionID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	data := []byte(`{"schema_version":1,"sessions":[{"Key":{"Agent":"claude","ChatID":"c"},"ID":"claude:c","ClaudeSessionID":"legacy-thread"}]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 2 || len(got.Sessions) != 1 || got.Sessions[0].AgentSessionID != "legacy-thread" {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestRestoreKeepsContextButClearsPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := Key{Agent: agent.Claude, ChatID: "c"}
	created := time.Date(2026, 7, 18, 9, 30, 0, 0, time.UTC)
	seed := Session{
		Key:            key,
		ID:             key.ID(),
		WorkDir:        "/w",
		AgentSessionID: "s1",
		History:        []Prompt{{Text: "old"}},
		State:          StateRunning,
		Queue:          []Input{{ID: "q", State: InputQueued}},
		ActiveBatch: &Batch{
			ID:        "b",
			State:     InputRunning,
			RenderRef: &RenderRef{CardID: "card-1", Version: 3, CreatedAt: created, SequenceUnknown: true, PendingSequence: 2},
			Inputs:    []Input{{ID: "r", ReplyToMessageID: "m1", State: InputRunning}},
		},
	}
	if err := SaveSnapshot(path, Snapshot{Sessions: []Session{seed}}); err != nil {
		t.Fatal(err)
	}

	m := NewManagerWithStore(path)
	notices, err := m.Restore()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m.Get(key)
	if !ok || got.AgentSessionID != "s1" || len(got.History) != 1 || got.WorkDir != "/w" {
		t.Fatalf("context = %#v", got)
	}
	if len(got.Queue) != 0 || got.ActiveBatch != nil || got.State != StateIdle {
		t.Fatalf("pending survived: %#v", got)
	}
	if len(notices) != 2 || notices[0].Status != InputCancelled || notices[0].RenderRef != nil || notices[1].Status != InputInterrupted || notices[1].RenderRef == nil || notices[1].RenderRef.CardID != "card-1" || !notices[1].RenderRef.CreatedAt.Equal(created) || !notices[1].RenderRef.SequenceUnknown || notices[1].RenderRef.PendingSequence != 2 {
		t.Fatalf("notices = %#v", notices)
	}
	persisted, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != 1 || len(persisted.Sessions) != 1 || len(persisted.Sessions[0].Queue) != 0 || persisted.Sessions[0].ActiveBatch != nil || persisted.Sessions[0].State != StateIdle {
		t.Fatalf("cleaned snapshot = %#v", persisted)
	}
}

func TestRestoreDebouncingInputIsCancelled(t *testing.T) {
	testRestoreSingleInputNotice(t, InputDebouncing, InputCancelled, false)
}

func TestRestoreQueuedInputIsCancelled(t *testing.T) {
	testRestoreSingleInputNotice(t, InputQueued, InputCancelled, false)
}

func TestRestoreStartingInputIsCancelled(t *testing.T) {
	testRestoreSingleInputNotice(t, InputStarting, InputCancelled, true)
}

func TestRestoreRunningInputIsInterrupted(t *testing.T) {
	testRestoreSingleInputNotice(t, InputRunning, InputInterrupted, true)
}

func TestRestoreMixedQueueAndActiveCreatesOneNoticePerNonTerminalInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := Key{Agent: agent.Claude, ChatID: "mixed"}
	seed := Session{
		Key: key,
		ID:  key.ID(),
		Queue: []Input{
			{ID: "debouncing", ReplyToMessageID: "m-debouncing", CardSessionID: "c-debouncing", State: InputDebouncing},
			{ID: "queued", ReplyToMessageID: "m-queued", CardSessionID: "c-queued", State: InputQueued},
			{ID: "completed", ReplyToMessageID: "m-completed", State: InputCompleted},
			{ID: "unknown", ReplyToMessageID: "m-unknown", State: InputState("unknown")},
		},
		ActiveBatch: &Batch{
			State:     InputQueued,
			RenderRef: &RenderRef{CardID: "active-card"},
			Inputs: []Input{
				{ID: "starting", ReplyToMessageID: "m-starting", CardSessionID: "c-starting", State: InputStarting},
				{ID: "running", ReplyToMessageID: "m-running", CardSessionID: "c-running", State: InputRunning},
				{ID: "fallback", ReplyToMessageID: "m-fallback", CardSessionID: "c-fallback"},
				{ID: "failed", ReplyToMessageID: "m-failed", State: InputFailed},
			},
		},
	}
	if err := SaveSnapshot(path, Snapshot{Sessions: []Session{seed}}); err != nil {
		t.Fatal(err)
	}

	notices, err := NewManagerWithStore(path).Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 5 {
		t.Fatalf("notice count = %d, notices = %#v", len(notices), notices)
	}
	want := []struct {
		reply  string
		status InputState
	}{
		{"m-debouncing", InputCancelled},
		{"m-queued", InputCancelled},
		{"m-starting", InputCancelled},
		{"m-running", InputInterrupted},
		{"m-fallback", InputCancelled},
	}
	for i, want := range want {
		if notices[i].ReplyToMessageID != want.reply || notices[i].Status != want.status {
			t.Fatalf("notice[%d] = %#v, want reply=%q status=%q", i, notices[i], want.reply, want.status)
		}
		if i < 2 && notices[i].RenderRef != nil {
			t.Fatalf("queue notice[%d] unexpectedly has render ref: %#v", i, notices[i].RenderRef)
		}
		if i >= 2 && (notices[i].RenderRef == nil || notices[i].RenderRef.CardID != "active-card") {
			t.Fatalf("active notice[%d] render ref = %#v", i, notices[i].RenderRef)
		}
	}
}

func TestRestoreFailureDoesNotPublishExistingManagerState(t *testing.T) {
	path := t.TempDir()
	key := Key{Agent: agent.Claude, ChatID: "existing"}
	m := NewManagerWithStore(path)
	m.sessions[key.ID()] = &Session{Key: key, ID: key.ID(), WorkDir: "/existing", State: StateRunning}
	m.revision = 7

	if _, err := m.Restore(); err == nil {
		t.Fatal("expected restore error")
	}
	got, ok := m.Get(key)
	if !ok || got.WorkDir != "/existing" || got.State != StateRunning || m.revision != 7 || m.lastPersistErr == nil {
		t.Fatalf("manager state was published after restore failure: session=%#v ok=%t revision=%d err=%v", got, ok, m.revision, m.lastPersistErr)
	}
}

func TestEnqueueDurableDoesNotPublishWhenSnapshotSaveFails(t *testing.T) {
	path := t.TempDir()
	key := Key{Agent: agent.Claude, ChatID: "enqueue"}
	m := NewManagerWithStore(path)

	if _, _, err := m.EnqueueDurable(key, Input{ID: "input", State: InputQueued}, "/w", BatchLimits{}); err == nil {
		t.Fatal("expected save error")
	}
	if _, ok := m.Get(key); ok || m.revision != 0 || m.lastPersistErr == nil {
		t.Fatalf("enqueue failure published state: exists=%t revision=%d err=%v", ok, m.revision, m.lastPersistErr)
	}
}

func TestFreezeReadyBatchDoesNotPublishWhenSnapshotSaveFails(t *testing.T) {
	path := t.TempDir()
	key := Key{Agent: agent.Claude, ChatID: "freeze"}
	input := Input{ID: "input", State: InputDebouncing, DebounceUntil: time.Unix(10, 0).Add(-time.Second)}
	m := NewManagerWithStore(path)
	m.sessions[key.ID()] = &Session{Key: key, ID: key.ID(), Queue: []Input{input}}
	m.revision = 7

	if _, _, err := m.FreezeReadyBatch(key, time.Unix(10, 0), BatchLimits{}); err == nil {
		t.Fatal("expected save error")
	}
	got, ok := m.Get(key)
	if !ok || len(got.Queue) != 1 || got.Queue[0].State != InputDebouncing || got.ActiveBatch != nil || m.revision != 7 || m.lastPersistErr == nil {
		t.Fatalf("freeze failure published state: session=%#v ok=%t revision=%d err=%v", got, ok, m.revision, m.lastPersistErr)
	}
}

func TestStoreAwareBatchLifecyclePersistsRunningAndTerminalState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewManagerWithStore(path)
	key := Key{Agent: agent.Claude, ChatID: "lifecycle"}
	now := time.Unix(10, 0)
	if _, _, err := m.EnqueueDurable(key, Input{ID: "input", Text: "hello", State: InputQueued, Time: now}, "/w", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	_, frozen, err := m.FreezeReadyBatch(key, now, BatchLimits{})
	if err != nil || frozen == nil {
		t.Fatalf("freeze batch=%#v err=%v", frozen, err)
	}
	created := time.Date(2026, 7, 18, 9, 30, 0, 0, time.UTC)
	if _, _, err := m.MarkBatchRunning(key, frozen.ID, &RenderRef{CardID: "card", CreatedAt: created, SequenceUnknown: true, PendingSequence: 2}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	running, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if running.Revision != 3 || len(running.Sessions) != 1 || running.Sessions[0].State != StateRunning || running.Sessions[0].ActiveBatch == nil || running.Sessions[0].ActiveBatch.State != InputRunning || running.Sessions[0].ActiveBatch.Inputs[0].State != InputRunning || running.Sessions[0].ActiveBatch.RenderRef == nil || running.Sessions[0].ActiveBatch.RenderRef.CardID != "card" || !running.Sessions[0].ActiveBatch.RenderRef.CreatedAt.Equal(created) || !running.Sessions[0].ActiveBatch.RenderRef.SequenceUnknown || running.Sessions[0].ActiveBatch.RenderRef.PendingSequence != 2 {
		t.Fatalf("running snapshot = %#v", running)
	}

	finishedAt := now.Add(2 * time.Second)
	if _, err := m.FinishBatch(key, frozen.ID, BatchCompletion{Status: InputCompleted, AgentSessionID: "session", Model: "model", Tokens: 5, At: finishedAt}); err != nil {
		t.Fatal(err)
	}
	finished, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Revision != 4 || len(finished.Sessions) != 1 || finished.Sessions[0].State != StateIdle || finished.Sessions[0].ActiveBatch != nil || finished.Sessions[0].AgentSessionID != "session" || finished.Sessions[0].Model != "model" || finished.Sessions[0].Tokens != 5 || !finished.Sessions[0].LastActive.Equal(finishedAt) {
		t.Fatalf("finished snapshot = %#v", finished)
	}
}

func TestStoreAwareLifecycleMutationsDoNotPublishWhenSnapshotSaveFails(t *testing.T) {
	key := Key{Agent: agent.Claude, ChatID: "rollback"}
	now := time.Unix(10, 0)
	t.Run("mark running", func(t *testing.T) {
		m := NewManagerWithStore(t.TempDir())
		m.sessions[key.ID()] = &Session{
			Key:            key,
			ID:             key.ID(),
			AgentSessionID: "old-session",
			Model:          "old-model",
			Tokens:         4,
			History:        []Prompt{{Text: "old"}},
			State:          StateIdle,
			Queue:          []Input{{ID: "later", State: InputQueued}},
			ActiveBatch:    &Batch{ID: "batch", State: InputStarting, Inputs: []Input{{ID: "active", Text: "new", State: InputStarting}}},
		}
		m.revision = 7

		sess, batch, err := m.MarkBatchRunning(key, "batch", &RenderRef{CardID: "card"}, now)
		if err == nil || batch != nil {
			t.Fatalf("mark result session=%#v batch=%#v err=%v", sess, batch, err)
		}
		if sess.State != StateIdle || sess.ActiveBatch == nil || sess.ActiveBatch.State != InputStarting || sess.ActiveBatch.RenderRef != nil || len(sess.Queue) != 1 || sess.AgentSessionID != "old-session" || len(sess.History) != 1 || m.revision != 7 || m.lastPersistErr == nil {
			t.Fatalf("mark failure published state: session=%#v revision=%d err=%v", sess, m.revision, m.lastPersistErr)
		}
	})
	t.Run("finish", func(t *testing.T) {
		m := NewManagerWithStore(t.TempDir())
		m.sessions[key.ID()] = &Session{
			Key:            key,
			ID:             key.ID(),
			AgentSessionID: "old-session",
			Model:          "old-model",
			Tokens:         4,
			History:        []Prompt{{Text: "old"}},
			State:          StateRunning,
			Queue:          []Input{{ID: "later", State: InputQueued}},
			ActiveBatch:    &Batch{ID: "batch", State: InputRunning, Inputs: []Input{{ID: "active", State: InputRunning}}},
		}
		m.revision = 7

		sess, err := m.FinishBatch(key, "batch", BatchCompletion{Status: InputCompleted, AgentSessionID: "new-session", Model: "new-model", Tokens: 3, At: now})
		if err == nil {
			t.Fatalf("finish result session=%#v err=%v", sess, err)
		}
		if sess.State != StateRunning || sess.ActiveBatch == nil || sess.ActiveBatch.State != InputRunning || len(sess.Queue) != 1 || sess.AgentSessionID != "old-session" || sess.Model != "old-model" || sess.Tokens != 4 || len(sess.History) != 1 || m.revision != 7 || m.lastPersistErr == nil {
			t.Fatalf("finish failure published state: session=%#v revision=%d err=%v", sess, m.revision, m.lastPersistErr)
		}
	})
	t.Run("queued cancellation", func(t *testing.T) {
		m := NewManagerWithStore(t.TempDir())
		m.sessions[key.ID()] = &Session{Key: key, ID: key.ID(), Queue: []Input{{ID: "input", ReplyToMessageID: "reply", State: InputQueued}}}
		m.revision = 7

		sess, removed, found, err := m.CancelQueuedInputByMessageID("reply")
		if err == nil || !found || removed.ID != "input" || removed.State != InputCancelled {
			t.Fatalf("cancel result session=%#v removed=%#v found=%t err=%v", sess, removed, found, err)
		}
		if len(sess.Queue) != 1 || sess.Queue[0].ID != "input" || sess.Queue[0].State != InputQueued || m.revision != 7 || m.lastPersistErr == nil {
			t.Fatalf("cancel failure published state: session=%#v revision=%d err=%v", sess, m.revision, m.lastPersistErr)
		}
	})
}

func TestStoreAwareDurableMutationsPersistSortedDeepCopiesAndAdvanceRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewManagerWithStore(path)
	keyB := Key{Agent: agent.Claude, ChatID: "b"}
	keyA := Key{Agent: agent.Claude, ChatID: "a"}
	now := time.Unix(10, 0)
	if _, _, err := m.EnqueueDurable(keyB, Input{ID: "b", Text: "b", State: InputQueued, Time: now}, "/b", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.EnqueueDurable(keyA, Input{ID: "a", Text: "a", State: InputQueued, Time: now}, "/a", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, batch, err := m.FreezeReadyBatch(keyA, now, BatchLimits{}); err != nil || batch == nil {
		t.Fatalf("freeze batch = %#v, err = %v", batch, err)
	}
	if m.revision != 3 || m.lastPersistErr != nil {
		t.Fatalf("manager revision=%d err=%v, want revision=3 without error", m.revision, m.lastPersistErr)
	}

	persisted, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != 3 || len(persisted.Sessions) != 2 || persisted.Sessions[0].ID != keyA.ID() || persisted.Sessions[1].ID != keyB.ID() {
		t.Fatalf("persisted snapshot = %#v", persisted)
	}
	persisted.Sessions[0].ActiveBatch.Inputs[0].Text = "mutated snapshot"
	got, ok := m.Get(keyA)
	if !ok || got.ActiveBatch == nil || got.ActiveBatch.Inputs[0].Text != "a" {
		t.Fatalf("snapshot mutation leaked into manager: %#v", got)
	}
}

func TestStoreAwarePersistenceSortsReceiptSnapshotWithoutMutatingManagerOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewManagerWithStore(path)
	source := []Receipt{
		{MessageID: "z-message", ExpiresAt: time.Unix(30, 0)},
		{MessageID: "a-message", ExpiresAt: time.Unix(20, 0)},
		{MessageID: "a-message", ExpiresAt: time.Unix(10, 0)},
	}
	m.receipts = source
	key := Key{Agent: agent.Claude, ChatID: "receipts"}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "input", State: InputQueued}, "/w", BatchLimits{}); err != nil {
		t.Fatal(err)
	}

	persisted, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Receipt{
		{MessageID: "a-message", ExpiresAt: time.Unix(10, 0)},
		{MessageID: "a-message", ExpiresAt: time.Unix(20, 0)},
		{MessageID: "z-message", ExpiresAt: time.Unix(30, 0)},
	}
	if len(persisted.Receipts) != len(want) {
		t.Fatalf("persisted receipts = %#v", persisted.Receipts)
	}
	for i := range want {
		if persisted.Receipts[i] != want[i] {
			t.Fatalf("persisted receipt[%d] = %#v, want %#v", i, persisted.Receipts[i], want[i])
		}
	}

	source[0].MessageID = "mutated-source"
	if m.receipts[0].MessageID != "z-message" {
		t.Fatalf("source mutation leaked into manager receipts: %#v", m.receipts)
	}
	reloaded, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Receipts[2].MessageID != "z-message" {
		t.Fatalf("source mutation leaked into persisted receipts: %#v", reloaded.Receipts)
	}
}

func TestRestoreMissingSnapshotPersistsAnEmptyCurrentSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewManagerWithStore(path)
	notices, err := m.Restore()
	if err != nil || len(notices) != 0 {
		t.Fatalf("restore notices=%#v err=%v", notices, err)
	}
	persisted, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.SchemaVersion != SnapshotVersion || persisted.Revision != 1 || len(persisted.Sessions) != 0 {
		t.Fatalf("persisted snapshot = %#v", persisted)
	}
}

func TestAcceptAndEnqueuePersistsReceiptAtomicallyAndRejectsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := Key{Agent: agent.Claude, ChatID: "dedup-restart"}
	now := time.Unix(10, 0).UTC()
	m := NewManagerWithStore(path)

	accepted, result, err := m.AcceptAndEnqueue(key, Input{ID: "m1", State: InputDebouncing}, now, 24*time.Hour, 10_000, BatchLimits{MaxPending: 20})
	if err != nil || !accepted || !result.Queued {
		t.Fatalf("first accept accepted=%t result=%#v err=%v", accepted, result, err)
	}
	persisted, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != 1 || len(persisted.Receipts) != 1 || persisted.Receipts[0].MessageID != "m1" || len(persisted.Sessions) != 1 || len(persisted.Sessions[0].Queue) != 1 {
		t.Fatalf("snapshot = %#v", persisted)
	}

	m2 := NewManagerWithStore(path)
	if _, err := m2.Restore(); err != nil {
		t.Fatal(err)
	}
	accepted, _, err = m2.AcceptAndEnqueue(key, Input{ID: "m1", State: InputDebouncing}, now.Add(time.Second), 24*time.Hour, 10_000, BatchLimits{MaxPending: 20})
	if err != nil || accepted {
		t.Fatalf("duplicate accepted=%t err=%v", accepted, err)
	}
}

func TestAcceptAndEnqueueDuplicateDoesNotAdvanceRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := Key{Agent: agent.Claude, ChatID: "dedup-revision"}
	now := time.Unix(10, 0).UTC()
	m := NewManagerWithStore(path)
	if accepted, _, err := m.AcceptAndEnqueue(key, Input{ID: "m1"}, now, time.Hour, 10, BatchLimits{}); err != nil || !accepted {
		t.Fatalf("first accept accepted=%t err=%v", accepted, err)
	}
	revision := m.revision
	if accepted, _, err := m.AcceptAndEnqueue(key, Input{ID: "m1"}, now.Add(time.Second), time.Hour, 10, BatchLimits{}); err != nil || accepted {
		t.Fatalf("duplicate accepted=%t err=%v", accepted, err)
	}
	if m.revision != revision {
		t.Fatalf("revision = %d, want %d", m.revision, revision)
	}
}

func TestAcceptAndEnqueueExpiredReceiptCanBeAcceptedAndReplaced(t *testing.T) {
	key := Key{Agent: agent.Claude, ChatID: "dedup-expired"}
	now := time.Unix(10, 0).UTC()
	m := NewManager()
	if accepted, _, err := m.AcceptAndEnqueue(key, Input{ID: "m1"}, now, time.Second, 10, BatchLimits{}); err != nil || !accepted {
		t.Fatalf("first accept accepted=%t err=%v", accepted, err)
	}
	later := now.Add(time.Second)
	if accepted, _, err := m.AcceptAndEnqueue(key, Input{ID: "m1"}, later, time.Hour, 10, BatchLimits{}); err != nil || !accepted {
		t.Fatalf("expired accept accepted=%t err=%v", accepted, err)
	}
	if len(m.receipts) != 1 || m.receipts[0].MessageID != "m1" || !m.receipts[0].ExpiresAt.Equal(later.Add(time.Hour)) {
		t.Fatalf("receipts = %#v", m.receipts)
	}
}

func TestAcceptMessagePersistsReceiptOnlyAndRejectsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewManagerWithStore(path)
	now := time.Unix(10, 0).UTC()
	if accepted, err := m.AcceptMessage("help-1", now, time.Hour, 10); err != nil || !accepted {
		t.Fatalf("first accept accepted=%t err=%v", accepted, err)
	}
	if len(m.sessions) != 0 || m.revision != 1 {
		t.Fatalf("receipt-only mutation changed sessions=%#v revision=%d", m.sessions, m.revision)
	}
	if accepted, err := m.AcceptMessage("help-1", now.Add(time.Second), time.Hour, 10); err != nil || accepted {
		t.Fatalf("duplicate accepted=%t err=%v", accepted, err)
	}
	if m.revision != 1 {
		t.Fatalf("duplicate revision = %d, want 1", m.revision)
	}
	persisted, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Sessions) != 0 || len(persisted.Receipts) != 1 || persisted.Receipts[0].MessageID != "help-1" {
		t.Fatalf("snapshot = %#v", persisted)
	}
}

func TestAcceptAndEnqueueQueueFullDoesNotRecordReceipt(t *testing.T) {
	key := Key{Agent: agent.Claude, ChatID: "dedup-full"}
	now := time.Unix(10, 0).UTC()
	m := NewManager()
	if accepted, _, err := m.AcceptAndEnqueue(key, Input{ID: "first"}, now, time.Hour, 10, BatchLimits{MaxPending: 1}); err != nil || !accepted {
		t.Fatalf("first accept accepted=%t err=%v", accepted, err)
	}
	if accepted, _, err := m.AcceptAndEnqueue(key, Input{ID: "full"}, now, time.Hour, 10, BatchLimits{MaxPending: 1}); err == nil || accepted {
		t.Fatalf("queue full accepted=%t err=%v", accepted, err)
	}
	for _, receipt := range m.receipts {
		if receipt.MessageID == "full" {
			t.Fatalf("queue-full message recorded a receipt: %#v", m.receipts)
		}
	}
}

func TestAcceptAndEnqueueSaveFailureDoesNotPublishReceiptOrInput(t *testing.T) {
	key := Key{Agent: agent.Claude, ChatID: "dedup-save-failure"}
	m := NewManagerWithStore(t.TempDir())
	if accepted, _, err := m.AcceptAndEnqueue(key, Input{ID: "m1"}, time.Unix(10, 0), time.Hour, 10, BatchLimits{}); err == nil || accepted {
		t.Fatalf("accepted=%t err=%v, want false persistence error", accepted, err)
	}
	if _, ok := m.Get(key); ok || len(m.receipts) != 0 || m.revision != 0 || m.lastPersistErr == nil {
		t.Fatalf("save failure published sessions=%#v receipts=%#v revision=%d err=%v", m.sessions, m.receipts, m.revision, m.lastPersistErr)
	}
}

func TestAcceptAndEnqueueExtendsCompatibleDebounceCohort(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "debounce-cohort"}
	now := time.Unix(10, 0).UTC()
	firstDeadline := now.Add(time.Second)
	sharedDeadline := now.Add(2 * time.Second)
	limits := BatchLimits{MaxPending: 10}

	for _, input := range []Input{
		{ID: "one", WorkDir: "/w", RequestedModel: "sonnet", RequestedEffort: "low", State: InputDebouncing, DebounceUntil: firstDeadline},
		{ID: "two", WorkDir: "/w", RequestedModel: "sonnet", RequestedEffort: "low", State: InputDebouncing, DebounceUntil: sharedDeadline},
	} {
		if accepted, _, err := m.AcceptAndEnqueue(key, input, now, 0, 0, limits); err != nil || !accepted {
			t.Fatalf("accept input %q: accepted=%t err=%v", input.ID, accepted, err)
		}
	}
	got, ok := m.Get(key)
	if !ok || len(got.Queue) != 2 || !got.Queue[0].DebounceUntil.Equal(sharedDeadline) {
		t.Fatalf("first debounce deadline = %s, want %s", got.Queue[0].DebounceUntil, sharedDeadline)
	}

	if _, batch, err := m.FreezeReadyBatch(key, firstDeadline, limits); err != nil || batch != nil {
		t.Fatalf("freeze before shared deadline: batch=%#v err=%v", batch, err)
	}
	_, batch, err := m.FreezeReadyBatch(key, sharedDeadline, limits)
	if err != nil || batch == nil {
		t.Fatalf("freeze at shared deadline: batch=%#v err=%v", batch, err)
	}
	if len(batch.Inputs) != 2 || batch.Inputs[0].ID != "one" || batch.Inputs[1].ID != "two" {
		t.Fatalf("batch inputs = %#v, want one then two", batch.Inputs)
	}
}

func TestAcceptAndEnqueueResetsEntireCompatibleDebounceTailInFIFOOrder(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "debounce-tail"}
	now := time.Unix(10, 0).UTC()
	limits := BatchLimits{MaxPending: 10}
	deadlines := []time.Time{now.Add(time.Second), now.Add(2 * time.Second), now.Add(3 * time.Second)}

	for i, deadline := range deadlines {
		input := Input{ID: string(rune('a' + i)), WorkDir: "/w", RequestedModel: "sonnet", RequestedEffort: "low", State: InputDebouncing, DebounceUntil: deadline}
		if accepted, _, err := m.AcceptAndEnqueue(key, input, now, 0, 0, limits); err != nil || !accepted {
			t.Fatalf("accept input %q: accepted=%t err=%v", input.ID, accepted, err)
		}
	}

	got, ok := m.Get(key)
	if !ok || len(got.Queue) != 3 {
		t.Fatalf("queue = %#v", got.Queue)
	}
	for i, input := range got.Queue {
		if input.ID != string(rune('a'+i)) || !input.DebounceUntil.Equal(deadlines[2]) {
			t.Fatalf("queue[%d] = %#v, want FIFO deadline %s", i, input, deadlines[2])
		}
	}
	if _, batch, err := m.FreezeReadyBatch(key, deadlines[2].Add(-time.Nanosecond), limits); err != nil || batch != nil {
		t.Fatalf("freeze before tail deadline: batch=%#v err=%v", batch, err)
	}
	_, batch, err := m.FreezeReadyBatch(key, deadlines[2], limits)
	if err != nil || batch == nil || len(batch.Inputs) != 3 {
		t.Fatalf("freeze at tail deadline: batch=%#v err=%v", batch, err)
	}
	for i, input := range batch.Inputs {
		if input.ID != string(rune('a'+i)) {
			t.Fatalf("batch input[%d] = %q, want %q", i, input.ID, string(rune('a'+i)))
		}
	}
}

func TestAcceptAndEnqueueDoesNotExtendAcrossDebounceBoundaries(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	oldDeadline := now.Add(time.Second)
	newDeadline := now.Add(2 * time.Second)
	for _, tc := range []struct {
		name      string
		preceding Input
		newest    Input
	}{
		{
			name:      "preceding reset",
			preceding: Input{Reset: true},
			newest:    Input{},
		},
		{
			name:      "new reset",
			preceding: Input{},
			newest:    Input{Reset: true},
		},
		{
			name:      "workdir",
			preceding: Input{WorkDir: "/other"},
			newest:    Input{WorkDir: "/w"},
		},
		{
			name:      "model",
			preceding: Input{RequestedModel: "opus"},
			newest:    Input{RequestedModel: "sonnet"},
		},
		{
			name:      "effort",
			preceding: Input{RequestedEffort: "high"},
			newest:    Input{RequestedEffort: "low"},
		},
		{
			name:      "non-debouncing",
			preceding: Input{State: InputQueued},
			newest:    Input{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			key := Key{Agent: agent.Claude, ChatID: tc.name}
			preceding := tc.preceding
			preceding.ID = "first"
			if preceding.State == "" {
				preceding.State = InputDebouncing
			}
			preceding.DebounceUntil = oldDeadline
			newest := tc.newest
			newest.ID = "second"
			newest.State = InputDebouncing
			newest.DebounceUntil = newDeadline

			for _, input := range []Input{preceding, newest} {
				if accepted, _, err := m.AcceptAndEnqueue(key, input, now, 0, 0, BatchLimits{}); err != nil || !accepted {
					t.Fatalf("accept input %q: accepted=%t err=%v", input.ID, accepted, err)
				}
			}
			got, ok := m.Get(key)
			if !ok || len(got.Queue) != 2 || !got.Queue[0].DebounceUntil.Equal(oldDeadline) {
				t.Fatalf("queue crossed %s boundary: %#v", tc.name, got.Queue)
			}
		})
	}
}

func TestAcceptAndEnqueueNeverShortensAnEarlierDebounceDeadline(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "debounce-out-of-order"}
	now := time.Unix(10, 0).UTC()
	laterDeadline := now.Add(2 * time.Second)
	earlierDeadline := now.Add(time.Second)
	for _, input := range []Input{
		{ID: "first", WorkDir: "/w", State: InputDebouncing, DebounceUntil: laterDeadline},
		{ID: "second", WorkDir: "/w", State: InputDebouncing, DebounceUntil: earlierDeadline},
	} {
		if accepted, _, err := m.AcceptAndEnqueue(key, input, now, 0, 0, BatchLimits{}); err != nil || !accepted {
			t.Fatalf("accept input %q: accepted=%t err=%v", input.ID, accepted, err)
		}
	}
	got, ok := m.Get(key)
	if !ok || !got.Queue[0].DebounceUntil.Equal(laterDeadline) {
		t.Fatalf("first deadline = %s, want %s", got.Queue[0].DebounceUntil, laterDeadline)
	}
	if _, batch, err := m.FreezeReadyBatch(key, earlierDeadline, BatchLimits{}); err != nil || batch != nil {
		t.Fatalf("freeze at earlier deadline: batch=%#v err=%v", batch, err)
	}
	_, batch, err := m.FreezeReadyBatch(key, laterDeadline, BatchLimits{})
	if err != nil || batch == nil || len(batch.Inputs) != 2 {
		t.Fatalf("freeze at later deadline: batch=%#v err=%v", batch, err)
	}
}

func TestAcceptAndEnqueueRejectedOrFailedCandidateDoesNotExtendDebounceDeadline(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	oldDeadline := now.Add(time.Second)
	newDeadline := now.Add(2 * time.Second)
	for _, tc := range []struct {
		name string
		run  func(*testing.T, *Manager, Key, Input)
	}{
		{
			name: "duplicate",
			run: func(t *testing.T, m *Manager, key Key, input Input) {
				t.Helper()
				if accepted, _, err := m.AcceptAndEnqueue(key, input, now, time.Hour, 10, BatchLimits{}); err != nil || !accepted {
					t.Fatalf("accept original: accepted=%t err=%v", accepted, err)
				}
				input.DebounceUntil = newDeadline
				if accepted, _, err := m.AcceptAndEnqueue(key, input, now, time.Hour, 10, BatchLimits{}); err != nil || accepted {
					t.Fatalf("accept duplicate: accepted=%t err=%v", accepted, err)
				}
			},
		},
		{
			name: "queue full",
			run: func(t *testing.T, m *Manager, key Key, input Input) {
				t.Helper()
				if accepted, _, err := m.AcceptAndEnqueue(key, input, now, 0, 0, BatchLimits{MaxPending: 1}); err != nil || !accepted {
					t.Fatalf("accept original: accepted=%t err=%v", accepted, err)
				}
				input.ID = "new"
				input.DebounceUntil = newDeadline
				if accepted, _, err := m.AcceptAndEnqueue(key, input, now, 0, 0, BatchLimits{MaxPending: 1}); err == nil || accepted {
					t.Fatalf("accept queue full: accepted=%t err=%v", accepted, err)
				}
			},
		},
		{
			name: "save failure",
			run: func(t *testing.T, m *Manager, key Key, input Input) {
				t.Helper()
				m.sessions[key.ID()] = &Session{Key: key, ID: key.ID(), WorkDir: input.WorkDir, Queue: []Input{input}}
				m.revision = 7
				input.ID = "new"
				input.DebounceUntil = newDeadline
				if accepted, _, err := m.AcceptAndEnqueue(key, input, now, 0, 0, BatchLimits{}); err == nil || accepted {
					t.Fatalf("accept persistence failure: accepted=%t err=%v", accepted, err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			if tc.name == "save failure" {
				m = NewManagerWithStore(t.TempDir())
			}
			key := Key{Agent: agent.Claude, ChatID: tc.name}
			input := Input{ID: "original", WorkDir: "/w", State: InputDebouncing, DebounceUntil: oldDeadline}
			tc.run(t, m, key, input)

			got, ok := m.Get(key)
			if !ok || len(got.Queue) != 1 || !got.Queue[0].DebounceUntil.Equal(oldDeadline) {
				t.Fatalf("live deadline changed: %#v", got.Queue)
			}
			want := uint64(0)
			if tc.name == "save failure" {
				want = 7
				if m.lastPersistErr == nil {
					t.Fatal("save failure did not record persistence error")
				}
			}
			if m.revision != want {
				t.Fatalf("revision = %d, want %d", m.revision, want)
			}
		})
	}
}

func TestAcceptMessagePrunesReceiptsLimitsEntriesAndIgnoresEmptyID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	now := time.Unix(10, 0).UTC()
	m := NewManagerWithStore(path)
	m.receipts = []Receipt{
		{MessageID: "expired", ExpiresAt: now},
		{MessageID: "a", ExpiresAt: now.Add(time.Hour)},
		{MessageID: "b", ExpiresAt: now.Add(time.Hour)},
	}
	if accepted, err := m.AcceptMessage("z", now, time.Hour, 2); err != nil || !accepted {
		t.Fatalf("accepted=%t err=%v", accepted, err)
	}
	if len(m.receipts) != 2 || m.receipts[0].MessageID != "b" || m.receipts[1].MessageID != "z" {
		t.Fatalf("receipts = %#v, want b then current z", m.receipts)
	}
	revision := m.revision
	if accepted, err := m.AcceptMessage("", now, time.Hour, 2); err != nil || !accepted {
		t.Fatalf("empty ID accepted=%t err=%v", accepted, err)
	}
	if m.revision != revision {
		t.Fatalf("empty ID revision = %d, want %d", m.revision, revision)
	}
}

func TestAcceptMessageConcurrentSameIDAcceptsExactlyOnce(t *testing.T) {
	m := NewManager()
	now := time.Unix(10, 0).UTC()
	const callers = 32
	accepted := make(chan bool, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := m.AcceptMessage("same", now, time.Hour, 10)
			if err != nil {
				t.Errorf("AcceptMessage: %v", err)
			}
			accepted <- ok
		}()
	}
	wg.Wait()
	close(accepted)
	count := 0
	for ok := range accepted {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("accepted count = %d, want 1", count)
	}
}

func testRestoreSingleInputNotice(t *testing.T, state, want InputState, active bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := Key{Agent: agent.Claude, ChatID: string(state)}
	seed := Session{Key: key, ID: key.ID()}
	input := Input{ID: "input", ReplyToMessageID: "message", CardSessionID: "card", State: state}
	if active {
		seed.ActiveBatch = &Batch{State: state, RenderRef: &RenderRef{CardID: "card-id"}, Inputs: []Input{input}}
	} else {
		seed.Queue = []Input{input}
	}
	if err := SaveSnapshot(path, Snapshot{Sessions: []Session{seed}}); err != nil {
		t.Fatal(err)
	}

	notices, err := NewManagerWithStore(path).Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0].Status != want || notices[0].ReplyToMessageID != "message" {
		t.Fatalf("notices = %#v", notices)
	}
	if active && notices[0].RenderRef == nil {
		t.Fatalf("active notice render ref = nil")
	}
	if !active && notices[0].RenderRef != nil {
		t.Fatalf("queue notice render ref = %#v", notices[0].RenderRef)
	}
}
