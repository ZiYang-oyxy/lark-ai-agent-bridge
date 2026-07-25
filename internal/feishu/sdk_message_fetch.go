package feishu

import (
	"context"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// FetchedMessage carries the minimal fields bridge needs to inline a quoted
// (replied-to) message into an agent prompt.
type FetchedMessage struct {
	MessageID   string
	MessageType string
	Text        string
	SenderID    string
}

// GetMessageAPI is the subset of the Feishu im.v1 message API used to fetch a
// single message by id. Extracted as an interface so it can be faked in tests.
type GetMessageAPI interface {
	Get(ctx context.Context, req *larkim.GetMessageReq, options ...larkcore.RequestOptionFunc) (*larkim.GetMessageResp, error)
}

// FetchMessage retrieves a single message by id and extracts its plain-text
// body. Non-text message types (image/file/etc.) yield an empty Text; callers
// decide how to represent them. Returns an error only when the API call itself
// fails or the message is missing, so callers can degrade gracefully.
func (s *SDKSender) FetchMessage(ctx context.Context, messageID string) (FetchedMessage, error) {
	if messageID == "" {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message: missing message id")
	}
	if s == nil || s.getAPI == nil {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message: message API unavailable")
	}
	req := larkim.NewGetMessageReqBuilder().MessageId(messageID).Build()
	resp, err := s.getAPI.Get(ctx, req)
	if err != nil {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message: %w", err)
	}
	if resp == nil {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message failed: empty response")
	}
	if !resp.Success() {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message failed: no items for id=%s", messageID)
	}
	item := resp.Data.Items[0]
	out := FetchedMessage{MessageID: messageID}
	if item.MsgType != nil {
		out.MessageType = *item.MsgType
	}
	if item.Body != nil && item.Body.Content != nil {
		out.Text = parseMessageText(*item.Body.Content)
	}
	if item.Sender != nil && item.Sender.Id != nil {
		out.SenderID = *item.Sender.Id
	}
	return out, nil
}
