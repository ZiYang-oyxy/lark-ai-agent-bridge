package feishu

import (
	"time"

	"lark-agent-bridge/internal/media"
)

type EventKind string

const (
	EventKindMessageMention EventKind = "message_mention"
	EventKindTopicReply     EventKind = "topic_reply"
)

type Mention struct {
	Key    string
	OpenID string
}

type InboundMessage struct {
	AppID       string
	ChatID      string
	MessageID   string
	RootID      string
	ParentID    string
	TopicID     string
	ChatType    string
	MessageType string
	RawContent  string
	UserAgent   string
	SenderID    string
	SenderType  string
	TenantKey   string
	Text        string
	Attachments []media.Ref
	MentionsBot bool
	Mentions    []Mention
	OccurredAt  time.Time
}

type RecalledMessage struct {
	AppID      string
	ChatID     string
	MessageID  string
	RecallTime string
	RecallType string
	OccurredAt time.Time
}

type Event struct {
	Kind        EventKind
	AppID       string
	ChatID      string
	MessageID   string
	RootID      string
	ParentID    string
	TopicID     string
	ChatType    string
	MessageType string
	RawContent  string
	UserAgent   string
	SenderID    string
	SenderType  string
	TenantKey   string
	Text        string
	Attachments []media.Ref
	MentionsBot bool
	Mentions    []Mention
	OccurredAt  time.Time
}

func (e Event) IsTopicFollowup() bool {
	return e.TopicID != ""
}

func BuildEvent(msg InboundMessage) Event {
	occurredAt := msg.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	return Event{
		Kind:        inferEventKind(msg),
		AppID:       msg.AppID,
		ChatID:      msg.ChatID,
		MessageID:   msg.MessageID,
		RootID:      msg.RootID,
		ParentID:    msg.ParentID,
		TopicID:     msg.TopicID,
		ChatType:    msg.ChatType,
		MessageType: msg.MessageType,
		RawContent:  msg.RawContent,
		UserAgent:   msg.UserAgent,
		SenderID:    msg.SenderID,
		SenderType:  msg.SenderType,
		TenantKey:   msg.TenantKey,
		Text:        msg.Text,
		Attachments: append([]media.Ref(nil), msg.Attachments...),
		MentionsBot: msg.MentionsBot,
		Mentions:    append([]Mention(nil), msg.Mentions...),
		OccurredAt:  occurredAt,
	}
}

func inferEventKind(msg InboundMessage) EventKind {
	if msg.MentionsBot {
		return EventKindMessageMention
	}
	return EventKindTopicReply
}
