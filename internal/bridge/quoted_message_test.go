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
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "我是一只黑猫", SenderID: "ou_author", MessageType: "text"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	text, sender := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent", ChatID: "oc_x"})
	if text != "我是一只黑猫" {
		t.Fatalf("text = %q", text)
	}
	if sender != "ou_author" {
		t.Fatalf("sender = %q", sender)
	}
	if len(fetcher.calls) != 1 || fetcher.calls[0] != "om_parent" {
		t.Fatalf("fetch called with %v", fetcher.calls)
	}
}

func TestResolveQuotedMessageNoParentSkips(t *testing.T) {
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "unused"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	text, sender := svc.resolveQuotedMessage(context.Background(), Message{ParentID: ""})
	if text != "" || sender != "" {
		t.Fatalf("expected empty, got text=%q sender=%q", text, sender)
	}
	if fetcher.fetched {
		t.Fatal("fetcher must not be called when there is no parent id")
	}
}

func TestResolveQuotedMessageNilFetcherSkips(t *testing.T) {
	svc := &Service{Audit: audit.NewRecorder()}
	text, sender := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if text != "" || sender != "" {
		t.Fatalf("expected empty when no fetcher, got text=%q sender=%q", text, sender)
	}
}

func TestResolveQuotedMessageFetchErrorDegrades(t *testing.T) {
	fetcher := &fakeMessageFetcher{err: errors.New("boom")}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	text, sender := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent", Sender: "ou_s", ChatID: "oc_x"})
	if text != "" || sender != "" {
		t.Fatalf("fetch failure must degrade to empty, got text=%q sender=%q", text, sender)
	}
}

func TestResolveQuotedMessageNonTextUsesPlaceholder(t *testing.T) {
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{Text: "", MessageType: "image"}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	text, _ := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if text != "[image 消息]" {
		t.Fatalf("expected type placeholder, got %q", text)
	}
}
