package bridge

import "time"

type Message struct {
	ID        string
	ChatID    string
	ThreadID  string
	Sender    string
	Text      string
	IsGroup   bool
	Mentioned bool
	Time      time.Time
}

func (m Message) ShouldHandle() bool {
	if !m.IsGroup {
		return true
	}
	return m.Mentioned
}
