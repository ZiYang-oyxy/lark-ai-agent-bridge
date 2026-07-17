package session

import (
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
)

func TestFreezeReadyBatchPreservesOrderAndBoundaries(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxInputs: 10, MaxTextRunes: 64 << 10}

	for _, input := range []Input{
		{ID: "i1", Text: "one", WorkDir: "/a", State: InputQueued, Time: now},
		{ID: "i2", Text: "two", WorkDir: "/a", State: InputQueued, Time: now},
		{ID: "i3", Text: "three", WorkDir: "/b", State: InputQueued, Time: now},
	} {
		if _, _, err := m.EnqueueDurable(key, input, input.WorkDir, limits); err != nil {
			t.Fatal(err)
		}
	}

	sess, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Inputs) != 2 || batch.Inputs[0].ID != "i1" || batch.Inputs[1].ID != "i2" {
		t.Fatalf("batch = %#v, want i1 then i2", batch)
	}
	if batch.State != InputStarting || sess.ActiveBatch == nil || sess.ActiveBatch.State != InputStarting {
		t.Fatalf("frozen batch state = %#v, want starting active batch", batch)
	}
	if len(sess.Queue) != 1 || sess.Queue[0].ID != "i3" {
		t.Fatalf("queue = %#v, want untouched workdir boundary i3", sess.Queue)
	}
}

func TestFreezeReadyBatchWaitsForDebounceThenQueuesDueInput(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxInputs: 10, MaxTextRunes: 64 << 10}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "i1", Text: "one", State: InputDebouncing, Time: now, DebounceUntil: now.Add(time.Second)}, "/a", limits); err != nil {
		t.Fatal(err)
	}

	before, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil || batch != nil {
		t.Fatalf("before deadline: session=%#v batch=%#v err=%v", before, batch, err)
	}
	if len(before.Queue) != 1 || before.Queue[0].State != InputDebouncing {
		t.Fatalf("before deadline queue = %#v", before.Queue)
	}

	after, batch, err := m.FreezeReadyBatch(key, now.Add(time.Second), limits)
	if err != nil || batch == nil {
		t.Fatalf("after deadline: session=%#v batch=%#v err=%v", after, batch, err)
	}
	if batch.Inputs[0].State != InputStarting || after.ActiveBatch.Inputs[0].State != InputStarting {
		t.Fatalf("frozen input = %#v, want starting", batch.Inputs[0])
	}
}

func TestFreezeReadyBatchResetIsExclusive(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxInputs: 10, MaxTextRunes: 64 << 10}
	for _, input := range []Input{
		{ID: "reset", Text: "/new", Reset: true, State: InputQueued, Time: now},
		{ID: "after", Text: "after", State: InputQueued, Time: now},
	} {
		if _, _, err := m.EnqueueDurable(key, input, "/a", limits); err != nil {
			t.Fatal(err)
		}
	}

	sess, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Inputs) != 1 || !batch.Inputs[0].Reset {
		t.Fatalf("batch = %#v, want exclusive reset", batch)
	}
	if len(sess.Queue) != 1 || sess.Queue[0].ID != "after" {
		t.Fatalf("queue = %#v, want input after reset retained", sess.Queue)
	}
}

func TestFreezeReadyBatchStopsBeforeResetBoundary(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxInputs: 10, MaxTextRunes: 64 << 10}
	for _, input := range []Input{
		{ID: "before", Text: "before", State: InputQueued, Time: now},
		{ID: "reset", Text: "/new", Reset: true, State: InputQueued, Time: now},
	} {
		if _, _, err := m.EnqueueDurable(key, input, "/a", limits); err != nil {
			t.Fatal(err)
		}
	}

	sess, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Inputs) != 1 || batch.Inputs[0].ID != "before" {
		t.Fatalf("batch = %#v, want input before reset only", batch)
	}
	if len(sess.Queue) != 1 || sess.Queue[0].ID != "reset" {
		t.Fatalf("queue = %#v, want reset retained at head", sess.Queue)
	}
}

func TestFreezeReadyBatchStopsAtRequestedModelAndEffortBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second Input
	}{
		{name: "model", second: Input{ID: "model", Text: "two", RequestedModel: "opus"}},
		{name: "effort", second: Input{ID: "effort", Text: "two", RequestedEffort: "high"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			key := Key{Agent: agent.Claude, ChatID: "chat"}
			now := time.Unix(10, 0)
			limits := BatchLimits{MaxInputs: 10, MaxTextRunes: 64 << 10}
			first := Input{ID: "first", Text: "one", RequestedModel: "sonnet", RequestedEffort: "low", State: InputQueued, Time: now}
			second := tc.second
			second.State, second.Time = InputQueued, now
			for _, input := range []Input{first, second} {
				if _, _, err := m.EnqueueDurable(key, input, "/a", limits); err != nil {
					t.Fatal(err)
				}
			}

			sess, batch, err := m.FreezeReadyBatch(key, now, limits)
			if err != nil {
				t.Fatal(err)
			}
			if batch == nil || len(batch.Inputs) != 1 || batch.Inputs[0].ID != "first" {
				t.Fatalf("batch = %#v, want first only", batch)
			}
			if len(sess.Queue) != 1 || sess.Queue[0].ID != second.ID {
				t.Fatalf("queue = %#v, want boundary input retained", sess.Queue)
			}
		})
	}
}

func TestFreezeReadyBatchRespectsInputAndTextLimitsWithoutSkippingHead(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxInputs: 2, MaxTextRunes: 3}
	for _, input := range []Input{
		{ID: "i1", Text: "a", State: InputQueued, Time: now},
		{ID: "i2", Text: "é", State: InputQueued, Time: now},
		{ID: "i3", Text: "cc", State: InputQueued, Time: now},
	} {
		if _, _, err := m.EnqueueDurable(key, input, "/a", limits); err != nil {
			t.Fatal(err)
		}
	}

	sess, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Inputs) != 2 || batch.Inputs[1].ID != "i2" {
		t.Fatalf("batch = %#v, want i1 and i2", batch)
	}
	if len(sess.Queue) != 1 || sess.Queue[0].ID != "i3" {
		t.Fatalf("queue = %#v, want text-limit head i3 retained", sess.Queue)
	}
}

func TestFreezeReadyBatchStopsAtTextLimitWithoutSkippingHead(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxTextRunes: 1}
	for _, input := range []Input{
		{ID: "i1", Text: "é", State: InputQueued, Time: now},
		{ID: "i2", Text: "ab", State: InputQueued, Time: now},
	} {
		if _, _, err := m.EnqueueDurable(key, input, "/a", limits); err != nil {
			t.Fatal(err)
		}
	}

	sess, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Inputs) != 1 || batch.Inputs[0].ID != "i1" {
		t.Fatalf("batch = %#v, want only first rune-sized input", batch)
	}
	if len(sess.Queue) != 1 || sess.Queue[0].ID != "i2" {
		t.Fatalf("queue = %#v, want text-limit head retained", sess.Queue)
	}
}

func TestFreezeReadyBatchDoesNotPromoteWhileBatchIsActive(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxInputs: 10, MaxTextRunes: 64 << 10}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "active", Text: "one", State: InputQueued, Time: now}, "/a", limits); err != nil {
		t.Fatal(err)
	}
	if _, batch, err := m.FreezeReadyBatch(key, now, limits); err != nil || batch == nil {
		t.Fatalf("initial freeze: batch=%#v err=%v", batch, err)
	}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "waiting", Text: "two", State: InputDebouncing, Time: now, DebounceUntil: now}, "/a", limits); err != nil {
		t.Fatal(err)
	}

	sess, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil || batch != nil {
		t.Fatalf("active freeze: session=%#v batch=%#v err=%v", sess, batch, err)
	}
	if len(sess.Queue) != 1 || sess.Queue[0].State != InputDebouncing {
		t.Fatalf("queue while active = %#v, want unpromoted debouncing input", sess.Queue)
	}
}

func TestEnqueueDurableRejectsFullQueueWithoutMutation(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	limits := BatchLimits{MaxPending: 1}
	first, result, err := m.EnqueueDurable(key, Input{ID: "i1", Text: "one"}, "/a", limits)
	if err != nil || !result.Queued || result.Position != 1 {
		t.Fatalf("first enqueue: session=%#v result=%#v err=%v", first, result, err)
	}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "i2", Text: "two"}, "/a", limits); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("full enqueue error = %v, want pending capacity error", err)
	}
	got, ok := m.Get(key)
	if !ok || len(got.Queue) != 1 || got.Queue[0].ID != "i1" {
		t.Fatalf("queue after rejected enqueue = %#v", got.Queue)
	}
}

func TestEnqueueDurableTreatsZeroMaxPendingAsUnbounded(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	for _, id := range []string{"i1", "i2"} {
		if _, _, err := m.EnqueueDurable(key, Input{ID: id, Text: id}, "/a", BatchLimits{}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	got, ok := m.Get(key)
	if !ok || len(got.Queue) != 2 {
		t.Fatalf("queue = %#v, want two inputs", got.Queue)
	}
}

func TestSessionClonesDoNotLeakDurableQueueOrActiveBatch(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxInputs: 10, MaxTextRunes: 64 << 10}
	for _, input := range []Input{
		{ID: "active", Text: "one", State: InputQueued, Time: now},
		{ID: "queued", Text: "two", State: InputQueued, Time: now},
	} {
		if _, _, err := m.EnqueueDurable(key, input, "/a", limits); err != nil {
			t.Fatal(err)
		}
	}
	if _, batch, err := m.FreezeReadyBatch(key, now, BatchLimits{MaxInputs: 1, MaxTextRunes: 64 << 10}); err != nil || batch == nil {
		t.Fatalf("freeze: batch=%#v err=%v", batch, err)
	} else {
		batch.Inputs[0].Text = "changed returned batch"
	}

	got, ok := m.Get(key)
	if !ok || got.ActiveBatch == nil {
		t.Fatalf("session = %#v", got)
	}
	got.Queue[0].Text = "changed queued"
	got.ActiveBatch.Inputs[0].Text = "changed active"
	got.ActiveBatch.RenderRef = &RenderRef{CardID: "changed"}

	again, ok := m.Get(key)
	if !ok || again.Queue[0].Text != "two" || again.ActiveBatch.Inputs[0].Text != "one" || again.ActiveBatch.RenderRef != nil {
		t.Fatalf("manager state leaked through clone: %#v", again)
	}

	listed := m.List()
	listed[0].ActiveBatch.Inputs[0].Text = "changed list"
	if final, _ := m.Get(key); final.ActiveBatch.Inputs[0].Text != "one" {
		t.Fatalf("manager state leaked through list: %#v", final)
	}
}

func TestCloneSessionDeepCopiesRenderRef(t *testing.T) {
	original := &Session{
		Queue: []Input{{ID: "queued", Text: "queued"}},
		ActiveBatch: &Batch{
			Inputs:    []Input{{ID: "active", Text: "active"}},
			RenderRef: &RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 1},
		},
	}
	cloned := cloneSession(original)
	cloned.Queue[0].Text = "changed queued"
	cloned.ActiveBatch.Inputs[0].Text = "changed active"
	cloned.ActiveBatch.RenderRef.CardID = "changed card"

	if original.Queue[0].Text != "queued" || original.ActiveBatch.Inputs[0].Text != "active" || original.ActiveBatch.RenderRef.CardID != "card" {
		t.Fatalf("clone mutated original: %#v", original)
	}
}

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

func TestGetReturnsExistingSessionWithoutCreating(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	if _, ok := m.Get(key); ok {
		t.Fatal("Get before create should return false")
	}
	m.Reset(key, "/tmp/work")
	got, ok := m.Get(key)
	if !ok {
		t.Fatal("Get after create should return true")
	}
	if got.WorkDir != "/tmp/work" {
		t.Fatalf("workdir = %q, want /tmp/work", got.WorkDir)
	}
}

func TestMarkCrashedClearsQueueAndAllowsNextRun(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	m.Enqueue(key, Input{Sender: "u1", Text: "first"}, "/tmp/work")
	m.Enqueue(key, Input{Sender: "u1", Text: "second"}, "/tmp/work")
	crashed := m.MarkCrashed(key, "/tmp/work")
	if crashed.State != StateCrashed || len(crashed.Queue) != 0 {
		t.Fatalf("crashed session = %#v, want crashed with empty queue", crashed)
	}
	next, queued := m.Enqueue(key, Input{Sender: "u1", Text: "third"}, "/tmp/work")
	if queued {
		t.Fatalf("next input queued after crash: %#v", next)
	}
	if next.State != StateRunning {
		t.Fatalf("next state = %s, want running", next.State)
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

func TestUpdateRunResultAccumulatesTokens(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	started, _ := m.Enqueue(key, Input{Sender: "u1", Text: "first"}, "/tmp/work")
	first := m.UpdateRunResult(started.ID, "claude-session-1", "sonnet", 12)
	second := m.UpdateRunResult(started.ID, "", "", 8)
	if first.Tokens != 12 || second.Tokens != 20 {
		t.Fatalf("tokens first=%d second=%d, want 12 then 20", first.Tokens, second.Tokens)
	}
}
