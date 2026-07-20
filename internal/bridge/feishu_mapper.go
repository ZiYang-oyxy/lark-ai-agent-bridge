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
	for _, mention := range in.Mentions {
		mentions = append(mentions, Mention{OpenID: mention.OpenID, Name: mention.Name, IsBot: mention.IsBot})
	}
	return Message{
		ID:             in.MessageID,
		ChatID:         in.ChatID,
		ThreadID:       in.TopicID,
		Sender:         in.SenderID,
		Text:           text,
		MessageType:    in.MessageType,
		Attachments:    append([]media.Ref(nil), in.Attachments...),
		IsGroup:        strings.EqualFold(in.ChatType, "group"),
		HasAttachments: len(in.Attachments) > 0,
		Mentioned:      in.MentionsBot,
		Mentions:       mentions,
		Time:           in.OccurredAt,
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
