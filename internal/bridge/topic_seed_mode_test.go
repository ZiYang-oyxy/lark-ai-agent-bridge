package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
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

func TestIsTopicSeedFirstRunFalseAfterFirstInputQueued(t *testing.T) {
	mgr := session.NewManager()
	topicKey := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "omt-1"}
	_, _, err := mgr.AcceptAndEnqueue(topicKey, session.Input{
		ID: "om-first", Text: "first", State: session.InputQueued,
	}, time.Now(), time.Hour, 10, session.BatchLimits{MaxPending: 10})
	if err != nil {
		t.Fatal(err)
	}
	if isTopicSeedFirstRun(mgr, topicKey) {
		t.Fatal("topic with a queued first input must not accept a second seed")
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

func TestServiceTopicQuoteIsInjectedOnlyIntoFirstSeed(t *testing.T) {
	cfg := testConfig(t)
	cfg.TopicSeedMode = config.TopicSeedModeQuote
	runner := newFakeRunner()
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	svc.TopicAliases = NewTopicAliasStore()
	svc.TopicAliases.Bind("oc_topic", "omt_topic", SyntheticTopicThreadPrefix+"om_root")
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{
		Text: "root context", SenderID: "ou_author", SenderType: "user", MessageType: "text",
	}}
	svc.MessageFetcher = fetcher

	sendAndDrain := func(msg Message, wantCalls int) {
		t.Helper()
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		key := session.Key{Agent: agent.Claude, ChatID: msg.ChatID, Thread: SyntheticTopicThreadPrefix + "om_root"}
		sess, ok := svc.Sessions.Get(key)
		if !ok || len(sess.Queue) != 1 {
			t.Fatalf("queued session = %#v, want one input", sess)
		}
		if err := svc.DrainReady(sess.Queue[0].DebounceUntil); err != nil {
			t.Fatal(err)
		}
		waitForCalls(t, runner, wantCalls)
		waitForSessionNoActiveBatch(t, svc, key)
	}

	now := time.Now()
	sendAndDrain(Message{
		ID: "om_root", ChatID: "oc_topic", ThreadID: "omt_topic", ParentID: "om_quoted",
		Sender: "ou_user", Text: "first", ExplicitBotMention: true, Time: now,
	}, 1)
	sendAndDrain(Message{
		ID: "om_follow", ChatID: "oc_topic", ThreadID: "omt_topic", RootID: "om_root", ParentID: "om_root",
		Sender: "ou_user", Text: "second", Time: now.Add(time.Second),
	}, 2)

	calls := runner.Calls()
	if !strings.Contains(calls[0].Prompt, "[用户引用了 ou_author 的消息]") || !strings.Contains(calls[0].Prompt, "> root context") {
		t.Fatalf("first topic prompt must contain quote seed: %q", calls[0].Prompt)
	}
	if strings.Contains(calls[1].Prompt, "[用户引用了") || strings.Contains(calls[1].Prompt, "root context") || calls[1].Prompt != "second" {
		t.Fatalf("follow-up topic prompt must not repeat quote seed: %q", calls[1].Prompt)
	}
	if len(fetcher.calls) != 1 || fetcher.calls[0] != "om_quoted" {
		t.Fatalf("quoted message fetches = %v, want only first seed parent", fetcher.calls)
	}
}

func TestServiceChatModeKeepsPerMessageQuotes(t *testing.T) {
	cfg := testConfig(t)
	cfg.ConversationMode = config.ConversationModeChat
	runner := newFakeRunner()
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	fetcher := &fakeMessageFetcher{msg: QuotedMessage{
		Text: "explicit quote", SenderID: "ou_author", SenderType: "user", MessageType: "text",
	}}
	svc.MessageFetcher = fetcher

	now := time.Now()
	for i, msg := range []Message{
		{ID: "om_first", ChatID: "oc_chat", ParentID: "om_parent_1", Sender: "ou_user", Text: "first", Time: now},
		{ID: "om_second", ChatID: "oc_chat", ParentID: "om_parent_2", Sender: "ou_user", Text: "second", Time: now.Add(time.Second)},
	} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		key := session.Key{Agent: agent.Claude, ChatID: msg.ChatID}
		sess, ok := svc.Sessions.Get(key)
		if !ok || len(sess.Queue) != 1 {
			t.Fatalf("queued session = %#v, want one input", sess)
		}
		if err := svc.DrainReady(sess.Queue[0].DebounceUntil); err != nil {
			t.Fatal(err)
		}
		waitForCalls(t, runner, i+1)
		waitForSessionNoActiveBatch(t, svc, key)
	}

	for i, call := range runner.Calls() {
		if !strings.Contains(call.Prompt, "[用户引用了 ou_author 的消息]") || !strings.Contains(call.Prompt, "> explicit quote") {
			t.Fatalf("chat prompt %d lost explicit quote: %q", i+1, call.Prompt)
		}
	}
	if len(fetcher.calls) != 2 {
		t.Fatalf("chat quote fetches = %v, want one per explicit reply", fetcher.calls)
	}
}
