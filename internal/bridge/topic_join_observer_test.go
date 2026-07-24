package bridge

import (
	"errors"
	"testing"
	"time"

	"lark-agent-bridge/internal/audit"
)

type fakeTopicStore struct {
	has     map[string]bool
	marks   []topicMark
	touches []topicMark
	markErr error
}

type topicMark struct {
	ChatID   string
	ThreadID string
	At       time.Time
}

func (f *fakeTopicStore) Has(chatID, threadID string) bool {
	return f.has[chatID+"/"+threadID]
}

func (f *fakeTopicStore) Mark(chatID, threadID string, at time.Time) error {
	if f.markErr != nil {
		return f.markErr
	}
	if f.has == nil {
		f.has = map[string]bool{}
	}
	f.has[chatID+"/"+threadID] = true
	f.marks = append(f.marks, topicMark{ChatID: chatID, ThreadID: threadID, At: at})
	return nil
}

func (f *fakeTopicStore) Touch(chatID, threadID string, at time.Time) error {
	f.touches = append(f.touches, topicMark{ChatID: chatID, ThreadID: threadID, At: at})
	return nil
}

func TestTopicJoinObserverMarksAndAudits(t *testing.T) {
	rec := audit.NewRecorder()
	store := &fakeTopicStore{}
	obs := NewTopicJoinObserver(rec, store)

	at := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	obs.RecordCardReply("oc_chat", "omt_topic", "om_origin", "om_reply", at)

	if len(store.marks) != 1 {
		t.Fatalf("marks = %d, want 1", len(store.marks))
	}
	m := store.marks[0]
	if m.ChatID != "oc_chat" || m.ThreadID != "omt_topic" || !m.At.Equal(at) {
		t.Fatalf("mark = %+v", m)
	}
	events := rec.Events()
	if len(events) != 1 || events[0].Action != "topic_participation_auto_joined" {
		t.Fatalf("audit events = %+v, want single topic_participation_auto_joined", events)
	}
	if events[0].SessionID != "oc_chat" {
		t.Fatalf("audit SessionID = %q, want %q", events[0].SessionID, "oc_chat")
	}
}

func TestTopicJoinObserverSkipsWhenEitherIDEmpty(t *testing.T) {
	rec := audit.NewRecorder()
	store := &fakeTopicStore{}
	obs := NewTopicJoinObserver(rec, store)

	obs.RecordCardReply("", "omt_topic", "om_origin", "om_reply", time.Now())
	obs.RecordCardReply("oc_chat", "", "om_origin", "om_reply", time.Now())

	if len(store.marks) != 0 {
		t.Fatalf("expected no marks, got %d", len(store.marks))
	}
	if len(rec.Events()) != 0 {
		t.Fatalf("expected no audit events, got %d", len(rec.Events()))
	}
}

func TestTopicJoinObserverRecordFailureAudited(t *testing.T) {
	rec := audit.NewRecorder()
	store := &fakeTopicStore{markErr: errors.New("boom")}
	obs := NewTopicJoinObserver(rec, store)

	obs.RecordCardReply("oc_chat", "omt_topic", "om_origin", "om_reply", time.Now())

	if len(store.marks) != 0 {
		t.Fatalf("marks should not persist on failure, got %d", len(store.marks))
	}
	events := rec.Events()
	if len(events) != 1 || events[0].Action != "topic_participation_save_failed" {
		t.Fatalf("audit events = %+v, want topic_participation_save_failed", events)
	}
}

func TestTopicJoinObserverForwardsRecord(t *testing.T) {
	rec := audit.NewRecorder()
	obs := NewTopicJoinObserver(rec, nil)

	obs.Record("system", "sample_action", "sess-1", "detail")

	events := rec.Events()
	if len(events) != 1 || events[0].Action != "sample_action" || events[0].SessionID != "sess-1" {
		t.Fatalf("audit events = %+v", events)
	}
}
