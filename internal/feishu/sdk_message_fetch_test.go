package feishu

import (
	"context"
	"errors"
	"testing"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type captureGetMessageAPI struct {
	reqs        []*larkim.GetMessageReq
	resp        *larkim.GetMessageResp
	err         error
	nilResponse bool
}

func (f *captureGetMessageAPI) Get(_ context.Context, req *larkim.GetMessageReq, _ ...larkcore.RequestOptionFunc) (*larkim.GetMessageResp, error) {
	f.reqs = append(f.reqs, req)
	if f.nilResponse {
		return nil, f.err
	}
	return f.resp, f.err
}

func getMessageResp(msgType, content, senderID string) *larkim.GetMessageResp {
	item := &larkim.Message{}
	if msgType != "" {
		mt := msgType
		item.MsgType = &mt
	}
	if content != "" {
		c := content
		item.Body = &larkim.MessageBody{Content: &c}
	}
	if senderID != "" {
		sid := senderID
		item.Sender = &larkim.Sender{Id: &sid}
	}
	return &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: []*larkim.Message{item}},
	}
}

func TestSDKSenderFetchMessageText(t *testing.T) {
	api := &captureGetMessageAPI{resp: getMessageResp("text", `{"text":"我是一只黑猫"}`, "ou_author")}
	sender := &SDKSender{getAPI: api}
	got, err := sender.FetchMessage(t.Context(), "om_parent")
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "我是一只黑猫" {
		t.Fatalf("text = %q", got.Text)
	}
	if got.SenderID != "ou_author" {
		t.Fatalf("sender = %q", got.SenderID)
	}
	if got.MessageType != "text" {
		t.Fatalf("type = %q", got.MessageType)
	}
	if len(api.reqs) != 1 {
		t.Fatalf("expected exactly one Get call, got %d", len(api.reqs))
	}
}

func TestSDKSenderFetchMessagePost(t *testing.T) {
	content := `{"title":"t","content":[[{"tag":"text","text":"line1"}],[{"tag":"text","text":"line2"}]]}`
	api := &captureGetMessageAPI{resp: getMessageResp("post", content, "ou_x")}
	sender := &SDKSender{getAPI: api}
	got, err := sender.FetchMessage(t.Context(), "om_parent")
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "line1\nline2" {
		t.Fatalf("post text = %q", got.Text)
	}
}

func TestSDKSenderFetchMessageEmptyID(t *testing.T) {
	sender := &SDKSender{getAPI: &captureGetMessageAPI{}}
	if _, err := sender.FetchMessage(t.Context(), ""); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestSDKSenderFetchMessageAPIError(t *testing.T) {
	api := &captureGetMessageAPI{nilResponse: true, err: errors.New("boom")}
	sender := &SDKSender{getAPI: api}
	if _, err := sender.FetchMessage(t.Context(), "om_parent"); err == nil {
		t.Fatal("expected error propagated from API")
	}
}

func TestSDKSenderFetchMessageNoItems(t *testing.T) {
	api := &captureGetMessageAPI{resp: &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: nil},
	}}
	sender := &SDKSender{getAPI: api}
	if _, err := sender.FetchMessage(t.Context(), "om_parent"); err == nil {
		t.Fatal("expected error when no items returned")
	}
}
