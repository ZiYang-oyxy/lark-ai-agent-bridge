package feishu

import (
	"context"
	"testing"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type captureReplyAPI struct {
	reqs []*larkim.ReplyMessageReq
}

func (f *captureReplyAPI) Reply(_ context.Context, req *larkim.ReplyMessageReq, _ ...larkcore.RequestOptionFunc) (*larkim.ReplyMessageResp, error) {
	f.reqs = append(f.reqs, req)
	return &larkim.ReplyMessageResp{}, nil
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
