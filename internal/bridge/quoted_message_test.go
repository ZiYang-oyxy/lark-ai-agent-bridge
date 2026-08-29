package bridge

import (
	"context"
	"errors"
	"testing"

	"lark-agent-bridge/internal/audit"
)

type fakeMessageFetcher struct {
	calls   []string
	msg     QuotedMessage
	err     error
	fetched bool
}

func (f *fakeMessageFetcher) FetchMessage(_ context.Context, messageID string) (QuotedMessage, error) {
	f.calls = append(f.calls, messageID)
	f.fetched = true
	return f.msg, f.err
}

func TestResolveQuotedMessageFetchesParent(t *testing.T) {
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "我是一只黑猫", SenderID: "ou_author", SenderName: "李俊", SenderType: "user", MessageType: "text"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	text, sender, senderName, senderType, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent", ChatID: "oc_x"})
	if text != "我是一只黑猫" {
		t.Fatalf("text = %q", text)
	}
	if sender != "ou_author" {
		t.Fatalf("sender = %q", sender)
	}
	if senderName != "李俊" {
		t.Fatalf("senderName = %q", senderName)
	}
	if senderType != "user" {
		t.Fatalf("senderType = %q, want user", senderType)
	}
	if len(fetcher.calls) != 1 || fetcher.calls[0] != "om_parent" {
		t.Fatalf("fetch called with %v", fetcher.calls)
	}
}

func TestResolveQuotedMessageNoParentSkips(t *testing.T) {
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "unused"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	text, sender, senderName, senderType, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: ""})
	if text != "" || sender != "" || senderName != "" || senderType != "" {
		t.Fatalf("expected empty, got text=%q sender=%q senderName=%q senderType=%q", text, sender, senderName, senderType)
	}
	if fetcher.fetched {
		t.Fatal("fetcher must not be called when there is no parent id")
	}
}

func TestResolveQuotedMessageNilFetcherSkips(t *testing.T) {
	svc := &Service{Audit: audit.NewRecorder()}
	text, sender, senderName, senderType, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if text != "" || sender != "" || senderName != "" || senderType != "" {
		t.Fatalf("expected empty when no fetcher, got text=%q sender=%q senderName=%q senderType=%q", text, sender, senderName, senderType)
	}
}

func TestResolveQuotedMessageFetchErrorDegrades(t *testing.T) {
	fetcher := &fakeMessageFetcher{err: errors.New("boom")}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	text, sender, senderName, senderType, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent", Sender: "ou_s", ChatID: "oc_x"})
	if text != "" || sender != "" || senderName != "" || senderType != "" {
		t.Fatalf("fetch failure must degrade to empty, got text=%q sender=%q senderName=%q senderType=%q", text, sender, senderName, senderType)
	}
}

func TestResolveQuotedMessageNonTextUsesPlaceholder(t *testing.T) {
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "", MessageType: "image", SenderType: "user"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	text, _, _, senderType, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if text != "[image 消息]" {
		t.Fatalf("expected type placeholder, got %q", text)
	}
	if senderType != "user" {
		t.Fatalf("senderType must pass through for non-text quote, got %q", senderType)
	}
}

// TestResolveQuotedMessageAppSenderIsMarkedApp 断言：当 fetched.SenderType == "app"
// 时（即引用的是别的 bot），resolveQuotedMessage 归一化为 "app"，供 prompt 层加
// 更严格的主体隔离提示。
func TestResolveQuotedMessageAppSenderIsMarkedApp(t *testing.T) {
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "review subagent 已重新启动", SenderID: "ou_other_bot", SenderType: "app", MessageType: "text"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher, BotOpenID: "ou_self"}
	_, _, _, senderType, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if senderType != "app" {
		t.Fatalf("senderType = %q, want app", senderType)
	}
}

// TestResolveQuotedMessageSelfBotOverridesSenderType 断言：即使 upstream SenderType
// 为 "app"/""，只要引用的发送者 open_id 等于本 bot 自己，就必须归一化为 "self_bot"
// —— 让 prompt 层知道这是自己的历史输出，而不是别的 bot 的话。
func TestResolveQuotedMessageSelfBotOverridesSenderType(t *testing.T) {
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "hi", SenderID: "ou_self", SenderType: "app", MessageType: "text"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher, BotOpenID: "ou_self"}
	_, _, _, senderType, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if senderType != "self_bot" {
		t.Fatalf("senderType = %q, want self_bot", senderType)
	}
}

// TestResolveQuotedMessageUnknownSenderTypeStaysEmpty 未知/anon/空的 upstream 值不
// 走 user/app 分支，保留空串走通用主体隔离提示，避免误标身份。
func TestResolveQuotedMessageUnknownSenderTypeStaysEmpty(t *testing.T) {
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "hi", SenderID: "ou_x", SenderType: "unknown", MessageType: "text"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	_, _, _, senderType, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if senderType != "" {
		t.Fatalf("unknown sender_type must degrade to empty, got %q", senderType)
	}
}

// TestResolveQuotedMessagePassesThroughLiteralSelfBot simulate/测试路径可以直接把
// upstream SenderType 传 "self_bot" 字面量，此时应原样保留——不必再依赖
// BotOpenID + open_id 比对。这条契约用来支持无 BotOpenID 上下文的 L2 断言。
func TestResolveQuotedMessagePassesThroughLiteralSelfBot(t *testing.T) {
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "hi", SenderID: "ou_x", SenderType: "self_bot", MessageType: "text"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	_, _, _, senderType, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if senderType != "self_bot" {
		t.Fatalf("literal self_bot must pass through, got %q", senderType)
	}
}
