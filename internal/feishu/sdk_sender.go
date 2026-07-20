package feishu

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"regexp"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type ReplyAPI interface {
	Reply(ctx context.Context, req *larkim.ReplyMessageReq, options ...larkcore.RequestOptionFunc) (*larkim.ReplyMessageResp, error)
}

type MessageReactionAPI interface {
	Create(ctx context.Context, req *larkim.CreateMessageReactionReq, options ...larkcore.RequestOptionFunc) (*larkim.CreateMessageReactionResp, error)
	Delete(ctx context.Context, req *larkim.DeleteMessageReactionReq, options ...larkcore.RequestOptionFunc) (*larkim.DeleteMessageReactionResp, error)
}

type SDKSender struct {
	api         ReplyAPI
	markdownAPI MarkdownMessageAPI
	reactionAPI MessageReactionAPI
}

func NewSDKSender(appID, appSecret string) *SDKSender {
	client := lark.NewClient(appID, appSecret)
	return &SDKSender{
		api:         client.Im.V1.Message,
		markdownAPI: client.Im.V1.Message,
		reactionAPI: client.Im.V1.MessageReaction,
	}
}

func (s *SDKSender) SendReply(ctx context.Context, reply Reply) (SendResult, error) {
	if !reply.ShouldReply {
		return SendResult{}, nil
	}
	if reply.ReplyToMessageID == "" {
		return SendResult{}, fmt.Errorf("missing reply target message id")
	}
	content, err := buildTextMessageContent(reply)
	if err != nil {
		return SendResult{}, err
	}
	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType("text").
		Content(content).
		ReplyInThread(reply.ReplyInThread).
		Uuid(buildReplyUUID(reply)).
		Build()
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(reply.ReplyToMessageID).
		Body(body).
		Build()
	req.Body = body
	resp, err := s.api.Reply(ctx, req)
	if err != nil {
		return SendResult{}, fmt.Errorf("reply feishu message: %w", err)
	}
	if resp == nil {
		return SendResult{}, fmt.Errorf("reply feishu message failed: empty response")
	}
	if !resp.Success() {
		return SendResult{}, fmt.Errorf("reply feishu message failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	result := SendResult{}
	if resp.Data != nil {
		if resp.Data.MessageId != nil {
			result.MessageID = *resp.Data.MessageId
		}
		if resp.Data.ThreadId != nil {
			result.ThreadID = *resp.Data.ThreadId
		}
		if resp.Data.ChatId != nil {
			result.ChatID = *resp.Data.ChatId
		}
	}
	return result, nil
}

func (s *SDKSender) AddReaction(ctx context.Context, messageID string, reactionType ReactionType) (string, error) {
	if messageID == "" {
		return "", fmt.Errorf("missing reaction target message id")
	}
	if reactionType == "" {
		return "", fmt.Errorf("missing reaction type")
	}
	body := larkim.NewCreateMessageReactionReqBodyBuilder().
		ReactionType(larkim.NewEmojiBuilder().EmojiType(string(reactionType)).Build()).
		Build()
	apiReq := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(body).
		Build()
	apiReq.Body = body
	resp, err := s.reactionAPI.Create(ctx, apiReq)
	if err != nil {
		return "", fmt.Errorf("create feishu reaction: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("create feishu reaction failed: empty response")
	}
	if !resp.Success() {
		return "", fmt.Errorf("create feishu reaction failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data != nil && resp.Data.ReactionId != nil {
		return *resp.Data.ReactionId, nil
	}
	return "", fmt.Errorf("create feishu reaction returned empty reaction id")
}

func (s *SDKSender) DeleteReaction(ctx context.Context, messageID, reactionID string) error {
	if messageID == "" {
		return fmt.Errorf("missing reaction target message id")
	}
	if reactionID == "" {
		return fmt.Errorf("missing reaction id")
	}
	apiReq := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build()
	resp, err := s.reactionAPI.Delete(ctx, apiReq)
	if err != nil {
		return fmt.Errorf("delete feishu reaction: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("delete feishu reaction failed: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("delete feishu reaction failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func buildTextMessageContent(reply Reply) (string, error) {
	text := ""
	if reply.MentionSender && reply.MentionOpenID != "" {
		text += fmt.Sprintf("<at user_id=\"%s\"></at> ", reply.MentionOpenID)
	}
	text += replaceMentionPlaceholders(reply.Message, reply.Mentions)
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", fmt.Errorf("marshal feishu text content: %w", err)
	}
	return string(payload), nil
}

var mentionPlaceholderPattern = regexp.MustCompile(`@_user_\d+`)

func replaceMentionPlaceholders(text string, mentions []Mention) string {
	if text == "" || len(mentions) == 0 {
		return text
	}
	mentionOpenIDs := make(map[string]string, len(mentions))
	for _, mention := range mentions {
		if mention.Key == "" || mention.OpenID == "" {
			continue
		}
		mentionOpenIDs[mention.Key] = mention.OpenID
	}
	return mentionPlaceholderPattern.ReplaceAllStringFunc(text, func(token string) string {
		openID, ok := mentionOpenIDs[token]
		if !ok {
			return token
		}
		return fmt.Sprintf("<at user_id=\"%s\"></at>", openID)
	})
}

func buildReplyUUID(reply Reply) string {
	if reply.ReplyToMessageID == "" {
		return "00000000-0000-5000-8000-000000000000"
	}
	payload := fmt.Sprintf("%s:%s:%s", reply.Kind, reply.ReplyToMessageID, reply.Message)
	sum := sha1.Sum([]byte("feishu-reply:" + payload))
	var uuid [16]byte
	copy(uuid[:], sum[:16])
	uuid[6] = (uuid[6] & 0x0f) | 0x50
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		uuid[0], uuid[1], uuid[2], uuid[3],
		uuid[4], uuid[5],
		uuid[6], uuid[7],
		uuid[8], uuid[9],
		uuid[10], uuid[11], uuid[12], uuid[13], uuid[14], uuid[15],
	)
}
