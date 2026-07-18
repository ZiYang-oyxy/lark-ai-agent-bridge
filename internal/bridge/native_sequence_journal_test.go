package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/reply"
	"lark-agent-bridge/internal/session"
)

func TestNativeSequenceJournalPersistsPrepareAndConfirmInSafeOrder(t *testing.T) {
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
	var stages []string
	originalSave := journal.save
	originalReplace := journal.replaceActiveRef
	originalSetLatest := journal.setLatest
	journal.save = func(path string, intents map[string]feishu.NativeSequenceIntent) error {
		stage := "intent.sync"
		if len(intents) == 0 {
			stage = "intent.delete"
		}
		stages = append(stages, stage)
		return originalSave(path, intents)
	}
	journal.replaceActiveRef = func(sessionID, gotBatchID string, ref session.RenderRef) error {
		stages = append(stages, "session.sync")
		return originalReplace(sessionID, gotBatchID, ref)
	}
	journal.setLatest = func(scope string, ref *session.RenderRef) error {
		stages = append(stages, "reply.sync")
		return originalSetLatest(scope, ref)
	}
	intent := feishu.NativeSequenceIntent{SessionID: key.ID(), BatchID: batchID, LatestScope: key.ID(), Ref: original, Candidate: 4}
	if err := journal.PrepareNative(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if want := []string{"intent.sync", "session.sync", "reply.sync"}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("prepare stages = %#v, want %#v", stages, want)
	}
	stages = nil
	if err := journal.ConfirmNative(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if want := []string{"session.sync", "reply.sync", "intent.delete"}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("confirm stages = %#v, want %#v", stages, want)
	}
}

func TestNativeSequenceJournalPrepareFailuresRemainReopenSafe(t *testing.T) {
	for _, failure := range []string{"intent.sync", "session.sync", "reply.sync"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			sessions, key, batchID, original := activeJournalSession(t, filepath.Join(dir, "sessions.json"))
			replies, err := reply.OpenStore(filepath.Join(dir, "replies.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := replies.SetLatest(key.ID(), &original); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "journal.json")
			journal, err := NewNativeSequenceJournal(path, sessions, replies)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected " + failure)
			if failure == "intent.sync" {
				journal.save = func(string, map[string]feishu.NativeSequenceIntent) error { return injected }
			}
			if failure == "session.sync" {
				journal.replaceActiveRef = func(string, string, session.RenderRef) error { return injected }
			}
			if failure == "reply.sync" {
				journal.setLatest = func(string, *session.RenderRef) error { return injected }
			}
			intent := feishu.NativeSequenceIntent{SessionID: key.ID(), BatchID: batchID, LatestScope: key.ID(), Ref: original, Candidate: 4}
			if err := journal.PrepareNative(context.Background(), intent); !errors.Is(err, injected) {
				t.Fatalf("PrepareNative() error = %v, want %v", err, injected)
			}
			if failure == "intent.sync" {
				if journal.RenderRefSequenceUnknown(original) {
					t.Fatal("intent sync failure made original ref unknown")
				}
				return
			}
			reopened, err := NewNativeSequenceJournal(path, sessions, replies)
			if err != nil {
				t.Fatal(err)
			}
			if !reopened.RenderRefSequenceUnknown(original) {
				t.Fatal("durable prepare intent was not authoritative after reopen")
			}
		})
	}
}

func TestNativeSequenceJournalFinishFailuresRemainReopenSafe(t *testing.T) {
	for _, failure := range []string{"session.sync", "reply.sync", "intent.delete"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			sessions, key, batchID, original := activeJournalSession(t, filepath.Join(dir, "sessions.json"))
			replies, err := reply.OpenStore(filepath.Join(dir, "replies.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := replies.SetLatest(key.ID(), &original); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "journal.json")
			journal, err := NewNativeSequenceJournal(path, sessions, replies)
			if err != nil {
				t.Fatal(err)
			}
			intent := feishu.NativeSequenceIntent{SessionID: key.ID(), BatchID: batchID, LatestScope: key.ID(), Ref: original, Candidate: 4}
			if err := journal.PrepareNative(context.Background(), intent); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected " + failure)
			if failure == "session.sync" {
				journal.replaceActiveRef = func(string, string, session.RenderRef) error { return injected }
			}
			if failure == "reply.sync" {
				journal.setLatest = func(string, *session.RenderRef) error { return injected }
			}
			if failure == "intent.delete" {
				originalSave := journal.save
				journal.save = func(path string, intents map[string]feishu.NativeSequenceIntent) error {
					if len(intents) == 0 {
						return injected
					}
					return originalSave(path, intents)
				}
			}
			if err := journal.ConfirmNative(context.Background(), intent); !errors.Is(err, injected) {
				t.Fatalf("ConfirmNative() error = %v, want %v", err, injected)
			}
			reopened, err := NewNativeSequenceJournal(path, sessions, replies)
			if err != nil {
				t.Fatal(err)
			}
			if !reopened.RenderRefSequenceUnknown(original) {
				t.Fatal("durable intent was not authoritative after failed finish and reopen")
			}
		})
	}
}

func TestNativeSequenceJournalAbortDeleteFailureRemainsReopenSafe(t *testing.T) {
	dir := t.TempDir()
	sessions, key, batchID, original := activeJournalSession(t, filepath.Join(dir, "sessions.json"))
	replies, err := reply.OpenStore(filepath.Join(dir, "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := replies.SetLatest(key.ID(), &original); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.json")
	journal, err := NewNativeSequenceJournal(path, sessions, replies)
	if err != nil {
		t.Fatal(err)
	}
	intent := feishu.NativeSequenceIntent{SessionID: key.ID(), BatchID: batchID, LatestScope: key.ID(), Ref: original, Candidate: 4}
	if err := journal.PrepareNative(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected intent.delete")
	originalSave := journal.save
	journal.save = func(path string, intents map[string]feishu.NativeSequenceIntent) error {
		if len(intents) == 0 {
			return injected
		}
		return originalSave(path, intents)
	}
	if err := journal.AbortNative(context.Background(), intent); !errors.Is(err, injected) {
		t.Fatalf("AbortNative() error = %v, want %v", err, injected)
	}
	reopened, err := NewNativeSequenceJournal(path, sessions, replies)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.RenderRefSequenceUnknown(original) {
		t.Fatal("failed abort cleanup made original sequence reusable after reopen")
	}
}

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
