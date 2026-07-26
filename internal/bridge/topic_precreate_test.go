package bridge

import (
	"context"
	"errors"
	"testing"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
)

// recordingSender captures every SendReply call and every DeleteMessage call
// so tests can assert exactly what the precreate path did (nothing on the
// guard-off branches; a single seed reply + optional recall on the on branch).
// The programmable SendReply return value lets us cover both the success case
// and the "Feishu accepted the reply but did not attach a thread" degrade.
type recordingSender struct {
	feishu.NoopSender
	sendReplies       []feishu.Reply
	sendReplyResult   feishu.SendResult
	sendReplyErr      error
	deletedMessageIDs []string
	deleteMessageErr  error
}

func (r *recordingSender) SendReply(_ context.Context, reply feishu.Reply) (feishu.SendResult, error) {
	r.sendReplies = append(r.sendReplies, reply)
	return r.sendReplyResult, r.sendReplyErr
}

func (r *recordingSender) DeleteMessage(_ context.Context, messageID string) error {
	r.deletedMessageIDs = append(r.deletedMessageIDs, messageID)
	return r.deleteMessageErr
}

func TestShouldPrecreateTopicForPostMatrix(t *testing.T) {
	base := Message{
		ID:                 "om_root",
		ChatID:             "oc_test",
		MessageType:        "post",
		ExplicitBotMention: true,
	}
	cases := []struct {
		name  string
		mut   func(m *Message)
		mode  config.ConversationMode
		want  bool
	}{
		{"post_topic_explicit_no_thread", nil, config.ConversationModeTopic, true},
		{"chat_mode_skipped", nil, config.ConversationModeChat, false},
		{"not_explicit_mention_skipped", func(m *Message) { m.ExplicitBotMention = false }, config.ConversationModeTopic, false},
		{"already_in_thread_skipped", func(m *Message) { m.ThreadID = "omt_existing" }, config.ConversationModeTopic, false},
		{"text_msg_skipped", func(m *Message) { m.MessageType = "text" }, config.ConversationModeTopic, false},
		{"image_msg_skipped", func(m *Message) { m.MessageType = "image" }, config.ConversationModeTopic, false},
		{"empty_message_id_skipped", func(m *Message) { m.ID = "" }, config.ConversationModeTopic, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := base
			if tc.mut != nil {
				tc.mut(&msg)
			}
			if got := shouldPrecreateTopicForPost(msg, tc.mode); got != tc.want {
				t.Fatalf("shouldPrecreateTopicForPost = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrecreateTopicForPostBindsAliasAndRecallsProbe(t *testing.T) {
	sender := &recordingSender{
		sendReplyResult: feishu.SendResult{
			MessageID: "om_probe",
			ThreadID:  "omt_precreated",
			ChatID:    "oc_test",
		},
	}
	aliases := NewTopicAliasStore()
	rec := audit.NewRecorder()
	svc := &Service{Notifier: sender, TopicAliases: aliases, Audit: rec}

	msg := Message{ID: "om_root", ChatID: "oc_test", Sender: "ou_user", MessageType: "post", ExplicitBotMention: true}

	got := svc.precreateTopicForPost(context.Background(), msg)
	if got != "omt_precreated" {
		t.Fatalf("precreated thread = %q, want omt_precreated", got)
	}
	if len(sender.sendReplies) != 1 {
		t.Fatalf("SendReply calls = %d, want 1", len(sender.sendReplies))
	}
	probe := sender.sendReplies[0]
	if !probe.ReplyInThread {
		t.Fatalf("probe reply_in_thread = false, want true")
	}
	if probe.ReplyToMessageID != "om_root" {
		t.Fatalf("probe reply_to = %q, want om_root", probe.ReplyToMessageID)
	}
	if probe.Message != topicPrecreateProbeText {
		t.Fatalf("probe body = %q, want %q", probe.Message, topicPrecreateProbeText)
	}
	if probe.Kind != feishu.ReplyKindPlaceholder {
		t.Fatalf("probe kind = %q, want placeholder", probe.Kind)
	}
	if got, ok := aliases.Resolve("oc_test", "omt_precreated"); !ok || got != SyntheticTopicThreadPrefix+"om_root" {
		t.Fatalf("alias resolve = (%q, %v), want (%q, true)", got, ok, SyntheticTopicThreadPrefix+"om_root")
	}
	if len(sender.deletedMessageIDs) != 1 || sender.deletedMessageIDs[0] != "om_probe" {
		t.Fatalf("deleted probes = %v, want [om_probe]", sender.deletedMessageIDs)
	}
	if !hasAuditEvent(rec.Events(), "topic_precreate_ok") {
		t.Fatalf("audit missing topic_precreate_ok: %+v", rec.Events())
	}
}

func TestPrecreateTopicForPostSendFailureDegrades(t *testing.T) {
	sender := &recordingSender{sendReplyErr: errors.New("network reset")}
	aliases := NewTopicAliasStore()
	rec := audit.NewRecorder()
	svc := &Service{Notifier: sender, TopicAliases: aliases, Audit: rec}

	msg := Message{ID: "om_root", ChatID: "oc_test", Sender: "ou_user", MessageType: "post", ExplicitBotMention: true}
	got := svc.precreateTopicForPost(context.Background(), msg)
	if got != "" {
		t.Fatalf("precreated thread on error = %q, want empty", got)
	}
	if _, ok := aliases.Resolve("oc_test", "omt_precreated"); ok {
		t.Fatalf("alias must not be bound on failure")
	}
	if len(sender.deletedMessageIDs) != 0 {
		t.Fatalf("no probe was created, delete should not fire; got %v", sender.deletedMessageIDs)
	}
	if !hasAuditEvent(rec.Events(), "topic_precreate_failed") {
		t.Fatalf("audit missing topic_precreate_failed: %+v", rec.Events())
	}
}

func TestPrecreateTopicForPostEmptyThreadDegrades(t *testing.T) {
	sender := &recordingSender{
		sendReplyResult: feishu.SendResult{MessageID: "om_probe", ThreadID: "", ChatID: "oc_test"},
	}
	aliases := NewTopicAliasStore()
	rec := audit.NewRecorder()
	svc := &Service{Notifier: sender, TopicAliases: aliases, Audit: rec}

	msg := Message{ID: "om_root", ChatID: "oc_test", Sender: "ou_user", MessageType: "post", ExplicitBotMention: true}
	got := svc.precreateTopicForPost(context.Background(), msg)
	if got != "" {
		t.Fatalf("precreated thread with empty result = %q, want empty", got)
	}
	if _, ok := aliases.Resolve("oc_test", ""); ok {
		t.Fatalf("alias must not be bound with empty thread")
	}
	// The probe still exists on the chat side; the degrade path should try
	// to recall it so the user does not see a lingering seed message.
	if len(sender.deletedMessageIDs) != 1 || sender.deletedMessageIDs[0] != "om_probe" {
		t.Fatalf("expected best-effort probe recall on empty-thread degrade, got %v", sender.deletedMessageIDs)
	}
	if !hasAuditEvent(rec.Events(), "topic_precreate_failed") {
		t.Fatalf("audit missing topic_precreate_failed: %+v", rec.Events())
	}
}

func TestPrecreateTopicForPostProbeRecallFailureAudited(t *testing.T) {
	sender := &recordingSender{
		sendReplyResult:  feishu.SendResult{MessageID: "om_probe", ThreadID: "omt_precreated"},
		deleteMessageErr: errors.New("beyond recall window"),
	}
	aliases := NewTopicAliasStore()
	rec := audit.NewRecorder()
	svc := &Service{Notifier: sender, TopicAliases: aliases, Audit: rec}

	msg := Message{ID: "om_root", ChatID: "oc_test", Sender: "ou_user", MessageType: "post", ExplicitBotMention: true}
	if got := svc.precreateTopicForPost(context.Background(), msg); got != "omt_precreated" {
		t.Fatalf("thread = %q, want omt_precreated even if recall fails", got)
	}
	// Alias must still be bound — the topic is already open on Feishu side.
	if got, ok := aliases.Resolve("oc_test", "omt_precreated"); !ok || got != SyntheticTopicThreadPrefix+"om_root" {
		t.Fatalf("alias resolve on recall-fail = (%q, %v)", got, ok)
	}
	if !hasAuditEvent(rec.Events(), "topic_precreate_ok") {
		t.Fatalf("audit missing topic_precreate_ok on recall-fail: %+v", rec.Events())
	}
	if !hasAuditEvent(rec.Events(), "topic_precreate_probe_recall_failed") {
		t.Fatalf("audit missing topic_precreate_probe_recall_failed: %+v", rec.Events())
	}
}

func TestPrecreateTopicForPostWithoutNotifierIsNoop(t *testing.T) {
	svc := &Service{Audit: audit.NewRecorder()}
	msg := Message{ID: "om_root", ChatID: "oc_test", MessageType: "post", ExplicitBotMention: true}
	if got := svc.precreateTopicForPost(context.Background(), msg); got != "" {
		t.Fatalf("nil notifier should return empty, got %q", got)
	}
}

func hasAuditEvent(events []audit.Event, action string) bool {
	for _, e := range events {
		if e.Action == action {
			return true
		}
	}
	return false
}
