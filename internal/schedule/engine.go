package schedule

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrQueueFull = errors.New("schedule: queue is full")

type EnqueueResult struct {
	Duplicate bool
}

type Dispatcher interface {
	Enqueue(context.Context, Task, Run) (EnqueueResult, error)
}

type Notifier interface {
	Notify(context.Context, Task, string) error
}

type EngineConfig struct {
	Now              func() time.Time
	CatchUpWindow    time.Duration
	ExecutionTimeout time.Duration
	HistoryRetention time.Duration
}

type Engine struct {
	store    *Store
	dispatch Dispatcher
	notify   Notifier
	config   EngineConfig

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
	timers  map[string]*scheduledTimer
	wg      sync.WaitGroup
}

type scheduledTimer struct {
	timer *time.Timer
	once  sync.Once
	done  func()
}

func (t *scheduledTimer) finish() {
	if t != nil {
		t.once.Do(t.done)
	}
}

func NewEngine(store *Store, dispatch Dispatcher, notify Notifier, cfg EngineConfig) *Engine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CatchUpWindow <= 0 {
		cfg.CatchUpWindow = 5 * time.Minute
	}
	if cfg.ExecutionTimeout <= 0 {
		cfg.ExecutionTimeout = 30 * time.Minute
	}
	if cfg.HistoryRetention <= 0 {
		cfg.HistoryRetention = 30 * 24 * time.Hour
	}
	return &Engine{store: store, dispatch: dispatch, notify: notify, config: cfg, timers: map[string]*scheduledTimer{}}
}

func (e *Engine) Store() *Store { return e.store }

func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return nil
	}
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.started = true
	e.mu.Unlock()

	if err := e.Recover(e.ctx); err != nil {
		e.Stop()
		return err
	}
	for _, task := range e.store.Tasks() {
		if task.Enabled && !task.Archived && task.NextRun.After(e.now()) {
			e.register(task)
		}
	}
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return
	}
	e.started = false
	if e.cancel != nil {
		e.cancel()
	}
	for id, timer := range e.timers {
		if timer.timer.Stop() {
			timer.finish()
		}
		delete(e.timers, id)
	}
	e.mu.Unlock()
	e.wg.Wait()
}

func (e *Engine) Recover(ctx context.Context) error {
	now := e.now()
	if _, err := e.store.ExpireDrafts(now); err != nil {
		return err
	}

	for _, run := range e.store.Runs() {
		if run.State != RunPending {
			continue
		}
		task, ok := e.store.Task(run.TaskID)
		if !ok {
			if err := e.store.UpdateRun(run.ID, RunFailed, "task no longer exists", now); err != nil {
				return err
			}
			continue
		}
		if now.Sub(run.ScheduledAt) > e.config.CatchUpWindow {
			if err := e.store.UpdateRun(run.ID, RunMissed, "catch-up window exceeded", now); err != nil {
				return err
			}
			continue
		}
		if err := e.dispatchPending(ctx, task, run); err != nil {
			return err
		}
	}

	for _, task := range e.store.Tasks() {
		if !task.Enabled || task.Archived || task.NextRun.IsZero() || task.NextRun.After(now) {
			continue
		}
		switch task.Kind {
		case KindTimer:
			if _, exists := e.store.Run(RunID(task.Kind, task.ID, task.NextRun)); exists {
				continue
			}
			if now.Sub(task.NextRun) <= e.config.CatchUpWindow {
				if err := e.fire(ctx, task, task.NextRun, false); err != nil && !errors.Is(err, ErrQueueFull) {
					return err
				}
			} else if err := e.markMissed(ctx, task, task.NextRun, "one-shot task missed its catch-up window"); err != nil {
				return err
			}
			if _, err := e.store.UpdateTaskSchedule(task.ID, time.Time{}, true); err != nil {
				return err
			}
		case KindCron:
			latest, next, err := cronRecoveryWindow(task, now)
			if err != nil {
				return err
			}
			if !latest.IsZero() {
				if now.Sub(latest) <= e.config.CatchUpWindow {
					if err := e.fire(ctx, task, latest, false); err != nil && !errors.Is(err, ErrQueueFull) {
						return err
					}
				} else if err := e.markMissed(ctx, task, latest, "recurring task missed its catch-up window"); err != nil {
					return err
				}
			}
			if _, err := e.store.UpdateTaskSchedule(task.ID, next, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) fire(ctx context.Context, task Task, scheduledAt time.Time, manual bool) error {
	run := Run{
		ID: RunID(task.Kind, task.ID, scheduledAt), TaskID: task.ID, ScheduledAt: scheduledAt,
		ClaimedAt: e.now(), State: RunPending, Manual: manual,
	}
	if _, active := e.store.ActiveRun(task.ID, run.ID); active {
		run.State = RunSkippedOverlap
		created, err := e.store.ClaimRun(run)
		if err != nil || !created {
			return err
		}
		return nil
	}
	created, err := e.store.ClaimRun(run)
	if err != nil || !created {
		return err
	}
	return e.dispatchPending(ctx, task, run)
}

func (e *Engine) dispatchPending(ctx context.Context, task Task, run Run) error {
	if e.dispatch == nil {
		err := errors.New("schedule dispatcher is not configured")
		_ = e.store.UpdateRun(run.ID, RunFailed, err.Error(), e.now())
		return err
	}
	_, err := e.dispatch.Enqueue(ctx, task, run)
	if err != nil {
		if updateErr := e.store.UpdateRun(run.ID, RunFailed, err.Error(), e.now()); updateErr != nil {
			return errors.Join(err, updateErr)
		}
		return err
	}
	return e.store.UpdateRun(run.ID, RunQueued, "", e.now())
}

func (e *Engine) markMissed(ctx context.Context, task Task, scheduledAt time.Time, reason string) error {
	run := Run{
		ID: RunID(task.Kind, task.ID, scheduledAt), TaskID: task.ID, ScheduledAt: scheduledAt,
		ClaimedAt: e.now(), State: RunMissed, LastError: reason, CompletedAt: e.now(),
	}
	created, err := e.store.ClaimRun(run)
	if err != nil || !created {
		return err
	}
	if e.notify != nil {
		_ = e.notify.Notify(ctx, task, fmt.Sprintf("定时任务 %s 未执行：%s", task.ID, reason))
	}
	return nil
}

func (e *Engine) Add(task Task) {
	e.mu.Lock()
	started := e.started
	e.mu.Unlock()
	if started && task.Enabled && !task.Archived {
		e.register(task)
	}
}

func (e *Engine) Remove(id string) error {
	e.cancelTimer(id)
	return e.store.DeleteTask(id)
}

func (e *Engine) SetEnabled(id string, enabled bool) (Task, error) {
	task, err := e.store.SetTaskEnabled(id, enabled)
	if err != nil {
		return Task{}, err
	}
	if enabled {
		e.register(task)
	} else {
		e.cancelTimer(id)
	}
	return task, nil
}

func (e *Engine) RunNow(ctx context.Context, id string) error {
	task, ok := e.store.Task(id)
	if !ok {
		return fmt.Errorf("task %q not found", id)
	}
	return e.fire(ctx, task, e.now(), true)
}

func (e *Engine) MarkRunning(runID string, at time.Time) error {
	return e.store.UpdateRun(runID, RunRunning, "", at)
}

func (e *Engine) Complete(runID string, err error, deliveryUnknown bool, at time.Time) error {
	if deliveryUnknown {
		return e.store.UpdateRun(runID, RunDeliveryUnknown, errorText(err), at)
	}
	if err != nil {
		return e.store.UpdateRun(runID, RunFailed, err.Error(), at)
	}
	return e.store.UpdateRun(runID, RunDelivered, "", at)
}

func (e *Engine) register(task Task) {
	now := e.now()
	when := task.NextRun
	if when.IsZero() {
		return
	}
	delay := when.Sub(now)
	if delay < 0 {
		delay = 0
	}
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return
	}
	if old := e.timers[task.ID]; old != nil {
		if old.timer.Stop() {
			old.finish()
		}
	}
	e.wg.Add(1)
	entry := &scheduledTimer{done: e.wg.Done}
	entry.timer = time.AfterFunc(delay, func() {
		defer entry.finish()
		e.mu.Lock()
		ctx := e.ctx
		delete(e.timers, task.ID)
		started := e.started
		e.mu.Unlock()
		if !started || ctx == nil || ctx.Err() != nil {
			return
		}
		_ = e.fire(ctx, task, when, false)
		if task.Kind == KindTimer {
			_, _ = e.store.UpdateTaskSchedule(task.ID, time.Time{}, true)
			return
		}
		schedule, err := parseCron(task.CronExpr, task.Timezone)
		if err != nil {
			return
		}
		next := schedule.Next(when)
		updated, err := e.store.UpdateTaskSchedule(task.ID, next, false)
		if err == nil {
			e.register(updated)
		}
	})
	e.timers[task.ID] = entry
	e.mu.Unlock()
}

func (e *Engine) cancelTimer(id string) {
	e.mu.Lock()
	if timer := e.timers[id]; timer != nil {
		if timer.timer.Stop() {
			timer.finish()
		}
		delete(e.timers, id)
	}
	e.mu.Unlock()
}

func (e *Engine) now() time.Time { return e.config.Now() }

func cronRecoveryWindow(task Task, now time.Time) (latest, next time.Time, err error) {
	schedule, err := parseCron(task.CronExpr, task.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	cursor := task.NextRun
	if cursor.IsZero() {
		cursor = schedule.Next(now.Add(-time.Minute))
	}
	for i := 0; i < 1_000_000 && !cursor.After(now); i++ {
		latest = cursor
		candidate := schedule.Next(cursor)
		if !candidate.After(cursor) {
			return time.Time{}, time.Time{}, fmt.Errorf("cron schedule did not advance for task %q", task.ID)
		}
		cursor = candidate
	}
	if !cursor.After(now) {
		return time.Time{}, time.Time{}, fmt.Errorf("cron catch-up scan limit exceeded for task %q", task.ID)
	}
	return latest, cursor, nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
