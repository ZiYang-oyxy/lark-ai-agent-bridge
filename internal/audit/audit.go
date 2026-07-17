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
	mu           sync.Mutex
	events       []Event
	writer       io.Writer
	writeErrs    int
	lastWriteErr error
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
		if err := json.NewEncoder(r.writer).Encode(event); err != nil {
			r.writeErrs++
			r.lastWriteErr = err
		}
	}
}

// WriteErrors reports the total number of audit writer failures observed by
// this recorder. A later successful write does not clear this history.
func (r *Recorder) WriteErrors() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeErrs
}

// LastWriteError reports the most recent audit writer failure, if any.
// A later successful write does not clear this error.
func (r *Recorder) LastWriteError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastWriteErr
}

func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]Event, len(r.events))
	copy(cp, r.events)
	return cp
}
