package audit

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"lark-agent-bridge/internal/security"
)

type Event struct {
	Time      time.Time
	Actor     string
	Action    string
	SessionID string
	Detail    string
}

type Recorder struct {
	mu     sync.Mutex
	events []Event
	writer io.Writer
}

func NewRecorder() *Recorder {
	return &Recorder{}
}

func NewRecorderWithWriter(writer io.Writer) *Recorder {
	return &Recorder{writer: writer}
}

func (r *Recorder) Record(actor, action, sessionID, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	event := Event{
		Time:      time.Now(),
		Actor:     actor,
		Action:    action,
		SessionID: sessionID,
		Detail:    security.Redact(detail),
	}
	r.events = append(r.events, event)
	if r.writer != nil {
		_ = json.NewEncoder(r.writer).Encode(event)
	}
}

func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]Event, len(r.events))
	copy(cp, r.events)
	return cp
}
