package feishu

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"lark-agent-bridge/internal/media"
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

	parsedMentions := buildMentions(mentions, botOpenID)
	return InboundMessage{
		AppID:              appID,
		ChatID:             chatID,
		MessageID:          messageID,
		RootID:             rootID,
		ParentID:           parentID,
		TopicID:            topicID,
		ChatType:           chatType,
		MessageType:        messageType,
		RawContent:         content,
		UserAgent:          userAgent,
		SenderID:           senderID,
		SenderType:         senderType,
		TenantKey:          tenantKey,
		Text:               parseMessageText(content),
		Attachments:        parseMessageAttachments(messageID, messageType, content),
		MentionsBot:        mentionsIncludeBot(parsedMentions, botOpenID),
		ExplicitBotMention: hasExplicitBotMention(messageType, content, parsedMentions),
		Mentions:           parsedMentions,
		OccurredAt:         parseCreateTime(createTime, eventHeader(event)),
	}
}

func parseMessageAttachments(messageID, messageType, content string) []media.Ref {
	if messageID == "" || content == "" {
		return nil
	}

	refs := make([]media.Ref, 0)
	seen := make(map[attachmentKey]struct{})
	appendRef := func(kind, fileKey, name string) {
		if fileKey == "" {
			return
		}
		key := attachmentKey{messageID: messageID, fileKey: fileKey, kind: kind}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		refs = append(refs, media.Ref{MessageID: messageID, FileKey: fileKey, Kind: kind, Name: name})
	}

	switch messageType {
	case "image":
		var payload struct {
			ImageKey string `json:"image_key"`
		}
		if json.Unmarshal([]byte(content), &payload) == nil {
			appendRef("image", payload.ImageKey, "")
		}
	case "file":
		var payload struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		if json.Unmarshal([]byte(content), &payload) == nil {
			appendRef("file", payload.FileKey, payload.FileName)
		}
	case "post":
		var payload any
		if json.Unmarshal([]byte(content), &payload) == nil {
			collectPostAttachmentRefs(payload, appendRef)
		}
	case "interactive":
		var payload any
		if json.Unmarshal([]byte(content), &payload) == nil {
			if envelope, ok := payload.(map[string]any); ok {
				if encoded, ok := envelope["json_card"].(string); ok && encoded != "" {
					var card any
					if json.Unmarshal([]byte(encoded), &card) == nil {
						payload = card
					}
				}
			}
			collectCardAttachmentRefs(payload, appendRef)
		}
	}

	return refs
}

func collectCardAttachmentRefs(value any, appendRef func(kind, fileKey, name string)) {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			collectCardAttachmentRefs(child, appendRef)
		}
	case map[string]any:
		tag := cardString(node, "tag")
		if tag == "img" || tag == "image" {
			appendRef("image", cardImageKey(node), "")
		}
		keys := make([]string, 0, len(node))
		for key := range node {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectCardAttachmentRefs(node[key], appendRef)
		}
	}
}

type attachmentKey struct {
	messageID string
	fileKey   string
	kind      string
}

func collectPostAttachmentRefs(v any, appendRef func(kind, fileKey, name string)) {
	switch typed := v.(type) {
	case []any:
		for _, child := range typed {
			collectPostAttachmentRefs(child, appendRef)
		}
	case map[string]any:
		tag, _ := typed["tag"].(string)
		switch tag {
		case "img":
			fileKey, _ := typed["image_key"].(string)
			appendRef("image", fileKey, "")
		case "file":
			fileKey, _ := typed["file_key"].(string)
			name, _ := typed["file_name"].(string)
			appendRef("file", fileKey, name)
		}

		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectPostAttachmentRefs(typed[key], appendRef)
		}
	}
}

func BuildRecalledMessageFromLark(event *larkim.P2MessageRecalledV1) RecalledMessage {
	if event == nil {
		return RecalledMessage{}
	}
	var appID string
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		appID = event.EventV2Base.Header.AppID
	}
	var messageID, chatID, recallTime, recallType string
	if event.Event != nil {
		messageID = stringValue(event.Event.MessageId)
		chatID = stringValue(event.Event.ChatId)
		recallTime = stringValue(event.Event.RecallTime)
		recallType = stringValue(event.Event.RecallType)
	}
	return RecalledMessage{
		AppID:      appID,
		ChatID:     chatID,
		MessageID:  messageID,
		RecallTime: recallTime,
		RecallType: recallType,
		OccurredAt: parseCreateTime(recallTime, recalledEventHeader(event)),
	}
}

func eventHeader(event *larkim.P2MessageReceiveV1) *larkevent.EventHeader {
	if event == nil || event.EventV2Base == nil {
		return nil
	}
	return event.EventV2Base.Header
}

func recalledEventHeader(event *larkim.P2MessageRecalledV1) *larkevent.EventHeader {
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

func buildMentions(mentions []*larkim.MentionEvent, botOpenID string) []Mention {
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
		result = append(result, Mention{
			Key: key, OpenID: openID, Name: stringValue(mention.Name),
			IsBot: openID == botOpenID,
			IsAll: openID == "all" || key == "@_all",
		})
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

func hasExplicitBotMention(messageType, content string, mentions []Mention) bool {
	botMentions := make([]Mention, 0, len(mentions))
	for _, mention := range mentions {
		if mention.IsBot {
			botMentions = append(botMentions, mention)
		}
	}
	if len(botMentions) == 0 {
		return false
	}

	switch messageType {
	case "text":
		text := parseMessageText(content)
		for _, mention := range botMentions {
			if mention.Key != "" && strings.Contains(text, mention.Key) {
				return true
			}
		}
	case "post":
		var payload any
		if json.Unmarshal([]byte(content), &payload) != nil {
			return false
		}
		// Feishu post payloads carry the placeholder key (e.g. "@_user_1") in
		// tag:at.user_id, not the real open_id. Match by Mention.Key to align
		// with the text branch above; using OpenID here silently fails on every
		// real post-form @bot and downgrades topic mode to chat routing.
		botKeys := make(map[string]struct{}, len(botMentions))
		for _, mention := range botMentions {
			if mention.Key != "" {
				botKeys[mention.Key] = struct{}{}
			}
		}
		return postContainsAtUser(payload, botKeys)
	}
	return false
}

func postContainsAtUser(v any, userKeys map[string]struct{}) bool {
	switch typed := v.(type) {
	case map[string]any:
		if tag, _ := typed["tag"].(string); tag == "at" {
			if userID, _ := typed["user_id"].(string); userID != "" {
				if _, ok := userKeys[userID]; ok {
					return true
				}
			}
		}
		for _, child := range typed {
			if postContainsAtUser(child, userKeys) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if postContainsAtUser(child, userKeys) {
				return true
			}
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
