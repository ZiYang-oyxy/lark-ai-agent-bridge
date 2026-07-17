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
	if len(second.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(second.History))
	}
	if second.WorkDir != "/tmp/work" {
		t.Fatalf("queued running workdir = %q, want original /tmp/work", second.WorkDir)
	}
	after, next := m.Complete(key, "/tmp/work")
	if next == nil || next.Text != "second" {
		t.Fatalf("next = %#v, want second input", next)
	}
	if after.State != StateRunning {
		t.Fatalf("state after complete = %s, want running for next queued item", after.State)
	}
}

func TestCrashedSessionAdoptsNextWorkDir(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Codex, ChatID: "chat"}
	first, queued := m.Enqueue(key, Input{Sender: "u1", Text: "first"}, "/tmp/old-work")
	if queued {
		t.Fatal("first input should not queue")
	}
	m.MarkWindowStarted(key, first.WorkDir, agent.ApprovalDefault)
	crashed := m.MarkCrashed(key, first.WorkDir)
	if crashed.WorkDir != "/tmp/old-work" {
		t.Fatalf("crashed workdir = %q, want old workdir", crashed.WorkDir)
	}
	next, queued := m.Enqueue(key, Input{Sender: "u1", Text: "next"}, "/tmp/new-work")
	if queued {
		t.Fatal("input after crash should start running, not queue")
	}
	if next.WorkDir != "/tmp/new-work" {
		t.Fatalf("workdir after crash = %q, want /tmp/new-work", next.WorkDir)
	}
	if next.WindowStarted {
		t.Fatal("crashed session should require a fresh window")
	}
}

func TestIdleReminderDueOnlyOnce(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Now()
	m.Enqueue(key, Input{Sender: "u1", Text: "first", Time: now.Add(-25 * time.Hour)}, "/tmp/work")
	due := m.IdleReminderDue(now, 24*time.Hour)
	if len(due) != 0 {
		t.Fatalf("running due len = %d, want 0", len(due))
	}
	m.CompleteAt(key, "/tmp/work", now.Add(-25*time.Hour))
	due = m.IdleReminderDue(now, 24*time.Hour)
	if len(due) != 1 {
		t.Fatalf("due len = %d, want 1", len(due))
	}
	due = m.IdleReminderDue(now, 24*time.Hour)
	if len(due) != 0 {
		t.Fatalf("second due len = %d, want 0", len(due))
	}
	m.Enqueue(key, Input{Sender: "u1", Text: "second", Time: now.Add(-1 * time.Hour)}, "/tmp/work")
	m.CompleteAt(key, "/tmp/work", now.Add(-1*time.Hour))
	due = m.IdleReminderDue(now, 24*time.Hour)
	if len(due) != 0 {
		t.Fatalf("recent activity due len = %d, want 0", len(due))
	}
}

func TestTouchResetsIdleReminder(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Now()
	m.Enqueue(key, Input{Sender: "u1", Text: "first", Time: now.Add(-25 * time.Hour)}, "/tmp/work")
	m.CompleteAt(key, "/tmp/work", now.Add(-25*time.Hour))
	if due := m.IdleReminderDue(now, 24*time.Hour); len(due) != 1 {
		t.Fatalf("due len = %d, want 1", len(due))
	}
	touched := m.Touch(key.ID(), now.Add(-1*time.Hour))
	if touched.ID == "" {
		t.Fatal("touch returned empty session")
	}
	if touched.IdleNotified {
		t.Fatal("touch should clear idle notified")
	}
	if due := m.IdleReminderDue(now, 24*time.Hour); len(due) != 0 {
		t.Fatalf("due after touch = %d, want 0", len(due))
	}
}
