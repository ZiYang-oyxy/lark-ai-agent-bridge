package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const SnapshotVersion = 1

type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      uint64    `json:"revision"`
	SavedAt       time.Time `json:"saved_at"`
	Tasks         []Task    `json:"tasks"`
	Drafts        []Draft   `json:"drafts,omitempty"`
	Runs          []Run     `json:"runs,omitempty"`
}

type Store struct {
	mu       sync.Mutex
	path     string
	snapshot Snapshot
}

func NewStore(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("schedule: empty store path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create schedule store directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("protect schedule store directory: %w", err)
	}
	s := &Store{path: path, snapshot: Snapshot{SchemaVersion: SnapshotVersion}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := s.persist(s.snapshot); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read schedule snapshot: %w", err)
	}
	if err := json.Unmarshal(data, &s.snapshot); err != nil {
		return nil, fmt.Errorf("decode schedule snapshot: %w", err)
	}
	if s.snapshot.SchemaVersion != SnapshotVersion {
		return nil, fmt.Errorf("unsupported schedule snapshot schema %d", s.snapshot.SchemaVersion)
	}
	s.snapshot = cloneSnapshot(s.snapshot)
	return s, nil
}

func (s *Store) CreateDraft(draft Draft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if draft.ID == "" {
		return fmt.Errorf("draft id is required")
	}
	if _, ok := draftIndex(s.snapshot.Drafts, draft.ID); ok {
		return fmt.Errorf("draft %q already exists", draft.ID)
	}
	if _, ok := taskIndex(s.snapshot.Tasks, draft.ID); ok {
		return fmt.Errorf("task %q already exists", draft.ID)
	}
	candidate := cloneSnapshot(s.snapshot)
	candidate.Drafts = append(candidate.Drafts, cloneDraft(draft))
	return s.publishLocked(candidate)
}

func (s *Store) ConfirmDraft(id, actor string, now time.Time) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.confirmDraftLocked(id, actor, now, false)
}

func (s *Store) AutoConfirmDraft(id string, now time.Time) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.confirmDraftLocked(id, "", now, true)
}

func (s *Store) confirmDraftLocked(id, actor string, now time.Time, automatic bool) (Task, error) {
	idx, ok := draftIndex(s.snapshot.Drafts, id)
	if !ok {
		return Task{}, fmt.Errorf("draft %q not found", id)
	}
	draft := cloneDraft(s.snapshot.Drafts[idx])
	if !automatic && actor != draft.Creator {
		return Task{}, fmt.Errorf("draft %q is owned by another user", id)
	}
	if automatic && (draft.AutoConfirmAt.IsZero() || now.Before(draft.AutoConfirmAt)) {
		return Task{}, fmt.Errorf("draft %q is not due for auto-confirmation", id)
	}
	if !automatic && !draft.ExpiresAt.IsZero() && now.After(draft.ExpiresAt) {
		return Task{}, fmt.Errorf("draft %q expired", id)
	}
	task := Task{
		ID: draft.ID, Kind: draft.Kind, CronExpr: draft.CronExpr, ScheduledAt: draft.ScheduledAt,
		Timezone: draft.Timezone, Description: draft.Description, Prompt: draft.Prompt,
		Creator: draft.Creator, Target: draft.Target, Execution: draft.Execution,
		Enabled: true, AutoConfirmed: automatic, AutoConfirmNoticePending: automatic,
		CreatedAt: draft.CreatedAt, ConfirmedAt: now,
	}
	if len(draft.Next) > 0 {
		task.NextRun = draft.Next[0]
	}
	candidate := cloneSnapshot(s.snapshot)
	candidate.Drafts = append(candidate.Drafts[:idx], candidate.Drafts[idx+1:]...)
	candidate.Tasks = append(candidate.Tasks, task)
	if err := s.publishLocked(candidate); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) MarkAutoConfirmNotified(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := taskIndex(s.snapshot.Tasks, id)
	if !ok {
		return fmt.Errorf("task %q not found", id)
	}
	if !s.snapshot.Tasks[idx].AutoConfirmNoticePending {
		return nil
	}
	candidate := cloneSnapshot(s.snapshot)
	candidate.Tasks[idx].AutoConfirmNoticePending = false
	return s.publishLocked(candidate)
}

func (s *Store) SetDraftAutoConfirmAt(id string, at time.Time) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := draftIndex(s.snapshot.Drafts, id)
	if !ok {
		return Draft{}, fmt.Errorf("draft %q not found", id)
	}
	candidate := cloneSnapshot(s.snapshot)
	candidate.Drafts[idx].AutoConfirmAt = at
	if err := s.publishLocked(candidate); err != nil {
		return Draft{}, err
	}
	return cloneDraft(candidate.Drafts[idx]), nil
}

func (s *Store) CancelDraft(id, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := draftIndex(s.snapshot.Drafts, id)
	if !ok {
		return fmt.Errorf("draft %q not found", id)
	}
	if actor != s.snapshot.Drafts[idx].Creator {
		return fmt.Errorf("draft %q is owned by another user", id)
	}
	candidate := cloneSnapshot(s.snapshot)
	candidate.Drafts = append(candidate.Drafts[:idx], candidate.Drafts[idx+1:]...)
	return s.publishLocked(candidate)
}

func (s *Store) ClaimRun(run Run) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := runIndex(s.snapshot.Runs, run.ID); ok {
		return false, nil
	}
	if _, ok := taskIndex(s.snapshot.Tasks, run.TaskID); !ok {
		return false, fmt.Errorf("task %q not found", run.TaskID)
	}
	if run.State == "" {
		run.State = RunPending
	}
	if run.ClaimedAt.IsZero() {
		run.ClaimedAt = time.Now()
	}
	candidate := cloneSnapshot(s.snapshot)
	candidate.Runs = append(candidate.Runs, run)
	if idx, ok := taskIndex(candidate.Tasks, run.TaskID); ok {
		candidate.Tasks[idx].LastRunID = run.ID
	}
	if err := s.publishLocked(candidate); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) UpdateRun(id string, next RunState, lastError string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := runIndex(s.snapshot.Runs, id)
	if !ok {
		return fmt.Errorf("run %q not found", id)
	}
	current := s.snapshot.Runs[idx].State
	if !validRunTransition(current, next) {
		return fmt.Errorf("invalid run state transition %s -> %s", current, next)
	}
	candidate := cloneSnapshot(s.snapshot)
	run := &candidate.Runs[idx]
	run.State = next
	run.LastError = lastError
	switch next {
	case RunQueued:
		run.QueuedAt = at
	case RunRunning:
		run.StartedAt = at
	default:
		if terminalRunState(next) {
			run.CompletedAt = at
		}
	}
	return s.publishLocked(candidate)
}

func validRunTransition(current, next RunState) bool {
	if current == next {
		return true
	}
	if terminalRunState(current) {
		return false
	}
	switch current {
	case RunPending:
		return next == RunQueued || next == RunRunning || terminalRunState(next)
	case RunQueued:
		return next == RunRunning || terminalRunState(next)
	case RunRunning:
		return terminalRunState(next)
	default:
		return false
	}
}

func (s *Store) Draft(id string) (Draft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := draftIndex(s.snapshot.Drafts, id)
	if !ok {
		return Draft{}, false
	}
	return cloneDraft(s.snapshot.Drafts[idx]), true
}

func (s *Store) DraftByOrigin(originRunID string) (Draft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, draft := range s.snapshot.Drafts {
		if draft.OriginRunID == originRunID {
			return cloneDraft(draft), true
		}
	}
	return Draft{}, false
}

func (s *Store) Task(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := taskIndex(s.snapshot.Tasks, id)
	if !ok {
		return Task{}, false
	}
	return s.snapshot.Tasks[idx], true
}

func (s *Store) Run(id string) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := runIndex(s.snapshot.Runs, id)
	if !ok {
		return Run{}, false
	}
	return s.snapshot.Runs[idx], true
}

func (s *Store) Tasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Task(nil), s.snapshot.Tasks...)
}

func (s *Store) Drafts() []Draft {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Draft, len(s.snapshot.Drafts))
	for i := range s.snapshot.Drafts {
		out[i] = cloneDraft(s.snapshot.Drafts[i])
	}
	return out
}

func (s *Store) Runs() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Run(nil), s.snapshot.Runs...)
}

func (s *Store) SetTaskEnabled(id string, enabled bool) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := taskIndex(s.snapshot.Tasks, id)
	if !ok {
		return Task{}, fmt.Errorf("task %q not found", id)
	}
	candidate := cloneSnapshot(s.snapshot)
	candidate.Tasks[idx].Enabled = enabled
	if err := s.publishLocked(candidate); err != nil {
		return Task{}, err
	}
	return candidate.Tasks[idx], nil
}

func (s *Store) UpdateTaskSchedule(id string, next time.Time, archived bool) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := taskIndex(s.snapshot.Tasks, id)
	if !ok {
		return Task{}, fmt.Errorf("task %q not found", id)
	}
	candidate := cloneSnapshot(s.snapshot)
	candidate.Tasks[idx].NextRun = next
	candidate.Tasks[idx].Archived = archived
	if archived {
		candidate.Tasks[idx].Enabled = false
	}
	if err := s.publishLocked(candidate); err != nil {
		return Task{}, err
	}
	return candidate.Tasks[idx], nil
}

func (s *Store) ActiveRun(taskID, exceptID string) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.snapshot.Runs {
		if run.TaskID == taskID && run.ID != exceptID && (run.State == RunPending || run.State == RunQueued || run.State == RunRunning) {
			return run, true
		}
	}
	return Run{}, false
}

func (s *Store) ExpireDrafts(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := cloneSnapshot(s.snapshot)
	kept := candidate.Drafts[:0]
	for _, draft := range candidate.Drafts {
		if draft.ExpiresAt.IsZero() || draft.ExpiresAt.After(now) {
			kept = append(kept, draft)
		}
	}
	expired := len(candidate.Drafts) - len(kept)
	if expired == 0 {
		return 0, nil
	}
	candidate.Drafts = kept
	if err := s.publishLocked(candidate); err != nil {
		return 0, err
	}
	return expired, nil
}

// PruneHistory removes terminal runs and their archived one-shot tasks after
// the retention cutoff. Active and recurring tasks are never removed.
func (s *Store) PruneHistory(cutoff time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := cloneSnapshot(s.snapshot)
	removedRunIDs := make(map[string]struct{})
	keptRuns := candidate.Runs[:0]
	for _, run := range candidate.Runs {
		if terminalRunState(run.State) && !run.CompletedAt.IsZero() && run.CompletedAt.Before(cutoff) {
			removedRunIDs[run.ID] = struct{}{}
			continue
		}
		keptRuns = append(keptRuns, run)
	}
	candidate.Runs = keptRuns
	keptTasks := candidate.Tasks[:0]
	for _, task := range candidate.Tasks {
		_, lastRunRemoved := removedRunIDs[task.LastRunID]
		if task.Kind == KindTimer && task.Archived && lastRunRemoved {
			continue
		}
		keptTasks = append(keptTasks, task)
	}
	candidate.Tasks = keptTasks
	removed := len(s.snapshot.Runs) - len(candidate.Runs) + len(s.snapshot.Tasks) - len(candidate.Tasks)
	if removed == 0 {
		return 0, nil
	}
	if err := s.publishLocked(candidate); err != nil {
		return 0, err
	}
	return removed, nil
}

func (s *Store) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := taskIndex(s.snapshot.Tasks, id)
	if !ok {
		return fmt.Errorf("task %q not found", id)
	}
	candidate := cloneSnapshot(s.snapshot)
	candidate.Tasks = append(candidate.Tasks[:idx], candidate.Tasks[idx+1:]...)
	return s.publishLocked(candidate)
}

func (s *Store) publishLocked(candidate Snapshot) error {
	candidate.SchemaVersion = SnapshotVersion
	candidate.Revision = s.snapshot.Revision + 1
	candidate.SavedAt = time.Now().UTC()
	if err := s.persist(candidate); err != nil {
		return err
	}
	s.snapshot = cloneSnapshot(candidate)
	return nil
}

func (s *Store) persist(snapshot Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode schedule snapshot: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".schedules-*.tmp")
	if err != nil {
		return fmt.Errorf("create schedule snapshot temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect schedule snapshot temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write schedule snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync schedule snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close schedule snapshot: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace schedule snapshot: %w", err)
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Tasks = append([]Task(nil), in.Tasks...)
	out.Runs = append([]Run(nil), in.Runs...)
	out.Drafts = make([]Draft, len(in.Drafts))
	for i := range in.Drafts {
		out.Drafts[i] = cloneDraft(in.Drafts[i])
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].ID < out.Tasks[j].ID })
	sort.Slice(out.Drafts, func(i, j int) bool { return out.Drafts[i].ID < out.Drafts[j].ID })
	sort.Slice(out.Runs, func(i, j int) bool { return out.Runs[i].ID < out.Runs[j].ID })
	return out
}

func cloneDraft(in Draft) Draft {
	out := in
	out.Next = append([]time.Time(nil), in.Next...)
	return out
}

func draftIndex(values []Draft, id string) (int, bool) {
	for i := range values {
		if values[i].ID == id {
			return i, true
		}
	}
	return 0, false
}

func taskIndex(values []Task, id string) (int, bool) {
	for i := range values {
		if values[i].ID == id {
			return i, true
		}
	}
	return 0, false
}

func runIndex(values []Run, id string) (int, bool) {
	for i := range values {
		if values[i].ID == id {
			return i, true
		}
	}
	return 0, false
}
