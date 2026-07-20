package feishu

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type captureReplyAPI struct {
	reqs        []*larkim.ReplyMessageReq
	resp        *larkim.ReplyMessageResp
	err         error
	nilResponse bool
}

func (f *captureReplyAPI) Reply(_ context.Context, req *larkim.ReplyMessageReq, _ ...larkcore.RequestOptionFunc) (*larkim.ReplyMessageResp, error) {
	f.reqs = append(f.reqs, req)
	if f.nilResponse {
		return nil, f.err
	}
	if f.resp == nil && f.err == nil {
		return &larkim.ReplyMessageResp{}, nil
	}
	return f.resp, f.err
}

type captureImageAPI struct {
	reqs []*larkim.CreateImageReq
	resp *larkim.CreateImageResp
	err  error
}

func (f *captureImageAPI) Create(_ context.Context, req *larkim.CreateImageReq, _ ...larkcore.RequestOptionFunc) (*larkim.CreateImageResp, error) {
	f.reqs = append(f.reqs, req)
	return f.resp, f.err
}

func TestSDKSenderForwardsReplyInThread(t *testing.T) {
	for _, want := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "topic"}[want], func(t *testing.T) {
			api := &captureReplyAPI{}
			sender := &SDKSender{api: api}
			if _, err := sender.SendReply(context.Background(), Reply{ReplyToMessageID: "message", Message: "hello", ShouldReply: true, ReplyInThread: want}); err != nil {
				t.Fatal(err)
			}
			if len(api.reqs) != 1 || api.reqs[0].Body == nil || api.reqs[0].Body.ReplyInThread == nil || *api.reqs[0].Body.ReplyInThread != want {
				t.Fatalf("reply request = %#v, want reply_in_thread=%t", api.reqs, want)
			}
		})
	}
}

func TestSDKSenderUploadsAndRepliesWithImage(t *testing.T) {
	imageKey := "img_123"
	messageID, threadID, chatID := "om_image", "omt_topic", "oc_chat"
	images := &captureImageAPI{resp: &larkim.CreateImageResp{Data: &larkim.CreateImageRespData{ImageKey: &imageKey}}}
	replies := &captureReplyAPI{resp: &larkim.ReplyMessageResp{Data: &larkim.ReplyMessageRespData{
		MessageId: &messageID, ThreadId: &threadID, ChatId: &chatID,
	}}}
	sender := &SDKSender{api: replies, imageAPI: images}

	key, err := sender.UploadImage(t.Context(), strings.NewReader("png-bytes"))
	if err != nil || key != imageKey {
		t.Fatalf("key=%q err=%v", key, err)
	}
	if len(images.reqs) != 1 || images.reqs[0].Body == nil || images.reqs[0].Body.ImageType == nil || *images.reqs[0].Body.ImageType != larkim.CreateImageImageTypeMessage {
		t.Fatalf("upload request=%#v", images.reqs)
	}
	uploaded, err := io.ReadAll(images.reqs[0].Body.Image)
	if err != nil || string(uploaded) != "png-bytes" {
		t.Fatalf("uploaded=%q err=%v", uploaded, err)
	}

	result, err := sender.ReplyImage(t.Context(), ImageReply{
		ReplyToMessageID: "om_source",
		ReplyInThread:    true,
		ImageKey:         key,
		UUID:             "stable-uuid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (SendResult{MessageID: messageID, ThreadID: threadID, ChatID: chatID}) {
		t.Fatalf("result=%#v", result)
	}
	if len(replies.reqs) != 1 || replies.reqs[0].Body == nil {
		t.Fatalf("reply requests=%#v", replies.reqs)
	}
	body := replies.reqs[0].Body
	if body.MsgType == nil || *body.MsgType != "image" || body.Content == nil || *body.Content != `{"image_key":"img_123"}` {
		t.Fatalf("reply body=%#v", body)
	}
	if body.ReplyInThread == nil || !*body.ReplyInThread || body.Uuid == nil || *body.Uuid != "stable-uuid" {
		t.Fatalf("reply routing=%#v", body)
	}
}

func TestSDKSenderImageErrorsAreSafe(t *testing.T) {
	secret := "img_secret"
	tests := []struct {
		name   string
		sender *SDKSender
		upload bool
	}{
		{name: "upload sdk", sender: &SDKSender{imageAPI: &captureImageAPI{err: errors.New("network failed")}}, upload: true},
		{name: "upload nil response", sender: &SDKSender{imageAPI: &captureImageAPI{}}, upload: true},
		{name: "upload business", sender: &SDKSender{imageAPI: &captureImageAPI{resp: &larkim.CreateImageResp{CodeError: larkcore.CodeError{Code: 999, Msg: "denied"}}}}, upload: true},
		{name: "upload empty key", sender: &SDKSender{imageAPI: &captureImageAPI{resp: &larkim.CreateImageResp{Data: &larkim.CreateImageRespData{}}}}, upload: true},
		{name: "reply sdk", sender: &SDKSender{api: &captureReplyAPI{err: errors.New("network failed")}}},
		{name: "reply nil response", sender: &SDKSender{api: &captureReplyAPI{nilResponse: true}}},
		{name: "reply business", sender: &SDKSender{api: &captureReplyAPI{resp: &larkim.ReplyMessageResp{CodeError: larkcore.CodeError{Code: 999, Msg: "denied"}}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.upload {
				_, err = tc.sender.UploadImage(t.Context(), strings.NewReader("private-image-content"))
			} else {
				_, err = tc.sender.ReplyImage(t.Context(), ImageReply{ReplyToMessageID: "om_source", ImageKey: secret})
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "private-image-content") {
				t.Fatalf("sensitive error=%q", err)
			}
		})
	}
}

func TestSDKSenderRejectsInvalidImageArguments(t *testing.T) {
	sender := &SDKSender{}
	if _, err := sender.UploadImage(t.Context(), nil); err == nil {
		t.Fatal("nil reader accepted")
	}
	for _, reply := range []ImageReply{{ImageKey: "img"}, {ReplyToMessageID: "source"}} {
		if _, err := sender.ReplyImage(t.Context(), reply); err == nil {
			t.Fatalf("invalid reply accepted: %#v", reply)
		}
	}
}
