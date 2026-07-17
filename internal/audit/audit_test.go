package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

type sequenceFailWriter struct {
	errs []error
}

func (w *sequenceFailWriter) Write([]byte) (int, error) {
	err := w.errs[0]
	w.errs = w.errs[1:]
	return 0, err
}

type switchWriter struct {
	writer io.Writer
}

func (w *switchWriter) Write(p []byte) (int, error) {
	return w.writer.Write(p)
}

func TestRecorderWritesRedactedJSONL(t *testing.T) {
	var buf bytes.Buffer
	recorder := NewRecorderWithWriter(&buf)
	recorder.Record("u1", "run_input", "claude:chat", "token=secret-value password:123")
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("audit log line is empty")
	}
	if strings.Contains(line, "secret-value") || strings.Contains(line, "123") {
		t.Fatalf("secret leaked in audit log: %s", line)
	}
	var event Event
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("audit log is not JSON: %v", err)
	}
	if event.Detail != "token=[REDACTED] password:[REDACTED]" {
		t.Fatalf("detail = %q, want redacted detail", event.Detail)
	}
}

func TestRecorderExposesWriteErrors(t *testing.T) {
	recorder := NewRecorderWithWriter(failWriter{})
	recorder.Record("u", "run", "session", "detail")

	if got := recorder.WriteErrors(); got != 1 {
		t.Fatalf("write errors = %d, want 1", got)
	}
	if err := recorder.LastWriteError(); err == nil || err.Error() != "disk full" {
		t.Fatalf("last write error = %v, want disk full", err)
	}
	if got := recorder.Events(); len(got) != 1 {
		t.Fatalf("events = %#v, want one retained event", got)
	}
}

func TestRecorderKeepsWriteFailureHistoryAfterLaterSuccess(t *testing.T) {
	writer := &switchWriter{writer: failWriter{}}
	recorder := NewRecorderWithWriter(writer)
	recorder.Record("u", "first", "session", "detail")
	writer.writer = &bytes.Buffer{}
	recorder.Record("u", "second", "session", "detail")

	if got := recorder.WriteErrors(); got != 1 {
		t.Fatalf("write errors = %d, want 1", got)
	}
	if err := recorder.LastWriteError(); err == nil || err.Error() != "disk full" {
		t.Fatalf("last write error = %v, want retained disk full", err)
	}
}

func TestRecorderExposesMostRecentWriteError(t *testing.T) {
	recorder := NewRecorderWithWriter(&sequenceFailWriter{errs: []error{errors.New("first failure"), errors.New("second failure")}})
	recorder.Record("u", "first", "session", "detail")
	recorder.Record("u", "second", "session", "detail")

	if got := recorder.WriteErrors(); got != 2 {
		t.Fatalf("write errors = %d, want 2", got)
	}
	if err := recorder.LastWriteError(); err == nil || err.Error() != "second failure" {
		t.Fatalf("last write error = %v, want second failure", err)
	}
}

func TestRecorderConcurrentRecordAndWriteErrorReads(t *testing.T) {
	recorder := NewRecorderWithWriter(failWriter{})
	const workers = 32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder.Record("u", "run", "session", "detail")
			_ = recorder.WriteErrors()
			_ = recorder.LastWriteError()
		}()
	}
	wg.Wait()

	if got := recorder.WriteErrors(); got != workers {
		t.Fatalf("write errors = %d, want %d", got, workers)
	}
}
