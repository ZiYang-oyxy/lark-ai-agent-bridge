package bridge

import (
	"strings"

	"lark-agent-bridge/internal/feishu"
)

func MessageFromFeishu(in feishu.InboundMessage) Message {
	return Message{
		ID:        in.MessageID,
		ChatID:    in.ChatID,
		ThreadID:  in.TopicID,
		Sender:    in.SenderID,
		Text:      stripMentionPrefix(in.Text, in.Mentions),
		IsGroup:   strings.EqualFold(in.ChatType, "group"),
		Mentioned: in.MentionsBot,
		Time:      in.OccurredAt,
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
