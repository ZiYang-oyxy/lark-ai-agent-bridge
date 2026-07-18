package bridge

import (
	"time"

	"lark-agent-bridge/internal/media"
)

type Message struct {
	ID             string
	ChatID         string
	ThreadID       string
	Sender         string
	Text           string
	Attachments    []media.Ref
	IsGroup        bool
	HasAttachments bool
	Mentioned      bool
	Time           time.Time
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
