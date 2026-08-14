package bridge

import (
	"context"
	"testing"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/media"
	"lark-agent-bridge/internal/session"
)

// isTopicSeedFirstRun 的 contract:仅当 key 非 root(Thread != "")且该 key 从未
// 记过 AgentSessionID 或历史时返回 true;这个 gate 决定 quote 模式是否要把引用
// 附件并入 seed。

func TestIsTopicSeedFirstRunTrueForFreshTopic(t *testing.T) {
	mgr := session.NewManager()
	topicKey := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "omt-1"}
	if !isTopicSeedFirstRun(mgr, topicKey) {
		t.Fatal("brand new topic key must be treated as first run")
	}
}

func TestIsTopicSeedFirstRunFalseForRootKey(t *testing.T) {
	mgr := session.NewManager()
	// Root key(Thread=="")永远不是"topic seed",quote 模式的附件并入分支不生效。
	rootKey := session.Key{Agent: agent.Claude, ChatID: "chat"}
	if isTopicSeedFirstRun(mgr, rootKey) {
		t.Fatal("root key must not be treated as topic-seed first run")
	}
}

func TestIsTopicSeedFirstRunFalseAfterFirstRun(t *testing.T) {
	mgr := session.NewManager()
	topicKey := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "omt-1"}
	mgr.GetOrCreate(topicKey, "")
	mgr.UpdateRunResult(topicKey.ID(), "topic-uuid", "", 0)
	if isTopicSeedFirstRun(mgr, topicKey) {
		t.Fatal("topic with minted agent session id is not first run any more")
	}
}

func TestIsTopicSeedFirstRunNilManager(t *testing.T) {
	topicKey := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "omt-1"}
	if isTopicSeedFirstRun(nil, topicKey) {
		t.Fatal("nil session manager must degrade to false, not panic")
	}
}

// resolveQuotedMessage 在 quote 模式下要把 FetchedMessage.Attachments 原样带出,
// 供 intake 决定是否把它们并入 msg.Attachments 一起下载。

type quoteAttachmentsFetcher struct {
	msg QuotedMessage
}

func (f *quoteAttachmentsFetcher) FetchMessage(_ context.Context, _ string) (QuotedMessage, error) {
	return f.msg, nil
}

func TestResolveQuotedMessageReturnsParentAttachments(t *testing.T) {
	attach := []media.Ref{{MessageID: "om_parent", FileKey: "img_key_1", Kind: "image"}}
	fetcher := &quoteAttachmentsFetcher{msg: QuotedMessage{Text: "看这张图", SenderID: "ou_a", MessageType: "text", Attachments: attach}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	_, _, _, _, gotAtt := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if len(gotAtt) != 1 || gotAtt[0].FileKey != "img_key_1" {
		t.Fatalf("attachments passthrough = %#v", gotAtt)
	}
}

func TestResolveQuotedMessageReturnsAttachmentsEvenWithEmptyText(t *testing.T) {
	// 纯图片消息:Text 为空,原逻辑返回占位符文本;这里同时要保留附件供 quote 模式抓下来。
	attach := []media.Ref{{MessageID: "om_parent", FileKey: "img_key_2", Kind: "image"}}
	fetcher := &quoteAttachmentsFetcher{msg: QuotedMessage{Text: "", MessageType: "image", Attachments: attach}}
	svc := &Service{Audit: audit.NewRecorder(), MessageFetcher: fetcher}
	text, _, _, _, gotAtt := svc.resolveQuotedMessage(context.Background(), Message{ParentID: "om_parent"})
	if text != "[image 消息]" {
		t.Fatalf("text placeholder = %q", text)
	}
	if len(gotAtt) != 1 || gotAtt[0].FileKey != "img_key_2" {
		t.Fatalf("image-only quote must still surface attachments: %#v", gotAtt)
	}
}
