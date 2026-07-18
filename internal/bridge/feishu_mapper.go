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
	return Message{
		ID:             in.MessageID,
		ChatID:         in.ChatID,
		ThreadID:       in.TopicID,
		Sender:         in.SenderID,
		Text:           text,
		Attachments:    append([]media.Ref(nil), in.Attachments...),
		IsGroup:        strings.EqualFold(in.ChatType, "group"),
		HasAttachments: len(in.Attachments) > 0,
		Mentioned:      in.MentionsBot,
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
