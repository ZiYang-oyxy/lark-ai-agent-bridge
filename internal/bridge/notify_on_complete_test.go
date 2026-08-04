package bridge

import (
	"context"
	"testing"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/session"
)

// fakeNotifier records the replies passed to SendReply so tests can assert what
// (if anything) the completion notification produced. It satisfies feishu.Sender.
type fakeNotifier struct {
	replies []feishu.Reply
}

func (f *fakeNotifier) SendReply(_ context.Context, reply feishu.Reply) (feishu.SendResult, error) {
	f.replies = append(f.replies, reply)
	return feishu.SendResult{MessageID: "om_notify"}, nil
}

func (f *fakeNotifier) AddReaction(context.Context, string, feishu.ReactionType) (string, error) {
	return "", nil
}

func (f *fakeNotifier) DeleteReaction(context.Context, string, string) error { return nil }

func (f *fakeNotifier) DeleteMessage(context.Context, string) error { return nil }

func newNotifyTestService(notifier feishu.Sender) *Service {
	svc := NewService(config.Config{}, card.NewFakeRenderer(), &fakeRunner{}, nil)
	svc.Notifier = notifier
	return svc
}

func notifyTestBatch(notify bool, mode config.ConversationMode, group bool) session.Batch {
	return session.Batch{Inputs: []session.Input{{
		Sender:           "ou_initiator",
		ReplyToMessageID: "om_origin",
		ConversationMode: mode,
		IsGroup:          group,
		NotifyOnComplete: notify,
	}}}
}

func TestNotifyOnCompleteDisabledSendsNothing(t *testing.T) {
	notifier := &fakeNotifier{}
	svc := newNotifyTestService(notifier)
	sess := session.Session{ID: "sess"}
	svc.notifyOnCompleteIfEnabled(context.Background(), sess, notifyTestBatch(false, config.ConversationModeChat, true), session.InputCompleted)
	if len(notifier.replies) != 0 {
		t.Fatalf("switch off should send nothing, got %#v", notifier.replies)
	}
}

func TestNotifyOnCompleteTopicMentionsInitiatorInThread(t *testing.T) {
	notifier := &fakeNotifier{}
	svc := newNotifyTestService(notifier)
	sess := session.Session{ID: "sess"}
	svc.notifyOnCompleteIfEnabled(context.Background(), sess, notifyTestBatch(true, config.ConversationModeTopic, true), session.InputCompleted)
	if len(notifier.replies) != 1 {
		t.Fatalf("expected exactly one reply, got %#v", notifier.replies)
	}
	got := notifier.replies[0]
	if !got.ShouldReply || got.ReplyToMessageID != "om_origin" || !got.ReplyInThread {
		t.Fatalf("reply routing = %#v", got)
	}
	if !got.MentionSender || got.MentionOpenID != "ou_initiator" {
		t.Fatalf("reply should @ initiator: %#v", got)
	}
	if got.Message == "" {
		t.Fatalf("reply message empty")
	}
	if !hasAuditAction(svc.Audit.Events(), "notify_on_complete_sent") {
		t.Fatalf("expected notify_on_complete_sent audit, got %#v", svc.Audit.Events())
	}
}

func TestCompletionStatusTextDefaultsAndIsCustomizable(t *testing.T) {
	for _, test := range []struct {
		name  string
		input session.Input
		want  string
	}{
		{name: "default", input: session.Input{}, want: config.DefaultCompletionStatusText},
		{name: "custom", input: session.Input{CompletionStatusText: "🎉 任务完成"}, want: "🎉 任务完成"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.input.EffectiveCompletionStatusText(); got != test.want {
				t.Fatalf("effective completion status text = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNotifyOnCompletePrivateChatDoesNotMention(t *testing.T) {
	notifier := &fakeNotifier{}
	svc := newNotifyTestService(notifier)
	sess := session.Session{ID: "sess"}
	// Private chat: IsGroup=false ⇒ no @ mention, but still a reply for the red dot.
	svc.notifyOnCompleteIfEnabled(context.Background(), sess, notifyTestBatch(true, config.ConversationModeChat, false), session.InputCompleted)
	if len(notifier.replies) != 1 {
		t.Fatalf("expected one reply, got %#v", notifier.replies)
	}
	got := notifier.replies[0]
	if got.MentionSender || got.MentionOpenID != "" {
		t.Fatalf("private chat must not mention: %#v", got)
	}
	if got.ReplyInThread {
		t.Fatalf("chat mode must not reply in thread: %#v", got)
	}
}

func TestNotifyOnCompleteSkipsNonCompletedStatuses(t *testing.T) {
	for _, status := range []session.InputState{session.InputCancelled, session.InputFailed} {
		notifier := &fakeNotifier{}
		svc := newNotifyTestService(notifier)
		svc.notifyOnCompleteIfEnabled(context.Background(), session.Session{ID: "sess"}, notifyTestBatch(true, config.ConversationModeTopic, true), status)
		if len(notifier.replies) != 0 {
			t.Fatalf("status %q should not notify, got %#v", status, notifier.replies)
		}
	}
}
