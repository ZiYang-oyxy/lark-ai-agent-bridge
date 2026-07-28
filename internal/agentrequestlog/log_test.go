package agentrequestlog

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecorderWritesExactJSONL(t *testing.T) {
	var buf bytes.Buffer
	recorder := NewRecorder(&buf)
	recorder.now = func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) }
	wantPrompt := "token=keep-this-user-text\n| failed | case |"
	if err := recorder.Record(Entry{
		RunID: "run-1", SessionID: "codex:chat", BatchID: "batch-1",
		Agent: "codex", Prompt: wantPrompt, ScheduleProposalEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	var got Entry
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion || got.Prompt != wantPrompt || got.RunID != "run-1" || !got.ScheduleProposalEnabled {
		t.Fatalf("entry = %#v", got)
	}
	var fields map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["schedule_token"]; ok {
		t.Fatal("schedule_token must not be serialized")
	}
	if _, ok := fields["schedule_socket"]; ok {
		t.Fatal("schedule_socket must not be serialized")
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
			if err := recorder.Record(Entry{Prompt: "prompt"}); err != nil {
				t.Errorf("Record: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1; got != workers {
		t.Fatalf("lines = %d, want %d", got, workers)
	}
}
