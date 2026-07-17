package bridge

import (
	"testing"
	"time"

	"lark-agent-bridge/internal/feishu"
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
