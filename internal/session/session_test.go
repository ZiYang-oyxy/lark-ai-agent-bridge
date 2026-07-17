package session

import (
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
)

func TestEnqueueQueuesWhileRunning(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	first, queued := m.Enqueue(key, Input{Sender: "u1", Text: "first"}, "/tmp/work")
	if queued {
		t.Fatal("first input should start running, not queue")
	}
	if first.State != StateRunning {
		t.Fatalf("state = %s, want running", first.State)
	}
	second, queued := m.Enqueue(key, Input{Sender: "u1", Text: "second"}, "/tmp/work")
	if !queued {
		t.Fatal("second input should queue")
	}
	if len(second.Queue) != 1 {
		t.Fatalf("queue len = %d, want 1", len(second.Queue))
	}
	if len(second.History) != 1 {
		t.Fatalf("history len = %d, want only running prompt recorded", len(second.History))
	}
	after, next := m.Complete(key, "/tmp/work")
	if next == nil || next.Text != "second" {
		t.Fatalf("next = %#v, want second input", next)
	}
	if after.State != StateRunning {
		t.Fatalf("state after complete = %s, want running for next queued item", after.State)
	}
	if len(after.History) != 2 {
		t.Fatalf("history len after dequeue = %d, want 2", len(after.History))
	}
}

func TestResetClearsClaudeSessionAndHistory(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"}
	started, queued := m.Enqueue(key, Input{Sender: "u1", Text: "first"}, "/tmp/work")
	if queued {
		t.Fatal("first input should start")
	}
	m.UpdateRunResult(started.ID, "claude-session-1", "sonnet", 12)
	m.Complete(key, "/tmp/work")
	reset := m.Reset(key, "/tmp/next")
	if reset.ClaudeSessionID != "" || reset.Model != "" || reset.Tokens != 0 {
		t.Fatalf("reset metadata = %#v, want cleared", reset)
	}
	if len(reset.History) != 0 {
		t.Fatalf("history len = %d, want cleared", len(reset.History))
	}
	if reset.WorkDir != "/tmp/next" {
		t.Fatalf("workdir = %q, want /tmp/next", reset.WorkDir)
	}
}

func TestQueuedResetClearsBeforeNextInput(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	first, _ := m.Enqueue(key, Input{Sender: "u1", Text: "first"}, "/tmp/work")
	m.UpdateRunResult(first.ID, "claude-session-1", "sonnet", 12)
	m.Enqueue(key, Input{Sender: "u1", Text: "second", Reset: true}, "/tmp/work")
	after, next := m.Complete(key, "/tmp/work")
	if next == nil || next.Text != "second" {
		t.Fatalf("next = %#v, want reset input", next)
	}
	if after.ClaudeSessionID != "" {
		t.Fatalf("claude session = %q, want reset before next run", after.ClaudeSessionID)
	}
	if len(after.History) != 1 || after.History[0].Text != "second" {
		t.Fatalf("history = %#v, want only reset prompt", after.History)
	}
}

func TestCompleteMarksIdle(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Now()
	m.Enqueue(key, Input{Sender: "u1", Text: "first", Time: now}, "/tmp/work")
	after, next := m.CompleteAt(key, "/tmp/work", now.Add(time.Second))
	if next != nil {
		t.Fatalf("next = %#v, want nil", next)
	}
	if after.State != StateIdle {
		t.Fatalf("state = %s, want idle", after.State)
	}
	if !after.LastActive.Equal(now.Add(time.Second)) {
		t.Fatalf("last active = %s, want completion time", after.LastActive)
	}
}
