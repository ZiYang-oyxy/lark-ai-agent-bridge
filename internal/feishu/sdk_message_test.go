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
