package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePersistsConfirmedTaskWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "schedules.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	draft := fixtureDraft()
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	task, err := store.ConfirmDraft(draft.ID, draft.Creator, draft.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != draft.ID || !task.Enabled {
		t.Fatalf("confirmed task = %#v", task)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Task(draft.ID); !ok {
		t.Fatal("task was not restored")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o", got)
	}
	if dirInfo, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	} else if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o", got)
	}
}

func TestStoreRejectsCorruptAndUnsupportedSnapshots(t *testing.T) {
	for name, body := range map[string]string{
		"corrupt":     "{",
		"unsupported": `{"schema_version":99}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "schedules.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewStore(path); err == nil {
				t.Fatal("expected load error")
			}
		})
	}
}

func TestStoreDoesNotPublishMutationWhenPersistenceFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	draft := fixtureDraft()
	if err := store.CreateDraft(draft); err == nil {
		t.Fatal("expected persistence error")
	}
	if _, ok := store.Draft(draft.ID); ok {
		t.Fatal("failed candidate was published in memory")
	}
}

func TestStoreClaimsRunOnceAndTracksTransitions(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	draft := fixtureDraft()
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	task, err := store.ConfirmDraft(draft.ID, draft.Creator, draft.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	run := Run{ID: RunID(task.Kind, task.ID, task.NextRun), TaskID: task.ID, ScheduledAt: task.NextRun, State: RunPending}
	created, err := store.ClaimRun(run)
	if err != nil || !created {
		t.Fatalf("first claim: created=%v err=%v", created, err)
	}
	created, err = store.ClaimRun(run)
	if err != nil || created {
		t.Fatalf("duplicate claim: created=%v err=%v", created, err)
	}
	if err := store.UpdateRun(run.ID, RunQueued, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRun(run.ID, RunDelivered, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Run(run.ID); got.State != RunDelivered {
		t.Fatalf("run state = %s", got.State)
	}
	if err := store.UpdateRun(run.ID, RunRunning, "", time.Now()); err == nil || !strings.Contains(err.Error(), "transition") {
		t.Fatalf("terminal transition error = %v", err)
	}
}

func fixtureDraft() Draft {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	return Draft{
		ID:          "abc12345",
		Kind:        KindTimer,
		ScheduledAt: now.Add(time.Hour),
		Timezone:    "Asia/Shanghai",
		Description: "提醒开会",
		Prompt:      "提醒我参加项目会议",
		Creator:     "ou_creator",
		Target:      Target{ChatID: "oc_chat", ThreadID: "omt_topic", ReplyToMessageID: "om_msg"},
		Execution:   FrozenExecution{Agent: "claude", Model: "sonnet", Effort: "low", WorkDir: "/tmp/work", ReplyMode: "append", ConversationMode: "topic"},
		CreatedAt:   now,
		ExpiresAt:   now.Add(10 * time.Minute),
		Next:        []time.Time{now.Add(time.Hour)},
	}
}
