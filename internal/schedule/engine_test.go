package schedule

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type recordingDispatcher struct {
	store     *Store
	calls     int
	duplicate bool
	err       error
	seen      []Run
}

func (d *recordingDispatcher) Enqueue(_ context.Context, _ Task, run Run) (EnqueueResult, error) {
	d.calls++
	if d.store != nil {
		persisted, ok := d.store.Run(run.ID)
		if !ok || persisted.State != RunPending {
			return EnqueueResult{}, errors.New("run was not pending before dispatch")
		}
	}
	d.seen = append(d.seen, run)
	return EnqueueResult{Duplicate: d.duplicate}, d.err
}

type recordingNotifier struct{ messages []string }

func (n *recordingNotifier) Notify(_ context.Context, _ Task, message string) error {
	n.messages = append(n.messages, message)
	return nil
}

func TestEnginePersistsRunBeforeDispatch(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	store, task := confirmedTask(t, now, KindTimer, now.Add(time.Minute))
	dispatch := &recordingDispatcher{store: store}
	engine := NewEngine(store, dispatch, &recordingNotifier{}, EngineConfig{Now: func() time.Time { return now }})

	if err := engine.fire(context.Background(), task, task.NextRun, false); err != nil {
		t.Fatal(err)
	}
	if dispatch.calls != 1 {
		t.Fatalf("dispatch calls = %d", dispatch.calls)
	}
	run, ok := store.Run(RunID(task.Kind, task.ID, task.NextRun))
	if !ok || run.State != RunQueued {
		t.Fatalf("run = %#v, ok=%v", run, ok)
	}
}

func TestEngineRecoveryReconcilesPendingRunWithoutDuplicateExecution(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 2, 0, 0, time.UTC)
	store, task := confirmedTask(t, now.Add(-2*time.Minute), KindTimer, now.Add(-time.Minute))
	run := Run{ID: RunID(task.Kind, task.ID, task.NextRun), TaskID: task.ID, ScheduledAt: task.NextRun, State: RunPending, ClaimedAt: now.Add(-time.Minute)}
	if created, err := store.ClaimRun(run); err != nil || !created {
		t.Fatalf("claim: created=%v err=%v", created, err)
	}
	dispatch := &recordingDispatcher{store: store, duplicate: true}
	engine := NewEngine(store, dispatch, &recordingNotifier{}, EngineConfig{Now: func() time.Time { return now }, CatchUpWindow: 5 * time.Minute})

	if err := engine.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dispatch.calls != 1 {
		t.Fatalf("dispatch calls = %d", dispatch.calls)
	}
	got, _ := store.Run(run.ID)
	if got.State != RunQueued {
		t.Fatalf("reconciled state = %s", got.State)
	}
}

func TestEngineRecoveryMarksStaleTimerMissed(t *testing.T) {
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	store, task := confirmedTask(t, now.Add(-time.Hour), KindTimer, now.Add(-6*time.Minute))
	notify := &recordingNotifier{}
	dispatch := &recordingDispatcher{store: store}
	engine := NewEngine(store, dispatch, notify, EngineConfig{Now: func() time.Time { return now }, CatchUpWindow: 5 * time.Minute})

	if err := engine.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dispatch.calls != 0 {
		t.Fatalf("dispatch calls = %d", dispatch.calls)
	}
	run, ok := store.Run(RunID(task.Kind, task.ID, task.NextRun))
	if !ok || run.State != RunMissed {
		t.Fatalf("missed run = %#v ok=%v", run, ok)
	}
	if len(notify.messages) != 1 {
		t.Fatalf("notifications = %#v", notify.messages)
	}
}

func TestEngineSkipsOverlappingOccurrence(t *testing.T) {
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	store, task := confirmedTask(t, now.Add(-time.Hour), KindCron, now)
	active := Run{ID: RunID(task.Kind, task.ID, now.Add(-time.Minute)), TaskID: task.ID, ScheduledAt: now.Add(-time.Minute), State: RunPending}
	if created, err := store.ClaimRun(active); err != nil || !created {
		t.Fatalf("claim active: created=%v err=%v", created, err)
	}
	if err := store.UpdateRun(active.ID, RunQueued, "", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	dispatch := &recordingDispatcher{store: store}
	engine := NewEngine(store, dispatch, &recordingNotifier{}, EngineConfig{Now: func() time.Time { return now }})

	if err := engine.fire(context.Background(), task, now, false); err != nil {
		t.Fatal(err)
	}
	if dispatch.calls != 0 {
		t.Fatalf("dispatch calls = %d", dispatch.calls)
	}
	run, ok := store.Run(RunID(task.Kind, task.ID, now))
	if !ok || run.State != RunSkippedOverlap {
		t.Fatalf("overlap run = %#v ok=%v", run, ok)
	}
}

func TestEngineMarksDispatchFailure(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	store, task := confirmedTask(t, now, KindTimer, now.Add(time.Minute))
	dispatch := &recordingDispatcher{store: store, err: ErrQueueFull}
	engine := NewEngine(store, dispatch, &recordingNotifier{}, EngineConfig{Now: func() time.Time { return now }})

	if err := engine.fire(context.Background(), task, task.NextRun, false); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("fire error = %v", err)
	}
	run, _ := store.Run(RunID(task.Kind, task.ID, task.NextRun))
	if run.State != RunFailed || run.LastError == "" {
		t.Fatalf("failed run = %#v", run)
	}
}

func TestCronRecoveryWindowKeepsOnlyMostRecentOccurrence(t *testing.T) {
	now := time.Date(2026, 7, 21, 9, 3, 30, 0, time.UTC)
	task := Task{ID: "cron-task", Kind: KindCron, CronExpr: "* * * * *", Timezone: "UTC", NextRun: time.Date(2026, 7, 21, 8, 58, 0, 0, time.UTC)}
	latest, next, err := cronRecoveryWindow(task, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := latest.Format(time.RFC3339); got != "2026-07-21T09:03:00Z" {
		t.Fatalf("latest = %s", got)
	}
	if got := next.Format(time.RFC3339); got != "2026-07-21T09:04:00Z" {
		t.Fatalf("next = %s", got)
	}
}

func confirmedTask(t *testing.T, now time.Time, kind Kind, scheduledAt time.Time) (*Store, Task) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	draft := fixtureDraft()
	draft.Kind = kind
	draft.CreatedAt = now
	draft.ExpiresAt = now.Add(10 * time.Minute)
	draft.ScheduledAt = scheduledAt
	draft.Next = []time.Time{scheduledAt}
	if kind == KindCron {
		draft.CronExpr = "* * * * *"
		draft.ScheduledAt = time.Time{}
	}
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	task, err := store.ConfirmDraft(draft.ID, draft.Creator, now)
	if err != nil {
		t.Fatal(err)
	}
	return store, task
}
