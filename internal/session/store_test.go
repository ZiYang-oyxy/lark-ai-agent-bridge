package session

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestRestoreKeepsContextButClearsPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := Key{Agent: agent.Claude, ChatID: "c"}
	seed := Session{
		Key:             key,
		ID:              key.ID(),
		WorkDir:         "/w",
		ClaudeSessionID: "s1",
		History:         []Prompt{{Text: "old"}},
		State:           StateRunning,
		Queue:           []Input{{ID: "q", State: InputQueued}},
		ActiveBatch: &Batch{
			ID:        "b",
			State:     InputRunning,
			RenderRef: &RenderRef{CardID: "card-1", Version: 3},
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
	if !ok || got.ClaudeSessionID != "s1" || len(got.History) != 1 || got.WorkDir != "/w" {
		t.Fatalf("context = %#v", got)
	}
	if len(got.Queue) != 0 || got.ActiveBatch != nil || got.State != StateIdle {
		t.Fatalf("pending survived: %#v", got)
	}
	if len(notices) != 2 || notices[0].Status != InputCancelled || notices[0].RenderRef != nil || notices[1].Status != InputInterrupted || notices[1].RenderRef == nil || notices[1].RenderRef.CardID != "card-1" {
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
