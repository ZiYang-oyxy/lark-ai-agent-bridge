package feishu

import (
	"reflect"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"lark-agent-bridge/internal/media"
)

func TestParseMessageTextFromTextPayload(t *testing.T) {
	got := parseMessageText(`{"text":"hello"}`)
	if got != "hello" {
		t.Fatalf("text = %q, want hello", got)
	}
}

func TestParseMessageTextFromPostPayload(t *testing.T) {
	raw := `{"zh_cn":{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"bot"},{"tag":"text","text":" /new status"}]]}}`
	got := parseMessageText(raw)
	if got != "/new status" {
		t.Fatalf("text = %q, want command text", got)
	}
}

func TestParseDirectImageAttachment(t *testing.T) {
	got := parseMessageAttachments("m-image", "image", `{"image_key":"img_123"}`)
	want := []media.Ref{{MessageID: "m-image", FileKey: "img_123", Kind: "image"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachments = %#v, want %#v", got, want)
	}
}

func TestParseDirectFileAttachment(t *testing.T) {
	got := parseMessageAttachments("m-file", "file", `{"file_key":"file_123","file_name":"design.pdf"}`)
	want := []media.Ref{{MessageID: "m-file", FileKey: "file_123", Kind: "file", Name: "design.pdf"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachments = %#v, want %#v", got, want)
	}
}

func TestParsePostAttachmentsPreservesOrderAndDeduplicates(t *testing.T) {
	raw := `{"zh_cn":{"content":[[{"tag":"img","image_key":"img_1"},{"tag":"file","file_key":"file_1","file_name":"one.txt"}],[{"tag":"img","image_key":"img_1"},{"tag":"file","file_key":"file_2","file_name":"two.txt"}]]}}`
	got := parseMessageAttachments("m-post", "post", raw)
	want := []media.Ref{
		{MessageID: "m-post", FileKey: "img_1", Kind: "image"},
		{MessageID: "m-post", FileKey: "file_1", Kind: "file", Name: "one.txt"},
		{MessageID: "m-post", FileKey: "file_2", Kind: "file", Name: "two.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachments = %#v, want %#v", got, want)
	}
}

func TestParsePostAttachmentsSortsObjectKeys(t *testing.T) {
	raw := `{"zh_cn":{"content":[[{"tag":"file","file_key":"z","file_name":"z.txt"}]]},"en_us":{"content":[[{"tag":"img","image_key":"a"}]]}}`
	got := parseMessageAttachments("m-post", "post", raw)
	want := []media.Ref{
		{MessageID: "m-post", FileKey: "a", Kind: "image"},
		{MessageID: "m-post", FileKey: "z", Kind: "file", Name: "z.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachments = %#v, want %#v", got, want)
	}
}

func TestParseMessageAttachmentsInteractive(t *testing.T) {
	content := `{"elements":[[{"tag":"img","image_key":"img_1"},{"tag":"img","image_key":"img_1"}],[{"tag":"image","property":{"image_key":"img_2"}}]]}`
	got := parseMessageAttachments("m-card", "interactive", content)
	want := []media.Ref{
		{MessageID: "m-card", FileKey: "img_1", Kind: "image"},
		{MessageID: "m-card", FileKey: "img_2", Kind: "image"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachments = %#v, want %#v", got, want)
	}
}

func TestParseMessageAttachmentsInteractiveJSONCard(t *testing.T) {
	content := `{"json_card":"{\"body\":{\"elements\":[{\"tag\":\"img\",\"img_key\":\"img_nested\"}]}}"}`
	got := parseMessageAttachments("m-card", "interactive", content)
	want := []media.Ref{{MessageID: "m-card", FileKey: "img_nested", Kind: "image"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachments = %#v, want %#v", got, want)
	}
}

func TestParseRejectedAttachments(t *testing.T) {
	for _, messageType := range []string{"sticker", "audio", "video"} {
		t.Run(messageType, func(t *testing.T) {
			got := parseMessageAttachments("m-rejected", messageType, `{"file_key":"ignored","image_key":"ignored"}`)
			if len(got) != 0 {
				t.Fatalf("attachments = %#v, want none", got)
			}
		})
	}
}

func TestParseAttachmentsRejectsMalformedAndMissingKeys(t *testing.T) {
	for _, test := range []struct {
		name        string
		messageType string
		content     string
	}{
		{name: "malformed image", messageType: "image", content: `{"image_key":`},
		{name: "missing image key", messageType: "image", content: `{}`},
		{name: "missing file key", messageType: "file", content: `{"file_name":"name.txt"}`},
		{name: "missing post keys", messageType: "post", content: `{"zh_cn":{"content":[[{"tag":"img"},{"tag":"file","file_name":"name.txt"}]]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := parseMessageAttachments("m-invalid", test.messageType, test.content)
			if len(got) != 0 {
				t.Fatalf("attachments = %#v, want none", got)
			}
		})
	}
}

func TestBuildEventCopiesAttachments(t *testing.T) {
	msg := InboundMessage{Attachments: []media.Ref{{MessageID: "m1", FileKey: "file", Kind: "file", Name: "one.txt"}}}
	event := BuildEvent(msg)
	msg.Attachments[0].Name = "changed.txt"
	if got := event.Attachments[0].Name; got != "one.txt" {
		t.Fatalf("attachments were not copied: %#v", event.Attachments)
	}
}

func TestBuildInboundMessageParsesAttachments(t *testing.T) {
	messageID := "m-inbound"
	messageType := "file"
	content := `{"file_key":"file_123","file_name":"design.pdf"}`
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{Message: &larkim.EventMessage{
		MessageId:   &messageID,
		MessageType: &messageType,
		Content:     &content,
	}}}
	got := BuildInboundMessageFromLark(event, "")
	want := []media.Ref{{MessageID: "m-inbound", FileKey: "file_123", Kind: "file", Name: "design.pdf"}}
	if !reflect.DeepEqual(got.Attachments, want) {
		t.Fatalf("attachments = %#v, want %#v", got.Attachments, want)
	}
}

func TestBuildInboundMessagePreservesSenderTypeAndMentionAll(t *testing.T) {
	senderType := "app"
	allOpenID, allKey, allName := "all", "@_all", "所有人"
	botOpenID, botKey, botName := "ou_bot", "@_user_1", "Bridge"
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{SenderType: &senderType},
		Message: &larkim.EventMessage{Mentions: []*larkim.MentionEvent{
			{Key: &allKey, Id: &larkim.UserId{OpenId: &allOpenID}, Name: &allName},
			{Key: &botKey, Id: &larkim.UserId{OpenId: &botOpenID}, Name: &botName},
		}},
	}}
	got := BuildInboundMessageFromLark(event, botOpenID)
	if got.SenderType != "app" || !got.MentionsBot {
		t.Fatalf("inbound sender/mention = %q/%t", got.SenderType, got.MentionsBot)
	}
	if len(got.Mentions) != 2 || !got.Mentions[0].IsAll || got.Mentions[0].IsBot || !got.Mentions[1].IsBot {
		t.Fatalf("mentions = %#v", got.Mentions)
	}
}

func TestBuildInboundMessageDistinguishesExplicitAndImplicitBotMentions(t *testing.T) {
	botOpenID, botKey, botName := "ou_bot", "@_user_1", "Bridge"
	messageType := "text"
	mention := &larkim.MentionEvent{Key: &botKey, Id: &larkim.UserId{OpenId: &botOpenID}, Name: &botName}

	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{name: "visible at", content: `{"text":"@_user_1 hello"}`, want: true},
		{name: "metadata only", content: `{"text":"hello"}`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{Message: &larkim.EventMessage{
				MessageType: &messageType,
				Content:     &tc.content,
				Mentions:    []*larkim.MentionEvent{mention},
			}}}
			got := BuildInboundMessageFromLark(event, botOpenID)
			if !got.MentionsBot {
				t.Fatal("MentionsBot = false, want metadata preserved")
			}
			if got.ExplicitBotMention != tc.want {
				t.Fatalf("ExplicitBotMention = %v, want %v", got.ExplicitBotMention, tc.want)
			}
		})
	}
}

// Regression: Feishu post payloads carry the placeholder key (e.g. "@_user_1")
// in tag:at.user_id, NOT the real open_id. postContainsAtUser must match by
// Mention.Key, not OpenID; otherwise post-form @bot mentions read as implicit
// and topic mode silently downgrades to chat routing on every rich-text ping.
func TestBuildInboundMessageDetectsExplicitBotMentionInPost(t *testing.T) {
	botOpenID, botKey, botName := "ou_bot", "@_user_1", "Bridge"
	otherOpenID, otherKey, otherName := "ou_other", "@_user_2", "Alice"
	messageType := "post"

	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "top-level at bot via placeholder user_id",
			content: `{"zh_cn":{"content":[[{"tag":"at","user_id":"@_user_1","user_name":"Bridge"},{"tag":"text","text":" hello"}]]}}`,
			want:    true,
		},
		{
			name:    "nested at bot survives recursion",
			content: `{"zh_cn":{"title":"","content":[[{"tag":"text","text":"prefix"}],[{"tag":"at","user_id":"@_user_1","user_name":"Bridge"},{"tag":"text","text":" body"}]]}}`,
			want:    true,
		},
		{
			name:    "at another user only, bot mention metadata present but no visible at bot",
			content: `{"zh_cn":{"content":[[{"tag":"at","user_id":"@_user_2","user_name":"Alice"},{"tag":"text","text":" ping"}]]}}`,
			want:    false,
		},
		{
			name:    "no at node at all, only bot mention metadata (implicit)",
			content: `{"zh_cn":{"content":[[{"tag":"text","text":"just text"}]]}}`,
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.content
			event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{Message: &larkim.EventMessage{
				MessageType: &messageType,
				Content:     &content,
				Mentions: []*larkim.MentionEvent{
					{Key: &botKey, Id: &larkim.UserId{OpenId: &botOpenID}, Name: &botName},
					{Key: &otherKey, Id: &larkim.UserId{OpenId: &otherOpenID}, Name: &otherName},
				},
			}}}
			got := BuildInboundMessageFromLark(event, botOpenID)
			if !got.MentionsBot {
				t.Fatal("MentionsBot = false, want true (bot mention metadata attached)")
			}
			if got.ExplicitBotMention != tc.want {
				t.Fatalf("ExplicitBotMention = %v, want %v", got.ExplicitBotMention, tc.want)
			}
		})
	}
}
