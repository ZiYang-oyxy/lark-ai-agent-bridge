package feishueventlog

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecorderWritesRawPayloadAsJSON(t *testing.T) {
	var buf bytes.Buffer
	recorder := NewRecorder(&buf)
	recorder.now = func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) }
	payload := []byte(`{"schema":"2.0","event":{"message":{"content":"{\"text\":\"hello\"}"}}}`)
	if err := recorder.Record(Event{Transport: "long_connection", EventType: "im.message.receive_v1", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion || got.Transport != "long_connection" || got.EventType != "im.message.receive_v1" || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("event = %#v", got)
	}
}

func TestRecorderSerializesConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	recorder := NewRecorder(&buf)
	const workers = 32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := recorder.Record(Event{Payload: json.RawMessage(`{"ok":true}`)}); err != nil {
				t.Errorf("Record: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1; got != workers {
		t.Fatalf("lines = %d, want %d", got, workers)
	}
}
