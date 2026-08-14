package bridge

import (
	"errors"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
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
	obs.RecordCardReply("oc_chat", "omt_topic", "om_origin", "om_reply", SyntheticTopicThreadPrefix+"om_origin", at)

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

	obs.RecordCardReply("", "omt_topic", "om_origin", "om_reply", SyntheticTopicThreadPrefix+"om_origin", time.Now())
	obs.RecordCardReply("oc_chat", "", "om_origin", "om_reply", SyntheticTopicThreadPrefix+"om_origin", time.Now())

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

	obs.RecordCardReply("oc_chat", "omt_topic", "om_origin", "om_reply", SyntheticTopicThreadPrefix+"om_origin", time.Now())

	if len(store.marks) != 0 {
		t.Fatalf("marks should not persist on failure, got %d", len(store.marks))
	}
	events := rec.Events()
	if len(events) != 1 || events[0].Action != "topic_participation_save_failed" {
		t.Fatalf("audit events = %+v, want topic_participation_save_failed", events)
	}
}

// Regression: an in-topic follow-up G (its own reply id om_G) that was routed
// into the original @bot session A via the root_id fallback must bind the real
// thread_id to A's synthetic key (@bot:om_A), NOT to a per-reply key derived
// from G's id (@bot:om_G). Binding to @bot:om_G would split the session from
// the root_id-fallback path and reintroduce the "session frequently switches"
// bug. The correct alias value is the run's synthetic thread key, passed as
// syntheticThread.
func TestTopicJoinObserverBindsRunSyntheticThreadNotReplyID(t *testing.T) {
	rec := audit.NewRecorder()
	store := &fakeTopicStore{}
	aliases := NewTopicAliasStore()
	obs := NewTopicJoinObserver(rec, store)
	obs.Aliases = aliases

	// Follow-up G in topic omt_X: run was routed to A's synthetic session
	// @bot:om_A; the reply carries G's own message id as replyTo.
	obs.RecordCardReply("oc_chat", "omt_X", "om_G", "om_reply_G", SyntheticTopicThreadPrefix+"om_A", time.Now())

	got, ok := aliases.Resolve("oc_chat", "omt_X")
	if !ok {
		t.Fatalf("alias not bound for (oc_chat, omt_X)")
	}
	if got != SyntheticTopicThreadPrefix+"om_A" {
		t.Fatalf("alias = %q, want %q (A's synthetic key, not G-derived %q)", got, SyntheticTopicThreadPrefix+"om_A", SyntheticTopicThreadPrefix+"om_G")
	}

	// Invariant: when the synthetic session exists, the alias-hit path yields
	// the same key as the service-scoped root_id recovery path.
	followUp := Message{ID: "om_G2", ChatID: "oc_chat", ThreadID: "omt_X", RootID: "om_A", Sender: "u"}
	syntheticKey := session.Key{Agent: agent.Claude, ChatID: "oc_chat", Thread: SyntheticTopicThreadPrefix + "om_A"}
	sessions := session.NewManager()
	sessions.GetOrCreate(syntheticKey, "")
	svc := &Service{Sessions: sessions, TopicAliases: aliases}
	aliasKey := svc.keyForMessage(agent.Claude, followUp, config.ConversationModeTopic)
	svc.TopicAliases = nil
	rootIDKey := svc.keyForMessage(agent.Claude, followUp, config.ConversationModeTopic)
	if aliasKey != rootIDKey {
		t.Fatalf("alias path key %+v != root_id fallback key %+v; paths split", aliasKey, rootIDKey)
	}
	if aliasKey.Thread != SyntheticTopicThreadPrefix+"om_A" {
		t.Fatalf("converged key thread = %q, want %q", aliasKey.Thread, SyntheticTopicThreadPrefix+"om_A")
	}
}

// A real omt_* thread (not a synthetic @bot: key) must not be aliased to
// itself: syntheticThread lacks the prefix, so no bind happens.
func TestTopicJoinObserverSkipsBindForNonSyntheticThread(t *testing.T) {
	rec := audit.NewRecorder()
	store := &fakeTopicStore{}
	aliases := NewTopicAliasStore()
	obs := NewTopicJoinObserver(rec, store)
	obs.Aliases = aliases

	obs.RecordCardReply("oc_chat", "omt_X", "om_G", "om_reply_G", "omt_X", time.Now())

	if _, ok := aliases.Resolve("oc_chat", "omt_X"); ok {
		t.Fatalf("real thread should not be aliased to itself")
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
