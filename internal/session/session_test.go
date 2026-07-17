package session

import (
	"path/filepath"
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

func TestFreezeReadyBatchAllowsOversizedHeadAsSingleInput(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxTextRunes: 3}
	for _, input := range []Input{
		{ID: "oversized", Text: "four", State: InputQueued, Time: now},
		{ID: "following", Text: "one", State: InputQueued, Time: now},
	} {
		if _, _, err := m.EnqueueDurable(key, input, "/a", limits); err != nil {
			t.Fatal(err)
		}
	}

	sess, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Inputs) != 1 || batch.Inputs[0].ID != "oversized" {
		t.Fatalf("batch = %#v, want oversized queue head as one-input batch", batch)
	}
	if len(sess.Queue) != 1 || sess.Queue[0].ID != "following" {
		t.Fatalf("queue = %#v, want following input retained", sess.Queue)
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

func TestEnqueueDurableCountsActiveBatchAgainstMaxPending(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxPending: 2}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "active", Text: "one", State: InputQueued, Time: now}, "/a", limits); err != nil {
		t.Fatal(err)
	}
	if _, batch, err := m.FreezeReadyBatch(key, now, BatchLimits{MaxInputs: 1}); err != nil || batch == nil {
		t.Fatalf("freeze: batch=%#v err=%v", batch, err)
	}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "queued", Text: "two", State: InputQueued, Time: now}, "/a", limits); err != nil {
		t.Fatal(err)
	}

	before, ok := m.Get(key)
	if !ok {
		t.Fatal("missing session before full enqueue")
	}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "rejected", Text: "three", State: InputQueued, Time: now}, "/a", limits); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("full enqueue error = %v, want pending capacity error", err)
	}
	after, ok := m.Get(key)
	if !ok || len(after.Queue) != 1 || after.Queue[0].ID != "queued" || after.ActiveBatch == nil || len(after.ActiveBatch.Inputs) != 1 || after.ActiveBatch.Inputs[0].ID != "active" {
		t.Fatalf("session after rejected enqueue = %#v", after)
	}
	if len(before.Queue) != len(after.Queue) || before.Queue[0].ID != after.Queue[0].ID || before.ActiveBatch.Inputs[0].ID != after.ActiveBatch.Inputs[0].ID {
		t.Fatalf("rejected enqueue changed durable state: before=%#v after=%#v", before, after)
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

func TestReadyKeysFiltersActiveAndFutureSessionsWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewManagerWithStore(path)
	now := time.Unix(100, 0)
	limits := BatchLimits{}
	readyA := Key{Agent: agent.Claude, ChatID: "a"}
	readyB := Key{Agent: agent.Claude, ChatID: "b"}
	future := Key{Agent: agent.Claude, ChatID: "future"}
	active := Key{Agent: agent.Claude, ChatID: "active"}

	for _, tc := range []struct {
		key   Key
		input Input
	}{
		{readyB, Input{ID: "b", State: InputQueued}},
		{readyA, Input{ID: "a", State: InputDebouncing, DebounceUntil: now}},
		{future, Input{ID: "future", State: InputDebouncing, DebounceUntil: now.Add(time.Second)}},
		{active, Input{ID: "active", State: InputQueued}},
	} {
		if _, _, err := m.EnqueueDurable(tc.key, tc.input, "/w", limits); err != nil {
			t.Fatal(err)
		}
	}
	if _, batch, err := m.FreezeReadyBatch(active, now, limits); err != nil || batch == nil {
		t.Fatalf("freeze active batch=%#v err=%v", batch, err)
	}
	revision := m.revision

	got := m.ReadyKeys(now)
	if len(got) != 2 || got[0] != readyA || got[1] != readyB {
		t.Fatalf("ready keys = %#v, want a then b", got)
	}
	if m.revision != revision {
		t.Fatalf("ReadyKeys advanced revision to %d, want %d", m.revision, revision)
	}
	stored, ok := m.Get(readyA)
	if !ok || stored.Queue[0].State != InputDebouncing {
		t.Fatalf("ReadyKeys promoted durable input: %#v", stored)
	}
}

func TestMarkBatchRunningTransitionsAndReturnsIsolatedClones(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "running"}
	now := time.Unix(100, 0)
	if _, _, err := m.EnqueueDurable(key, Input{ID: "input", Text: "hello", State: InputQueued, Time: now}, "/w", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	_, frozen, err := m.FreezeReadyBatch(key, now, BatchLimits{})
	if err != nil || frozen == nil {
		t.Fatalf("freeze batch=%#v err=%v", frozen, err)
	}
	ref := &RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 1}
	sess, batch, err := m.MarkBatchRunning(key, frozen.ID, ref, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if sess.State != StateRunning || sess.ActiveBatch == nil || sess.ActiveBatch.State != InputRunning || sess.ActiveBatch.StartedAt != now.Add(time.Second) || sess.ActiveBatch.Inputs[0].State != InputRunning {
		t.Fatalf("running session = %#v", sess)
	}
	if batch == nil || batch.State != InputRunning || batch.RenderRef == nil || batch.RenderRef.CardID != "card" {
		t.Fatalf("running batch = %#v", batch)
	}

	ref.CardID = "caller mutation"
	sess.ActiveBatch.RenderRef.CardID = "session mutation"
	batch.Inputs[0].Text = "batch mutation"
	got, ok := m.Get(key)
	if !ok || got.ActiveBatch == nil || got.ActiveBatch.RenderRef.CardID != "card" || got.ActiveBatch.Inputs[0].Text != "hello" {
		t.Fatalf("returned values leaked into manager: %#v", got)
	}
}

func TestMarkBatchRunningResetClearsContextAndPreservesLaterQueue(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "reset"}
	now := time.Unix(100, 0)
	old, _ := m.Enqueue(key, Input{ID: "old", Sender: "u", Text: "old prompt", Time: now}, "/old")
	m.UpdateRunResult(old.ID, "old-session", "old-model", 12)
	m.CompleteAt(key, "/old", now.Add(time.Second))
	for _, input := range []Input{
		{ID: "reset", Sender: "u", Text: "fresh prompt", Reset: true, WorkDir: "/new", State: InputQueued, Time: now.Add(2 * time.Second)},
		{ID: "later", Sender: "u", Text: "later prompt", WorkDir: "/new", State: InputQueued, Time: now.Add(3 * time.Second)},
	} {
		if _, _, err := m.EnqueueDurable(key, input, input.WorkDir, BatchLimits{}); err != nil {
			t.Fatal(err)
		}
	}
	_, frozen, err := m.FreezeReadyBatch(key, now.Add(4*time.Second), BatchLimits{})
	if err != nil || frozen == nil {
		t.Fatalf("freeze batch=%#v err=%v", frozen, err)
	}
	sess, _, err := m.MarkBatchRunning(key, frozen.ID, nil, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if sess.ClaudeSessionID != "" || sess.Model != "" || sess.Tokens != 0 || sess.WorkDir != "/new" {
		t.Fatalf("reset context = %#v", sess)
	}
	if len(sess.History) != 1 || sess.History[0].Text != "fresh prompt" {
		t.Fatalf("reset history = %#v", sess.History)
	}
	if sess.ActiveBatch == nil || len(sess.ActiveBatch.Inputs) != 1 || len(sess.Queue) != 1 || sess.Queue[0].ID != "later" {
		t.Fatalf("reset scheduling state = %#v", sess)
	}
}

func TestFinishBatchReleasesEveryTerminalStatusAndPreservesQueue(t *testing.T) {
	for _, status := range []InputState{InputCompleted, InputFailed, InputCancelled, InputInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			m := NewManager()
			key := Key{Agent: agent.Claude, ChatID: string(status)}
			now := time.Unix(100, 0)
			if _, _, err := m.EnqueueDurable(key, Input{ID: "active", Text: "one", State: InputQueued, Time: now}, "/w", BatchLimits{}); err != nil {
				t.Fatal(err)
			}
			_, frozen, err := m.FreezeReadyBatch(key, now, BatchLimits{})
			if err != nil || frozen == nil {
				t.Fatalf("freeze batch=%#v err=%v", frozen, err)
			}
			if _, _, err := m.MarkBatchRunning(key, frozen.ID, nil, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			m.UpdateRunResult(key.ID(), "", "", 4)
			if _, _, err := m.EnqueueDurable(key, Input{ID: "later", Text: "two", State: InputQueued, Time: now}, "/w", BatchLimits{}); err != nil {
				t.Fatal(err)
			}

			finishedAt := now.Add(2 * time.Second)
			sess, err := m.FinishBatch(key, frozen.ID, BatchCompletion{Status: status, ClaudeSessionID: "new-session", Model: "new-model", Tokens: 7, At: finishedAt})
			if err != nil {
				t.Fatal(err)
			}
			if sess.State != StateIdle || sess.ActiveBatch != nil || len(sess.Queue) != 1 || sess.Queue[0].ID != "later" {
				t.Fatalf("finished scheduling state = %#v", sess)
			}
			if sess.ClaudeSessionID != "new-session" || sess.Model != "new-model" || sess.Tokens != 11 || !sess.LastActive.Equal(finishedAt) {
				t.Fatalf("finished context = %#v", sess)
			}
		})
	}
}

func TestFinishBatchRejectsStaleBatchAndInvalidStatusWithoutMutation(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "stale"}
	now := time.Unix(100, 0)
	if _, _, err := m.EnqueueDurable(key, Input{ID: "active", State: InputQueued, Time: now}, "/w", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	_, frozen, err := m.FreezeReadyBatch(key, now, BatchLimits{})
	if err != nil || frozen == nil {
		t.Fatalf("freeze batch=%#v err=%v", frozen, err)
	}
	if _, _, err := m.MarkBatchRunning(key, frozen.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	before, _ := m.Get(key)
	for _, completion := range []BatchCompletion{
		{Status: InputCompleted, At: now},
		{Status: InputQueued, At: now},
	} {
		batchID := frozen.ID
		if completion.Status == InputCompleted {
			batchID = "stale-batch"
		}
		if _, err := m.FinishBatch(key, batchID, completion); err == nil {
			t.Fatalf("FinishBatch(%q, %#v) unexpectedly succeeded", batchID, completion)
		}
		got, _ := m.Get(key)
		if got.ActiveBatch == nil || got.ActiveBatch.ID != before.ActiveBatch.ID || got.State != before.State || got.Tokens != before.Tokens {
			t.Fatalf("failed finish mutated manager: before=%#v after=%#v", before, got)
		}
	}
}

func TestCancelQueuedInputByMessageIDMatchesBothIDsAndPreservesFIFO(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "cancel"}
	for _, input := range []Input{
		{ID: "input-1", State: InputQueued},
		{ID: "input-2", ReplyToMessageID: "reply-2", State: InputDebouncing},
		{ID: "input-3", State: InputQueued},
	} {
		if _, _, err := m.EnqueueDurable(key, input, "/w", BatchLimits{}); err != nil {
			t.Fatal(err)
		}
	}
	sess, removed, found, err := m.CancelQueuedInputByMessageID("reply-2")
	if err != nil || !found || removed.ID != "input-2" || removed.State != InputCancelled {
		t.Fatalf("reply cancellation: session=%#v removed=%#v found=%t err=%v", sess, removed, found, err)
	}
	if len(sess.Queue) != 2 || sess.Queue[0].ID != "input-1" || sess.Queue[1].ID != "input-3" {
		t.Fatalf("reply cancellation queue = %#v", sess.Queue)
	}
	sess, removed, found, err = m.CancelQueuedInputByMessageID("input-1")
	if err != nil || !found || removed.ID != "input-1" || removed.State != InputCancelled || len(sess.Queue) != 1 || sess.Queue[0].ID != "input-3" {
		t.Fatalf("input cancellation: session=%#v removed=%#v found=%t err=%v", sess, removed, found, err)
	}
	if sess, removed, found, err := m.CancelQueuedInputByMessageID(""); err != nil || found || sess.ID != "" || removed.ID != "" {
		t.Fatalf("empty cancellation: session=%#v removed=%#v found=%t err=%v", sess, removed, found, err)
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
