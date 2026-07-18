package bridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/reply"
	"lark-agent-bridge/internal/session"
)

func TestNativeSequenceJournalPrepareConfirmAndReopen(t *testing.T) {
	dir := t.TempDir()
	sessions, key, batchID, original := activeJournalSession(t, filepath.Join(dir, "sessions.json"))
	replies, err := reply.OpenStore(filepath.Join(dir, "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := replies.SetLatest(key.ID(), &original); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "native-sequence-journal.json")
	journal, err := NewNativeSequenceJournal(path, sessions, replies)
	if err != nil {
		t.Fatal(err)
	}
	intent := feishu.NativeSequenceIntent{SessionID: key.ID(), BatchID: batchID, LatestScope: key.ID(), Ref: original, Candidate: 4}
	if err := journal.PrepareNative(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	active, _ := sessions.Get(key)
	if active.ActiveBatch == nil || active.ActiveBatch.RenderRef == nil || !active.ActiveBatch.RenderRef.SequenceUnknown || active.ActiveBatch.RenderRef.PendingSequence != 4 {
		t.Fatalf("active ref = %#v", active.ActiveBatch)
	}
	latest := replies.GetLatest(key.ID())
	if latest == nil || !latest.SequenceUnknown || latest.PendingSequence != 4 || !journal.RenderRefSequenceUnknown(original) {
		t.Fatalf("latest=%#v unknown=%t", latest, journal.RenderRefSequenceUnknown(original))
	}
	reopened, err := NewNativeSequenceJournal(path, sessions, replies)
	if err != nil || !reopened.RenderRefSequenceUnknown(original) {
		t.Fatalf("reopened unknown=%t err=%v", reopened != nil && reopened.RenderRefSequenceUnknown(original), err)
	}
	if err := journal.ConfirmNative(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	active, _ = sessions.Get(key)
	latest = replies.GetLatest(key.ID())
	if active.ActiveBatch.RenderRef.Version != 4 || active.ActiveBatch.RenderRef.SequenceUnknown || active.ActiveBatch.RenderRef.PendingSequence != 0 || latest.Version != 4 || journal.RenderRefSequenceUnknown(original) {
		t.Fatalf("confirmed active=%#v latest=%#v unknown=%t", active.ActiveBatch.RenderRef, latest, journal.RenderRefSequenceUnknown(original))
	}
}

func TestNativeSequenceJournalAbortRestoresOriginalRef(t *testing.T) {
	dir := t.TempDir()
	sessions, key, batchID, original := activeJournalSession(t, filepath.Join(dir, "sessions.json"))
	replies, err := reply.OpenStore(filepath.Join(dir, "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := replies.SetLatest(key.ID(), &original); err != nil {
		t.Fatal(err)
	}
	journal, err := NewNativeSequenceJournal(filepath.Join(dir, "journal.json"), sessions, replies)
	if err != nil {
		t.Fatal(err)
	}
	intent := feishu.NativeSequenceIntent{SessionID: key.ID(), BatchID: batchID, LatestScope: key.ID(), Ref: original, Candidate: 4}
	if err := journal.PrepareNative(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := journal.AbortNative(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	active, _ := sessions.Get(key)
	if got := active.ActiveBatch.RenderRef; got.Version != original.Version || got.SequenceUnknown || got.PendingSequence != 0 || !got.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("restored ref=%#v original=%#v", got, original)
	}
}

func TestNativeSequenceJournalDurableIntentWinsWhenSessionPublishFails(t *testing.T) {
	dir := t.TempDir()
	sessions := session.NewManagerWithStore(filepath.Join(dir, "sessions.json"))
	replies, err := reply.OpenStore(filepath.Join(dir, "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.json")
	journal, err := NewNativeSequenceJournal(path, sessions, replies)
	if err != nil {
		t.Fatal(err)
	}
	ref := session.RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 3}
	intent := feishu.NativeSequenceIntent{SessionID: "missing", BatchID: "batch", Ref: ref, Candidate: 4}
	if err := journal.PrepareNative(context.Background(), intent); err == nil {
		t.Fatal("PrepareNative() error=nil, want missing session")
	}
	if !journal.RenderRefSequenceUnknown(ref) {
		t.Fatal("durable intent did not keep ref unknown")
	}
	reopened, err := NewNativeSequenceJournal(path, sessions, replies)
	if err != nil || !reopened.RenderRefSequenceUnknown(ref) {
		t.Fatalf("reopened unknown=%t err=%v", reopened != nil && reopened.RenderRefSequenceUnknown(ref), err)
	}
}

func activeJournalSession(t *testing.T, path string) (*session.Manager, session.Key, string, session.RenderRef) {
	t.Helper()
	m := session.NewManagerWithStore(path)
	key := session.Key{Agent: agent.Claude, ChatID: "journal-chat"}
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if _, _, err := m.EnqueueDurable(key, session.Input{ID: "input", Text: "hello", Time: now}, "/tmp", session.BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	_, batch, err := m.FreezeReadyBatch(key, now, session.BatchLimits{})
	if err != nil || batch == nil {
		t.Fatalf("freeze batch=%#v err=%v", batch, err)
	}
	ref := session.RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 3, CreatedAt: now}
	if _, _, err := m.MarkBatchRunning(key, batch.ID, &ref, now); err != nil {
		t.Fatal(err)
	}
	return m, key, batch.ID, ref
}
