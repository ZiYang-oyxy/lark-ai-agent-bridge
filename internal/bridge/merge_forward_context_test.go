package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/session"
)

func TestServiceAttachesSkippedMergeForwardToFollowingMention(t *testing.T) {
	now := time.Now()
	runner := newFakeRunner()
	recorder := audit.NewRecorder()
	svc := NewService(testConfig(t), card.NewFakeRenderer(), runner, recorder)
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "卡片里的最终方案"}}
	svc.MessageFetcher = fetcher

	forward := Message{
		ID: "forward", ChatID: "group", Sender: "user", MessageType: "merge_forward",
		Text: "Merged and Forwarded Message", IsGroup: true, Time: now,
	}
	if err := svc.HandleMessage(context.Background(), forward); err != nil {
		t.Fatal(err)
	}
	request := Message{
		ID: "request", ChatID: "group", Sender: "user", Text: "请 review 这个方案",
		IsGroup: true, Mentioned: true, Time: now.Add(time.Second),
	}
	if err := svc.HandleMessage(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	queued, ok := svc.Sessions.Get(session.Key{Agent: "claude", ChatID: "group"})
	if !ok || len(queued.Queue) != 1 {
		t.Fatalf("queued session = %#v", queued)
	}
	if got := queued.Queue[0].Text; !strings.Contains(got, "卡片里的最终方案") || !strings.Contains(got, "请 review 这个方案") {
		t.Fatalf("queued prompt = %q", got)
	}
	if len(fetcher.calls) != 1 || fetcher.calls[0] != "forward" {
		t.Fatalf("fetch calls = %v", fetcher.calls)
	}
	if !auditContainsAction(recorder.Events(), "merge_forward_context_attached") {
		t.Fatalf("audit = %#v", recorder.Events())
	}
}

func TestServiceDoesNotAttachSkippedMergeForwardToCommand(t *testing.T) {
	now := time.Now()
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "must not be fetched"}}
	svc.MessageFetcher = fetcher

	if err := svc.HandleMessage(context.Background(), Message{
		ID: "forward", ChatID: "group", Sender: "user", MessageType: "merge_forward", IsGroup: true, Time: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), Message{
		ID: "help", ChatID: "group", Sender: "user", Text: "/help", IsGroup: true, Mentioned: true, Time: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.calls) != 0 {
		t.Fatalf("fetch calls = %v, want none", fetcher.calls)
	}
	if events := renderer.Events(); len(events) != 1 || events[0].HelpCard == nil {
		t.Fatalf("events = %#v", events)
	}
}

func TestPendingMergeForwardRequiresSameSenderAndIsConsumedOnce(t *testing.T) {
	now := time.Now()
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "forwarded body"}}
	svc.MessageFetcher = fetcher
	svc.rememberSkippedMergeForward(Message{ID: "forward", ChatID: "group", Sender: "user-a", MessageType: "merge_forward", IsGroup: true, Time: now}, IntakeReasonMentionRequired)

	other := Message{ID: "other", ChatID: "group", Sender: "user-b", IsGroup: true, Mentioned: true, Time: now.Add(time.Second)}
	_, otherCmd := svc.attachPendingMergeForward(context.Background(), other, Command{Type: CommandRun, Text: "other request"})
	if otherCmd.Text != "other request" || len(fetcher.calls) != 0 {
		t.Fatalf("other command = %#v, calls=%v", otherCmd, fetcher.calls)
	}

	request := Message{ID: "request", ChatID: "group", Sender: "user-a", IsGroup: true, Mentioned: true, Time: now.Add(2 * time.Second)}
	_, first := svc.attachPendingMergeForward(context.Background(), request, Command{Type: CommandRun, Text: "read it"})
	_, second := svc.attachPendingMergeForward(context.Background(), request, Command{Type: CommandRun, Text: "read it again"})
	if !strings.Contains(first.Text, "forwarded body") || strings.Contains(second.Text, "forwarded body") {
		t.Fatalf("first=%q second=%q", first.Text, second.Text)
	}
	if len(fetcher.calls) != 1 {
		t.Fatalf("fetch calls = %v", fetcher.calls)
	}
}

func TestPendingForwardMatchingReplyParentIsRenderedOnlyAsQuote(t *testing.T) {
	now := time.Now()
	recorder := audit.NewRecorder()
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), recorder)
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{
		Text:        "测试失败!!!\n任务名称 RNIC\n[按钮:测试报告](https://reports.example.com/rnic)",
		MessageType: "interactive",
		SenderID:    "ou_reporter",
		SenderType:  "user",
	}}
	svc.MessageFetcher = fetcher
	svc.rememberSkippedMergeForward(Message{
		ID: "om_card", ChatID: "chat", Sender: "user", MessageType: "interactive", Time: now,
	}, IntakeReasonForwardMaterial)

	request := Message{
		ID: "om_request", ParentID: "om_card", ChatID: "chat", Sender: "user",
		Text: "告诉我卡片内容", MessageType: "text", Time: now.Add(time.Second),
	}
	if err := svc.HandleMessage(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	queued, ok := svc.Sessions.Get(session.Key{Agent: "claude", ChatID: "chat"})
	if !ok || len(queued.Queue) != 1 {
		t.Fatalf("queued session = %#v", queued)
	}
	prompt := BuildBatchPrompt(session.Batch{Inputs: []session.Input{queued.Queue[0]}})
	if count := strings.Count(prompt, "测试失败!!!"); count != 1 {
		t.Fatalf("source card occurrences = %d, want 1:\n%s", count, prompt)
	}
	if strings.Contains(prompt, "以下是用户刚刚转发/分享的内容") {
		t.Fatalf("same source was also rendered as forwarded material:\n%s", prompt)
	}
	if len(fetcher.calls) != 1 || fetcher.calls[0] != "om_card" {
		t.Fatalf("fetch calls = %v, want one quote fetch", fetcher.calls)
	}
	if !auditContainsAction(recorder.Events(), "merge_forward_context_deduplicated") {
		t.Fatalf("audit = %#v", recorder.Events())
	}
}
