package bridge

import (
	"strings"

	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/media"
)

func MessageFromFeishu(in feishu.InboundMessage) Message {
	text := stripMentionPrefix(in.Text, in.Mentions)
	text = ExpandMentionPlaceholders(text, in.Mentions)
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
		ID:                 in.MessageID,
		ChatID:             in.ChatID,
		ThreadID:           in.TopicID,
		RootID:             in.RootID,
		ParentID:           in.ParentID,
		Sender:             in.SenderID,
		SenderType:         normalizeSenderType(in.SenderType),
		Text:               text,
		MessageType:        in.MessageType,
		Attachments:        append([]media.Ref(nil), in.Attachments...),
		IsGroup:            strings.EqualFold(in.ChatType, "group"),
		HasAttachments:     len(in.Attachments) > 0,
		Mentioned:          in.MentionsBot,
		ExplicitBotMention: in.ExplicitBotMention,
		MentionAll:         mentionAll,
		Mentions:           mentions,
		Time:               in.OccurredAt,
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

// ExpandMentionPlaceholders 把飞书 SDK 事件里的 `@_user_N` 占位符替换成 agent 可
// 识别的身份标记。飞书事件用 `@_user_N` 作为正文占位、把真实身份放到同层的
// Mentions 数组里 (Key/OpenID/Name)。不展开的话 agent 只能看到 `@_user_2`,
// 完全不知道被 @ 的是谁——尤其是转发/引用消息里,用户经常需要 agent 「at 某人」
// 或者判断某句话是对谁说的。
//
// 展开格式:
//   - 有 Name + OpenID: `@Name(open_id)`
//   - 只有 OpenID:      `@user(open_id)`
//   - Bot / @所有人:     保持原样(bot 由 Mentioned 字段单独承载;@所有人的语义
//                        `@_all` 让 agent 阅读起来也一目了然)
//   - 其它异常:          保持原样,不破坏可读性
func ExpandMentionPlaceholders(text string, mentions []feishu.Mention) string {
	if text == "" || len(mentions) == 0 {
		return text
	}
	out := text
	for _, mention := range mentions {
		if mention.Key == "" || mention.IsBot || mention.IsAll {
			continue
		}
		replacement := formatMentionIdentity(mention)
		if replacement == "" {
			continue
		}
		out = strings.ReplaceAll(out, mention.Key, replacement)
	}
	return out
}

func formatMentionIdentity(mention feishu.Mention) string {
	name := strings.TrimSpace(mention.Name)
	openID := strings.TrimSpace(mention.OpenID)
	switch {
	case name != "" && openID != "":
		return "@" + name + "(" + openID + ")"
	case openID != "":
		return "@user(" + openID + ")"
	case name != "":
		return "@" + name
	default:
		return ""
	}
}
