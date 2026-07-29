package bridge

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/media"
)

func TestMessageFromFeishuMapsGroupMention(t *testing.T) {
	now := time.Now()
	got := MessageFromFeishu(feishu.InboundMessage{
		MessageID:          "m1",
		ChatID:             "chat",
		TopicID:            "topic",
		SenderID:           "user",
		Text:               "@_user_1 hello",
		ChatType:           "group",
		MentionsBot:        true,
		ExplicitBotMention: true,
		Mentions:           []feishu.Mention{{Key: "@_user_1", OpenID: "bot"}},
		OccurredAt:         now,
	})
	if got.Text != "hello" {
		t.Fatalf("text = %q, want hello", got.Text)
	}
	if !got.IsGroup || !got.Mentioned || !got.ExplicitBotMention {
		t.Fatalf("group/mentioned/explicit = %v/%v/%v, want true/true/true", got.IsGroup, got.Mentioned, got.ExplicitBotMention)
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

func TestMessageFromFeishuCopiesMessageType(t *testing.T) {
	got := MessageFromFeishu(feishu.InboundMessage{MessageType: "interactive"})
	if got.MessageType != "interactive" {
		t.Fatalf("message type = %q, want interactive", got.MessageType)
	}
}

func TestMessageFromFeishuDropsDirectAttachmentResourceJSONFromText(t *testing.T) {
	for _, in := range []feishu.InboundMessage{
		{MessageType: "image", Text: `{"image_key":"img_123"}`, Attachments: []media.Ref{{MessageID: "m-image", FileKey: "img_123", Kind: "image"}}},
		{MessageType: "file", Text: `{"file_key":"file_123","file_name":"notes.txt"}`, Attachments: []media.Ref{{MessageID: "m-file", FileKey: "file_123", Kind: "file", Name: "notes.txt"}}},
		{MessageType: "image", Text: `{"image_key":`},
		{MessageType: "file", Text: `{"file_name":"notes.txt"}`},
	} {
		got := MessageFromFeishu(in)
		if got.Text != "" {
			t.Fatalf("message = %#v, want no direct attachment resource JSON", got)
		}
	}
}

func TestMessageFromFeishuNormalizesSenderType(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: "user", want: "user"},
		{raw: "app", want: "bot"},
		{raw: "bot", want: "bot"},
		{raw: "anonymous", want: ""},
	} {
		got := MessageFromFeishu(feishu.InboundMessage{SenderType: tc.raw})
		if got.SenderType != tc.want {
			t.Fatalf("sender type %q normalized to %q, want %q", tc.raw, got.SenderType, tc.want)
		}
	}
}

func TestMessageFromFeishuDistinguishesMentionAllFromBotMention(t *testing.T) {
	got := MessageFromFeishu(feishu.InboundMessage{
		MentionsBot: false,
		Mentions:    []feishu.Mention{{Key: "@_all", OpenID: "all", IsAll: true}},
	})
	if !got.MentionAll || got.Mentioned || len(got.Mentions) != 1 || !got.Mentions[0].IsAll {
		t.Fatalf("message = %#v", got)
	}
}

// TestMessageFromFeishuExpandsUserMentionPlaceholders 锁死身份展开契约：
// 飞书事件里的 `@_user_N` 是无语义占位符，agent 只看 Text 时完全无法识别
// mention 的对象。bridge 必须把它替换成携带 name/open_id 的可识别形式，
// 尤其是转发/引用消息里 agent 常常需要按被 @ 的人做后续动作。
func TestMessageFromFeishuExpandsUserMentionPlaceholders(t *testing.T) {
	for _, tc := range []struct {
		label   string
		text    string
		ments   []feishu.Mention
		wantSub []string
		wantNo  []string
	}{
		{
			label: "name and open_id",
			text:  "帮我 at @_user_2 讨论一下,@_user_3 也需要先 at",
			ments: []feishu.Mention{
				{Key: "@_user_2", OpenID: "ou_alice", Name: "Alice"},
				{Key: "@_user_3", OpenID: "ou_bob", Name: "Bob"},
			},
			wantSub: []string{"@Alice(ou_alice)", "@Bob(ou_bob)"},
			wantNo:  []string{"@_user_2", "@_user_3"},
		},
		{
			label: "open_id only fallback",
			text:  "找 @_user_5 确认",
			ments: []feishu.Mention{
				{Key: "@_user_5", OpenID: "ou_charlie"},
			},
			wantSub: []string{"@user(ou_charlie)"},
			wantNo:  []string{"@_user_5"},
		},
		{
			label: "bot mention keeps placeholder",
			text:  "@_user_1 帮我看下",
			ments: []feishu.Mention{
				{Key: "@_user_1", OpenID: "ou_bot", Name: "Bridge", IsBot: true},
			},
			wantSub: nil,
			wantNo:  []string{"@Bridge(ou_bot)"},
		},
		{
			label: "mention all keeps placeholder",
			text:  "各位 @_all 注意",
			ments: []feishu.Mention{
				{Key: "@_all", OpenID: "all", IsAll: true},
			},
			wantSub: []string{"@_all"},
			wantNo:  []string{"@all(all)"},
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := MessageFromFeishu(feishu.InboundMessage{Text: tc.text, Mentions: tc.ments, ChatType: "group"})
			for _, sub := range tc.wantSub {
				if !strings.Contains(got.Text, sub) {
					t.Fatalf("text = %q, want contains %q", got.Text, sub)
				}
			}
			for _, no := range tc.wantNo {
				if strings.Contains(got.Text, no) {
					t.Fatalf("text = %q, must not contain %q", got.Text, no)
				}
			}
		})
	}
}
