package feishueventlog

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

const SchemaVersion = 1

type Event struct {
	SchemaVersion int             `json:"schema_version"`
	Time          time.Time       `json:"time"`
	Transport     string          `json:"transport"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
}

type Recorder struct {
	mu     sync.Mutex
	writer io.Writer
	now    func() time.Time
}

func NewRecorder(writer io.Writer) *Recorder {
	return &Recorder{writer: writer, now: time.Now}
}

func (r *Recorder) Record(event Event) error {
	if r == nil || r.writer == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	event.SchemaVersion = SchemaVersion
	if event.Time.IsZero() {
		event.Time = r.now()
	}
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return json.NewEncoder(r.writer).Encode(event)
}
