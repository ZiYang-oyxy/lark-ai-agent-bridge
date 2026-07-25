package bridge

import (
	"strings"

	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/media"
)

func MessageFromFeishu(in feishu.InboundMessage) Message {
	text := stripMentionPrefix(in.Text, in.Mentions)
	if in.MessageType == "image" || in.MessageType == "file" {
		text = ""
	}
	mentions := make([]Mention, 0, len(in.Mentions))
	mentionAll := false
	for _, mention := range in.Mentions {
		mentions = append(mentions, Mention{OpenID: mention.OpenID, Name: mention.Name, IsBot: mention.IsBot, IsAll: mention.IsAll})
		mentionAll = mentionAll || mention.IsAll
	}
	return Message{
		ID:             in.MessageID,
		ChatID:         in.ChatID,
		ThreadID:       in.TopicID,
		RootID:         in.RootID,
		ParentID:       in.ParentID,
		Sender:         in.SenderID,
		SenderType:     normalizeSenderType(in.SenderType),
		Text:           text,
		MessageType:    in.MessageType,
		Attachments:    append([]media.Ref(nil), in.Attachments...),
		IsGroup:        strings.EqualFold(in.ChatType, "group"),
		HasAttachments: len(in.Attachments) > 0,
		Mentioned:      in.MentionsBot,
		MentionAll:     mentionAll,
		Mentions:       mentions,
		Time:           in.OccurredAt,
	}
}

func normalizeSenderType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "user":
		return "user"
	case "app", "bot":
		return "bot"
	default:
		return ""
	}
}

func stripMentionPrefix(text string, mentions []feishu.Mention) string {
	out := strings.TrimSpace(text)
	for _, mention := range mentions {
		if mention.Key == "" {
			continue
		}
		out = strings.TrimSpace(strings.TrimPrefix(out, mention.Key))
	}
	return out
}
