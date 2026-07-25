package bridge

import (
	"time"

	"lark-agent-bridge/internal/media"
)

type Message struct {
	ID             string
	ChatID         string
	ThreadID       string
	RootID         string
	ParentID       string
	Sender         string
	SenderType     string
	Text           string
	MessageType    string
	Attachments    []media.Ref
	IsGroup        bool
	HasAttachments bool
	Mentioned      bool
	MentionAll     bool
	Mentions       []Mention
	Time           time.Time
}

type Mention struct {
	OpenID string
	Name   string
	IsBot  bool
	IsAll  bool
}

type MessageRecall struct {
	MessageID  string
	ChatID     string
	RecallType string
	Time       time.Time
}

func (m Message) ShouldHandle() bool {
	if !m.IsGroup {
		return true
	}
	return m.Mentioned
}
