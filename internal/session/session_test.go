package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/media"
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

func TestLiveAttachmentPathsCopiesQueuedAndActiveAttachments(t *testing.T) {
	m := NewManager()
	paths := map[string]string{
		"debouncing": "/cache/debouncing.png",
		"queued":     "/cache/queued.png",
		"starting":   "/cache/starting.png",
		"running":    "/cache/running.png",
	}
	queuedKey := Key{Agent: agent.Claude, ChatID: "media-queued"}
	startingKey := Key{Agent: agent.Claude, ChatID: "media-starting"}
	runningKey := Key{Agent: agent.Claude, ChatID: "media-running"}
	m.GetOrCreate(queuedKey, "/work")
	m.GetOrCreate(startingKey, "/work")
	m.GetOrCreate(runningKey, "/work")
	m.mu.Lock()
	m.sessions[queuedKey.ID()].Queue = []Input{
		{State: InputDebouncing, Attachments: []media.Attachment{{Path: paths["debouncing"]}}},
		{State: InputQueued, Attachments: []media.Attachment{{Path: paths["queued"]}}},
	}
	m.sessions[startingKey.ID()].ActiveBatch = &Batch{State: InputStarting, Inputs: []Input{{State: InputStarting, Attachments: []media.Attachment{{Path: paths["starting"]}}}}}
	m.sessions[runningKey.ID()].ActiveBatch = &Batch{State: InputRunning, Inputs: []Input{{State: InputRunning, Attachments: []media.Attachment{{Path: paths["running"]}}}}}
	m.mu.Unlock()

	got := m.LiveAttachmentPaths()
	for _, path := range paths {
		if _, ok := got[path]; !ok {
			t.Fatalf("live paths = %v, missing %q", got, path)
		}
	}
	delete(got, paths["running"])
	if _, ok := m.LiveAttachmentPaths()[paths["running"]]; !ok {
		t.Fatal("LiveAttachmentPaths returned an aliased set")
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

func TestFreezeReadyBatchStopsAtRuntimePreferenceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second Input
	}{
		{name: "model", second: Input{ID: "model", Text: "two", RequestedModel: "opus"}},
		{name: "effort", second: Input{ID: "effort", Text: "two", RequestedEffort: "high"}},
		{name: "agent_bin", second: Input{ID: "bin", Text: "two", RequestedModel: "sonnet", RequestedEffort: "low", AgentBin: "/w/bin/cx3"}},
		{name: "agent_home", second: Input{ID: "home", Text: "two", RequestedModel: "sonnet", RequestedEffort: "low", AgentHome: "/w/.codex-home"}},
		{name: "bridge_instructions", second: Input{ID: "instructions", Text: "two", RequestedModel: "sonnet", RequestedEffort: "low", BridgeInstructionsVersion: "v2"}},
		{name: "conversation_mode", second: Input{ID: "conversation", Text: "two", RequestedModel: "sonnet", RequestedEffort: "low", ConversationMode: config.ConversationModeTopic}},
		{name: "reply_mode", second: Input{ID: "reply", Text: "two", RequestedModel: "sonnet", RequestedEffort: "low", ConversationMode: config.ConversationModeChat, ReplyMode: config.ReplyModeAppendCleanCard}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			key := Key{Agent: agent.Claude, ChatID: "chat"}
			now := time.Unix(10, 0)
			limits := BatchLimits{MaxInputs: 10, MaxTextRunes: 64 << 10}
			first := Input{ID: "first", Text: "one", RequestedModel: "sonnet", RequestedEffort: "low", ConversationMode: config.ConversationModeChat, ReplyMode: config.ReplyModeAppend, State: InputQueued, Time: now}
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

func TestFreezeReadyBatchRespectsAttachmentLimitsWithoutSkippingHead(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "attachments"}
	now := time.Unix(10, 0)
	limits := BatchLimits{MaxAttachments: 10, MaxAttachmentBytes: 100 << 20}
	attachments := make([]media.Attachment, 9)
	for i := range attachments {
		attachments[i] = media.Attachment{Path: "/cache/fit", Size: 10 << 20}
	}
	for _, input := range []Input{
		{ID: "nine", State: InputQueued, Time: now, Attachments: attachments},
		{ID: "ten", State: InputQueued, Time: now, Attachments: []media.Attachment{{Path: "/cache/ten", Size: 10 << 20}}},
		{ID: "eleventh", State: InputQueued, Time: now, Attachments: []media.Attachment{{Path: "/cache/eleventh", Size: 1}}},
		{ID: "over-bytes", State: InputQueued, Time: now, Attachments: []media.Attachment{{Path: "/cache/over", Size: 101 << 20}}},
	} {
		if _, _, err := m.EnqueueDurable(key, input, "/work", limits); err != nil {
			t.Fatal(err)
		}
	}

	sess, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil || batch == nil {
		t.Fatalf("freeze batch=%#v err=%v", batch, err)
	}
	if len(batch.Inputs) != 2 || batch.Inputs[0].ID != "nine" || batch.Inputs[1].ID != "ten" {
		t.Fatalf("batch inputs = %#v, want boundary pair", batch.Inputs)
	}
	if len(sess.Queue) != 2 || sess.Queue[0].ID != "eleventh" || sess.Queue[1].ID != "over-bytes" {
		t.Fatalf("remaining queue = %#v, want eleventh then over-bytes", sess.Queue)
	}
	if _, _, err := m.MarkBatchRunning(key, batch.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.FinishBatch(key, batch.ID, BatchCompletion{Status: InputCompleted, At: now}); err != nil {
		t.Fatal(err)
	}
	sess, next, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil || next == nil || len(next.Inputs) != 1 || next.Inputs[0].ID != "eleventh" {
		t.Fatalf("next batch=%#v err=%v, want eleventh only", next, err)
	}
	if len(sess.Queue) != 1 || sess.Queue[0].ID != "over-bytes" {
		t.Fatalf("queue after byte limit = %#v, want over-byte head retained", sess.Queue)
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

func TestFinishBatchRearmsCompatibleQueueHead(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "rearm"}
	now := time.Unix(100, 0)
	limits := BatchLimits{MaxPending: 10}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "active", Text: "active", State: InputQueued, Time: now}, "/work", limits); err != nil {
		t.Fatal(err)
	}
	_, batch, err := m.FreezeReadyBatch(key, now, limits)
	if err != nil || batch == nil {
		t.Fatalf("freeze active batch = %#v, err=%v", batch, err)
	}
	if _, _, err := m.MarkBatchRunning(key, batch.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	oldDeadline := now.Add(time.Second)
	for _, input := range []Input{
		{ID: "text", Text: "text", State: InputDebouncing, Time: now, DebounceUntil: oldDeadline, DebounceWindow: 250 * time.Millisecond},
		{ID: "rich", Text: "rich", State: InputDebouncing, Time: now, DebounceUntil: oldDeadline, DebounceWindow: time.Second},
		{ID: "different-bin", Text: "later", State: InputDebouncing, Time: now, DebounceUntil: oldDeadline, DebounceWindow: time.Second, AgentBin: "other"},
	} {
		if _, _, err := m.EnqueueDurable(key, input, "/work", limits); err != nil {
			t.Fatal(err)
		}
	}
	completedAt := now.Add(10 * time.Second)
	sess, err := m.FinishBatch(key, batch.ID, BatchCompletion{Status: InputCompleted, At: completedAt})
	if err != nil {
		t.Fatal(err)
	}
	want := completedAt.Add(time.Second)
	if len(sess.Queue) != 3 || !sess.Queue[0].DebounceUntil.Equal(want) || !sess.Queue[1].DebounceUntil.Equal(want) {
		t.Fatalf("rearmed queue = %#v, want first cohort deadline %s", sess.Queue, want)
	}
	if !sess.Queue[2].DebounceUntil.Equal(oldDeadline) {
		t.Fatalf("incompatible deadline = %s, want unchanged %s", sess.Queue[2].DebounceUntil, oldDeadline)
	}
}

func TestFreezeReadyBatchUsesUniqueIDsToRejectLateCompletionAtSameTime(t *testing.T) {
	m := NewManager()
	key := Key{Agent: agent.Claude, ChatID: "same-time"}
	now := time.Unix(100, 0)
	if _, _, err := m.EnqueueDurable(key, Input{ID: "a", Text: "first", State: InputQueued, Time: now}, "/w", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	_, first, err := m.FreezeReadyBatch(key, now, BatchLimits{})
	if err != nil || first == nil {
		t.Fatalf("freeze first batch=%#v err=%v", first, err)
	}
	if _, _, err := m.MarkBatchRunning(key, first.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "b", Text: "second", State: InputQueued, Time: now}, "/w", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.FinishBatch(key, first.ID, BatchCompletion{Status: InputCompleted, AgentSessionID: "session-a", Model: "model-a", Tokens: 5, At: now}); err != nil {
		t.Fatal(err)
	}

	_, second, err := m.FreezeReadyBatch(key, now, BatchLimits{})
	if err != nil || second == nil {
		t.Fatalf("freeze second batch=%#v err=%v", second, err)
	}
	if second.ID == first.ID {
		t.Fatalf("batch IDs collided at same time: %q", first.ID)
	}
	if _, _, err := m.MarkBatchRunning(key, second.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.EnqueueDurable(key, Input{ID: "later", Text: "third", State: InputQueued, Time: now}, "/w", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	before, ok := m.Get(key)
	if !ok {
		t.Fatal("missing session before late completion")
	}
	revision := m.revision

	if _, err := m.FinishBatch(key, first.ID, BatchCompletion{Status: InputCompleted, AgentSessionID: "stale", Model: "stale", Tokens: 99, At: now.Add(time.Second)}); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("late completion error = %v, want batch mismatch", err)
	}
	after, ok := m.Get(key)
	if !ok || after.ActiveBatch == nil || after.ActiveBatch.ID != second.ID || after.State != StateRunning || len(after.Queue) != 1 || after.Queue[0].ID != "later" || after.AgentSessionID != before.AgentSessionID || after.Model != before.Model || after.Tokens != before.Tokens || after.LastActive != before.LastActive || m.revision != revision {
		t.Fatalf("late completion changed new batch: before=%#v after=%#v revision=%d want=%d", before, after, m.revision, revision)
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
	created := time.Date(2026, 7, 18, 9, 30, 0, 0, time.UTC)
	original := &Session{
		Queue: []Input{{ID: "queued", Text: "queued", Attachments: []media.Attachment{{Path: "/cache/queued.png"}}}},
		ActiveBatch: &Batch{
			Inputs:    []Input{{ID: "active", Text: "active", Attachments: []media.Attachment{{Path: "/cache/active.png"}}}},
			RenderRef: &RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 1, CreatedAt: created, SequenceUnknown: true, PendingSequence: 2},
		},
	}
	cloned := cloneSession(original)
	cloned.Queue[0].Text = "changed queued"
	cloned.ActiveBatch.Inputs[0].Text = "changed active"
	cloned.Queue[0].Attachments[0].Path = "/cache/changed-queued.png"
	cloned.ActiveBatch.Inputs[0].Attachments[0].Path = "/cache/changed-active.png"
	cloned.ActiveBatch.RenderRef.CardID = "changed card"

	if original.Queue[0].Text != "queued" || original.ActiveBatch.Inputs[0].Text != "active" ||
		original.Queue[0].Attachments[0].Path != "/cache/queued.png" || original.ActiveBatch.Inputs[0].Attachments[0].Path != "/cache/active.png" ||
		original.ActiveBatch.RenderRef.CardID != "card" || !original.ActiveBatch.RenderRef.CreatedAt.Equal(created) || !original.ActiveBatch.RenderRef.SequenceUnknown || original.ActiveBatch.RenderRef.PendingSequence != 2 {
		t.Fatalf("clone mutated original: %#v", original)
	}
}

func TestRenderRefJSONKeepsCreatedAtAndOmitsInactiveP2Fields(t *testing.T) {
	created := time.Date(2026, 7, 18, 9, 30, 0, 0, time.UTC)
	ref := RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 4, CreatedAt: created}
	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("sequence_unknown")) || bytes.Contains(data, []byte("pending_sequence")) {
		t.Fatalf("inactive P2 fields leaked into JSON: %s", data)
	}
	ref.SequenceUnknown = true
	ref.PendingSequence = 5
	data, err = json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip RenderRef
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !roundTrip.CreatedAt.Equal(created) || !roundTrip.SequenceUnknown || roundTrip.PendingSequence != 5 {
		t.Fatalf("round trip = %#v", roundTrip)
	}
	var legacy RenderRef
	if err := json.Unmarshal([]byte(`{"CardID":"old","ReplyMessageID":"reply","Version":3}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if !legacy.CreatedAt.IsZero() || legacy.SequenceUnknown || legacy.PendingSequence != 0 {
		t.Fatalf("legacy ref = %#v", legacy)
	}
}

func TestReplaceActiveBatchRenderRefPersistsBeforePublishing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewManagerWithStore(path)
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Now().UTC()
	if _, _, err := m.EnqueueDurable(key, Input{ID: "input", Text: "hello", Time: now}, "/tmp", BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	_, batch, err := m.FreezeReadyBatch(key, now, BatchLimits{})
	if err != nil || batch == nil {
		t.Fatalf("freeze batch=%#v err=%v", batch, err)
	}
	initial := &RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 3, CreatedAt: now}
	if _, _, err := m.MarkBatchRunning(key, batch.ID, initial, now); err != nil {
		t.Fatal(err)
	}
	pending := *initial
	pending.SequenceUnknown = true
	pending.PendingSequence = 4
	if err := m.ReplaceActiveBatchRenderRef(key.ID(), batch.ID, pending); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Get(key)
	if got.ActiveBatch == nil || got.ActiveBatch.RenderRef == nil || !got.ActiveBatch.RenderRef.SequenceUnknown || got.ActiveBatch.RenderRef.PendingSequence != 4 {
		t.Fatalf("active ref = %#v", got.ActiveBatch)
	}
	reopened := NewManagerWithStore(path)
	// Restore converts an active batch to a notice; the persisted ref must be the pending value.
	notices, err := reopened.Restore()
	if err != nil || len(notices) == 0 || notices[0].RenderRef == nil || !notices[0].RenderRef.SequenceUnknown || notices[0].RenderRef.PendingSequence != 4 {
		t.Fatalf("notices=%#v err=%v", notices, err)
	}
}

func TestReplaceActiveBatchRenderRefRejectsInvalidIdentity(t *testing.T) {
	m := NewManager()
	for _, tc := range []struct {
		sessionID, batchID string
		ref                RenderRef
	}{
		{"missing", "batch", RenderRef{CardID: "card"}},
		{"missing", "batch", RenderRef{}},
		{"missing", "batch", RenderRef{CardID: "card", PendingSequence: -1}},
	} {
		if err := m.ReplaceActiveBatchRenderRef(tc.sessionID, tc.batchID, tc.ref); err == nil {
			t.Fatalf("ReplaceActiveBatchRenderRef(%#v) error=nil", tc)
		}
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
	created := time.Date(2026, 7, 18, 9, 30, 0, 0, time.UTC)
	ref := &RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 1, CreatedAt: created, SequenceUnknown: true, PendingSequence: 2}
	sess, batch, err := m.MarkBatchRunning(key, frozen.ID, ref, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if sess.State != StateRunning || sess.ActiveBatch == nil || sess.ActiveBatch.State != InputRunning || sess.ActiveBatch.StartedAt != now.Add(time.Second) || sess.ActiveBatch.Inputs[0].State != InputRunning {
		t.Fatalf("running session = %#v", sess)
	}
	if batch == nil || batch.State != InputRunning || batch.RenderRef == nil || batch.RenderRef.CardID != "card" || !batch.RenderRef.CreatedAt.Equal(created) || !batch.RenderRef.SequenceUnknown || batch.RenderRef.PendingSequence != 2 {
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
	if sess.AgentSessionID != "" || sess.Model != "" || sess.Tokens != 0 || sess.WorkDir != "/new" {
		t.Fatalf("reset context = %#v", sess)
	}
	if len(sess.History) != 1 || sess.History[0].Text != "fresh prompt" {
		t.Fatalf("reset history = %#v", sess.History)
	}
	if sess.ActiveBatch == nil || len(sess.ActiveBatch.Inputs) != 1 || len(sess.Queue) != 1 || sess.Queue[0].ID != "later" {
		t.Fatalf("reset scheduling state = %#v", sess)
	}
}

func TestMarkBatchRunningPinsAndResetsBridgeInstructionsVersion(t *testing.T) {
	m := NewManagerWithStoreVersion("", "v2")
	key := Key{Agent: agent.Claude, ChatID: "bridge-version"}
	now := time.Unix(200, 0)
	start := func(input Input, at time.Time) (Session, *Batch) {
		t.Helper()
		input.State = InputQueued
		input.Time = at
		if _, _, err := m.EnqueueDurable(key, input, "/w", BatchLimits{}); err != nil {
			t.Fatal(err)
		}
		_, frozen, err := m.FreezeReadyBatch(key, at, BatchLimits{})
		if err != nil || frozen == nil {
			t.Fatalf("freeze batch=%#v err=%v", frozen, err)
		}
		sess, batch, err := m.MarkBatchRunning(key, frozen.ID, nil, at)
		if err != nil {
			t.Fatal(err)
		}
		return sess, batch
	}
	finish := func(batch *Batch, at time.Time) {
		t.Helper()
		if _, err := m.FinishBatch(key, batch.ID, BatchCompletion{Status: InputCompleted, AgentSessionID: "agent-session", At: at}); err != nil {
			t.Fatal(err)
		}
	}

	first, batch := start(Input{ID: "first", Text: "one", BridgeInstructionsVersion: "v1"}, now)
	if first.BridgeInstructionsVersion != "v1" || batch.Inputs[0].BridgeInstructionsVersion != "v1" {
		t.Fatalf("first version session=%q input=%q", first.BridgeInstructionsVersion, batch.Inputs[0].BridgeInstructionsVersion)
	}
	finish(batch, now.Add(time.Second))

	resumed, batch := start(Input{ID: "resume", Text: "two", BridgeInstructionsVersion: "v2"}, now.Add(2*time.Second))
	if resumed.BridgeInstructionsVersion != "v1" {
		t.Fatalf("resume changed version to %q", resumed.BridgeInstructionsVersion)
	}
	if batch.Inputs[0].BridgeInstructionsVersion != "v2" {
		t.Fatalf("queued input snapshot changed to %q", batch.Inputs[0].BridgeInstructionsVersion)
	}
	finish(batch, now.Add(3*time.Second))

	reset, batch := start(Input{ID: "reset", Text: "three", Reset: true, BridgeInstructionsVersion: "v2"}, now.Add(4*time.Second))
	if reset.BridgeInstructionsVersion != "v2" || reset.AgentSessionID != "" {
		t.Fatalf("reset session = %#v", reset)
	}
	if batch.Inputs[0].BridgeInstructionsVersion != "v2" {
		t.Fatalf("reset input version = %q", batch.Inputs[0].BridgeInstructionsVersion)
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
			sess, err := m.FinishBatch(key, frozen.ID, BatchCompletion{Status: status, AgentSessionID: "new-session", Model: "new-model", Tokens: 7, At: finishedAt})
			if err != nil {
				t.Fatal(err)
			}
			if sess.State != StateIdle || sess.ActiveBatch != nil || len(sess.Queue) != 1 || sess.Queue[0].ID != "later" {
				t.Fatalf("finished scheduling state = %#v", sess)
			}
			if sess.AgentSessionID != "new-session" || sess.Model != "new-model" || sess.Tokens != 11 || !sess.LastActive.Equal(finishedAt) {
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
	if reset.AgentSessionID != "" || reset.Model != "" || reset.Tokens != 0 {
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
	if after.AgentSessionID != "" {
		t.Fatalf("claude session = %q, want reset before next run", after.AgentSessionID)
	}
	if len(after.History) != 1 || after.History[0].Text != "second" {
		t.Fatalf("history = %#v, want only reset prompt", after.History)
	}
}

func TestManagerResumeSwitchesIdleBindingAndRestoresPinnedVersion(t *testing.T) {
	workDir := mustCanonicalWorkDir(t, t.TempDir())
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	entry := CatalogEntry{SessionID: "target", Agent: agent.Claude, WorkDir: workDir, UpdatedAt: now.Add(-time.Hour), BridgeInstructionsVersion: "v1", Summary: "old prompt"}
	if err := catalog.Upsert(entry); err != nil {
		t.Fatal(err)
	}
	manager := NewManagerWithStore(filepath.Join(t.TempDir(), "sessions.json"))
	manager.AttachCatalog(catalog)
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	manager.sessions[key.ID()] = &Session{
		Key: key, ID: key.ID(), WorkDir: workDir, AgentSessionID: "current",
		BridgeInstructionsVersion: "v2", State: StateIdle, Model: "model", Tokens: 9,
		History: []Prompt{{Text: "current prompt"}}, CreatedAt: now.Add(-time.Hour), LastActive: now.Add(-time.Minute),
	}

	got, err := manager.Resume(key, CatalogIdentity{Agent: agent.Claude, WorkDir: workDir}, "target", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentSessionID != "target" || got.BridgeInstructionsVersion != "v1" || got.WorkDir != workDir {
		t.Fatalf("resumed = %#v", got)
	}
	if got.State != StateIdle || got.ActiveBatch != nil || len(got.Queue) != 0 || len(got.History) != 0 || got.Model != "" || got.Tokens != 0 {
		t.Fatalf("stale scope state survived: %#v", got)
	}
	if !got.LastActive.Equal(now) {
		t.Fatalf("last active = %v, want %v", got.LastActive, now)
	}
	resolved, ok := catalog.Resolve(CatalogIdentity{Agent: agent.Claude, WorkDir: workDir}, "target")
	if !ok || !resolved.UpdatedAt.Equal(now) {
		t.Fatalf("catalog target = %#v, ok=%t", resolved, ok)
	}

	restored := NewManagerWithStore(filepath.Join(manager.storePath))
	if _, err := restored.Restore(); err != nil {
		t.Fatal(err)
	}
	stored, ok := restored.Get(key)
	if !ok || stored.AgentSessionID != "target" || stored.BridgeInstructionsVersion != "v1" {
		t.Fatalf("stored binding = %#v, ok=%t", stored, ok)
	}
}

func TestManagerResumeRejectsUnavailableMissingAndBusyScopes(t *testing.T) {
	workDir := mustCanonicalWorkDir(t, t.TempDir())
	identity := CatalogIdentity{Agent: agent.Claude, WorkDir: workDir}
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	manager := NewManager()
	if _, err := manager.Resume(key, identity, "target", time.Now()); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	manager.AttachCatalog(catalog)
	if _, err := manager.Resume(key, identity, "missing", time.Now()); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	entry := CatalogEntry{SessionID: "target", Agent: agent.Claude, WorkDir: workDir, UpdatedAt: time.Now(), BridgeInstructionsVersion: "v1"}
	if err := catalog.Upsert(entry); err != nil {
		t.Fatal(err)
	}
	for name, session := range map[string]*Session{
		"running": {Key: key, ID: key.ID(), WorkDir: workDir, State: StateRunning},
		"active":  {Key: key, ID: key.ID(), WorkDir: workDir, State: StateIdle, ActiveBatch: &Batch{ID: "active"}},
		"queued":  {Key: key, ID: key.ID(), WorkDir: workDir, State: StateIdle, Queue: []Input{{ID: "queued"}}},
	} {
		t.Run(name, func(t *testing.T) {
			manager.sessions[key.ID()] = cloneSession(session)
			before := *cloneSession(manager.sessions[key.ID()])
			if _, err := manager.Resume(key, identity, "target", time.Now()); !errors.Is(err, ErrSessionBusy) {
				t.Fatalf("busy error = %v", err)
			}
			after := manager.sessions[key.ID()]
			if after.AgentSessionID != before.AgentSessionID || len(after.Queue) != len(before.Queue) || (after.ActiveBatch == nil) != (before.ActiveBatch == nil) {
				t.Fatalf("busy session changed: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestManagerResumeSnapshotFailureDoesNotPublishBinding(t *testing.T) {
	workDir := mustCanonicalWorkDir(t, t.TempDir())
	identity := CatalogIdentity{Agent: agent.Claude, WorkDir: workDir}
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Upsert(CatalogEntry{SessionID: "target", Agent: agent.Claude, WorkDir: workDir, UpdatedAt: time.Now(), BridgeInstructionsVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
	manager := NewManagerWithStore(filepath.Join(t.TempDir(), "missing", "parent", "sessions.json"))
	manager.AttachCatalog(catalog)
	key := Key{Agent: agent.Claude, ChatID: "chat"}
	manager.sessions[key.ID()] = &Session{Key: key, ID: key.ID(), WorkDir: workDir, AgentSessionID: "current", State: StateIdle}
	manager.storePath = filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(manager.storePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.storePath = filepath.Join(manager.storePath, "sessions.json")

	if _, err := manager.Resume(key, identity, "target", time.Now()); err == nil {
		t.Fatal("resume succeeded with unwritable snapshot path")
	}
	got, _ := manager.Get(key)
	if got.AgentSessionID != "current" {
		t.Fatalf("failed resume published binding = %#v", got)
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

func TestScheduleInputsNeverBatchWithOtherInputs(t *testing.T) {
	ordinary := Input{WorkDir: "/tmp/work", RequestedModel: "sonnet", RequestedEffort: "low", AgentBin: "claude", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeTopic, BridgeInstructionsVersion: "v1"}
	scheduled := ordinary
	scheduled.ScheduleRunID = "cron:task:2026-07-21T09:00:00Z"
	scheduled.ScheduleTaskID = "task"
	if compatibleBatchInput(ordinary, scheduled) || compatibleBatchInput(scheduled, ordinary) || !compatibleBatchInput(scheduled, scheduled) {
		t.Fatal("scheduled input must match only its own run")
	}
}
