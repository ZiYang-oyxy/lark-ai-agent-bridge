package bridge

import (
	"reflect"
	"testing"
	"time"

	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/media"
)

func TestMessageFromFeishuMapsGroupMention(t *testing.T) {
	now := time.Now()
	got := MessageFromFeishu(feishu.InboundMessage{
		MessageID:   "m1",
		ChatID:      "chat",
		TopicID:     "topic",
		SenderID:    "user",
		Text:        "@_user_1 hello",
		ChatType:    "group",
		MentionsBot: true,
		Mentions:    []feishu.Mention{{Key: "@_user_1", OpenID: "bot"}},
		OccurredAt:  now,
	})
	if got.Text != "hello" {
		t.Fatalf("text = %q, want hello", got.Text)
	}
	if !got.IsGroup || !got.Mentioned {
		t.Fatalf("group/mentioned = %v/%v, want true/true", got.IsGroup, got.Mentioned)
	}
	if got.ThreadID != "topic" {
		t.Fatalf("thread = %q", got.ThreadID)
	}
}

func TestMessageFromFeishuCopiesAttachments(t *testing.T) {
	in := feishu.InboundMessage{Attachments: []media.Ref{{MessageID: "m1", FileKey: "file", Kind: "file", Name: "one.txt"}}}
	got := MessageFromFeishu(in)
	want := []media.Ref{{MessageID: "m1", FileKey: "file", Kind: "file", Name: "one.txt"}}
	if !reflect.DeepEqual(got.Attachments, want) {
		t.Fatalf("attachments = %#v, want %#v", got.Attachments, want)
	}
	if !got.HasAttachments {
		t.Fatal("HasAttachments = false, want true")
	}
	in.Attachments[0].Name = "changed.txt"
	if got.Attachments[0].Name != "one.txt" {
		t.Fatalf("attachments were not copied: %#v", got.Attachments)
	}
}
