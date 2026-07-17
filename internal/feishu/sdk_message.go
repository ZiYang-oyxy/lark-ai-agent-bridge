package feishu

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type textContent struct {
	Text string `json:"text"`
}

func BuildInboundMessageFromLark(event *larkim.P2MessageReceiveV1, botOpenID string) InboundMessage {
	if event == nil {
		return InboundMessage{}
	}

	var appID string
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		appID = event.EventV2Base.Header.AppID
	}

	var messageID, rootID, parentID, chatID, topicID, chatType, messageType, content, createTime, userAgent string
	var mentions []*larkim.MentionEvent
	if event.Event != nil && event.Event.Message != nil {
		messageID = stringValue(event.Event.Message.MessageId)
		rootID = stringValue(event.Event.Message.RootId)
		parentID = stringValue(event.Event.Message.ParentId)
		chatID = stringValue(event.Event.Message.ChatId)
		topicID = stringValue(event.Event.Message.ThreadId)
		chatType = stringValue(event.Event.Message.ChatType)
		messageType = stringValue(event.Event.Message.MessageType)
		content = stringValue(event.Event.Message.Content)
		mentions = event.Event.Message.Mentions
		createTime = stringValue(event.Event.Message.CreateTime)
		userAgent = stringValue(event.Event.Message.UserAgent)
	}

	var senderID, senderType, tenantKey string
	if event.Event != nil && event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		senderID = stringValue(event.Event.Sender.SenderId.OpenId)
	}
	if event.Event != nil && event.Event.Sender != nil {
		senderType = stringValue(event.Event.Sender.SenderType)
		tenantKey = stringValue(event.Event.Sender.TenantKey)
	}

	parsedMentions := buildMentions(mentions)
	return InboundMessage{
		AppID:       appID,
		ChatID:      chatID,
		MessageID:   messageID,
		RootID:      rootID,
		ParentID:    parentID,
		TopicID:     topicID,
		ChatType:    chatType,
		MessageType: messageType,
		RawContent:  content,
		UserAgent:   userAgent,
		SenderID:    senderID,
		SenderType:  senderType,
		TenantKey:   tenantKey,
		Text:        parseMessageText(content),
		MentionsBot: mentionsIncludeBot(parsedMentions, botOpenID),
		Mentions:    parsedMentions,
		OccurredAt:  parseCreateTime(createTime, eventHeader(event)),
	}
}

func eventHeader(event *larkim.P2MessageReceiveV1) *larkevent.EventHeader {
	if event == nil || event.EventV2Base == nil {
		return nil
	}
	return event.EventV2Base.Header
}

func parseMessageText(content string) string {
	if content == "" {
		return ""
	}
	var parsed textContent
	if err := json.Unmarshal([]byte(content), &parsed); err == nil && parsed.Text != "" {
		return parsed.Text
	}
	if text := parsePostMessageText(content); text != "" {
		return text
	}
	return strings.TrimSpace(content)
}

func parsePostMessageText(content string) string {
	var payload any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return ""
	}
	lines := collectPostTextLines(payload)
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func collectPostTextLines(v any) []string {
	switch typed := v.(type) {
	case map[string]any:
		if content, ok := typed["content"]; ok {
			return collectPostContentLines(content)
		}
		var out []string
		for _, child := range typed {
			out = append(out, collectPostTextLines(child)...)
		}
		return out
	case []any:
		var out []string
		for _, child := range typed {
			out = append(out, collectPostTextLines(child)...)
		}
		return out
	default:
		return nil
	}
}

func collectPostContentLines(v any) []string {
	rows, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		text := strings.TrimSpace(collectPostInlineText(row))
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func collectPostInlineText(v any) string {
	switch typed := v.(type) {
	case []any:
		var b strings.Builder
		for _, child := range typed {
			b.WriteString(collectPostInlineText(child))
		}
		return b.String()
	case map[string]any:
		tag, _ := typed["tag"].(string)
		switch tag {
		case "text", "md", "a":
			text, _ := typed["text"].(string)
			return text
		default:
			return ""
		}
	default:
		return ""
	}
}

func buildMentions(mentions []*larkim.MentionEvent) []Mention {
	result := make([]Mention, 0, len(mentions))
	for _, mention := range mentions {
		if mention == nil || mention.Id == nil {
			continue
		}
		openID := stringValue(mention.Id.OpenId)
		key := stringValue(mention.Key)
		if openID == "" || key == "" {
			continue
		}
		result = append(result, Mention{Key: key, OpenID: openID})
	}
	return result
}

func mentionsIncludeBot(mentions []Mention, botOpenID string) bool {
	if botOpenID == "" {
		return false
	}
	for _, mention := range mentions {
		if mention.OpenID == botOpenID {
			return true
		}
	}
	return false
}

func parseCreateTime(raw string, header *larkevent.EventHeader) time.Time {
	if raw == "" && header != nil {
		raw = header.CreateTime
	}
	if raw == "" {
		return time.Now()
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.UnixMilli(ms)
	}
	return time.Now()
}

func stringValue(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}
