package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lark-agent-bridge/internal/actiongrant"
	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/bridgeinstructions"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/schedule"
	"lark-agent-bridge/internal/session"
)

func TestScheduleConfirmationActionPromotesDraft(t *testing.T) {
	service, renderer, store := scheduleTestService(t)
	draft := bridgeFixtureDraft(time.Now())
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	result, err := service.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "confirm-card", ActionID: "schedule.confirm", Value: draft.ID, Actor: draft.Creator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Task(draft.ID); !ok {
		t.Fatal("confirmed task not persisted")
	}
	if result.Event == nil || result.Event.Type != "schedule_confirmed" {
		t.Fatalf("action result = %#v", result)
	}
	if events := renderer.Events(); len(events) != 1 || !strings.Contains(events[0].Segments[0].Text, draft.ID) {
		t.Fatalf("events = %#v", events)
	}
}

func TestScheduleAutoConfirmationRecoversFromPersistedDeadline(t *testing.T) {
	service, renderer, store := scheduleTestService(t)
	now := time.Now()
	draft := bridgeFixtureDraft(now)
	draft.AutoConfirmAt = now.Add(-time.Second)
	draft.ExpiresAt = now.Add(-time.Minute)
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}

	if err := service.ProcessScheduleConfirmations(now); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Task(draft.ID); !ok {
		t.Fatal("expired persisted deadline was not auto-confirmed")
	}
	if _, ok := store.Draft(draft.ID); ok {
		t.Fatal("auto-confirmed draft still exists")
	}
	events := renderer.Events()
	if len(events) != 1 || !strings.Contains(segmentText(events[0]), "已自动确认") {
		t.Fatalf("recovery event = %#v", events)
	}
	if err := service.ProcessScheduleConfirmations(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Tasks()); got != 1 {
		t.Fatalf("repeated processing created %d tasks, want 1", got)
	}
	if _, err := store.ExpireDrafts(now); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Task(draft.ID); !ok {
		t.Fatal("startup draft expiry removed recovered task")
	}
}

func TestScheduleAutoConfirmationFallsBackWhenCardUpdateFails(t *testing.T) {
	renderer := &selectiveScheduleRenderer{failTypes: map[string]bool{"schedule_confirmed": true}}
	service, store := scheduleTestServiceWithRenderer(t, renderer)
	now := time.Now()
	draft := bridgeFixtureDraft(now)
	draft.AutoConfirmAt = now.Add(-time.Second)
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	service.scheduleConfirms[draft.ID] = pendingScheduleConfirmation{event: card.Event{SessionID: "confirm-card", ReplyToMessageID: "om_origin"}}

	if err := service.ProcessScheduleConfirmations(now); err != nil {
		t.Fatal(err)
	}
	task, ok := store.Task(draft.ID)
	if !ok || task.AutoConfirmNoticePending {
		t.Fatalf("task notification state = %#v", task)
	}
	events := renderer.Events()
	if len(events) != 1 || events[0].Type != "message" || !strings.Contains(segmentText(events[0]), "已自动确认") {
		t.Fatalf("fallback events = %#v", events)
	}
}

func TestScheduleAutoConfirmationRetriesPendingNotice(t *testing.T) {
	renderer := &selectiveScheduleRenderer{failAll: true}
	service, store := scheduleTestServiceWithRenderer(t, renderer)
	now := time.Now()
	draft := bridgeFixtureDraft(now)
	draft.AutoConfirmAt = now.Add(-time.Second)
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}

	if err := service.ProcessScheduleConfirmations(now); err != nil {
		t.Fatal(err)
	}
	task, ok := store.Task(draft.ID)
	if !ok || !task.AutoConfirmNoticePending {
		t.Fatalf("failed delivery was not persisted = %#v", task)
	}
	renderer.SetFailAll(false)
	service.scheduleConfirms = map[string]pendingScheduleConfirmation{}
	if err := service.ProcessScheduleConfirmations(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	task, _ = store.Task(draft.ID)
	if task.AutoConfirmNoticePending {
		t.Fatal("successful retry did not clear pending notification")
	}
	if events := renderer.Events(); len(events) != 1 || !strings.Contains(segmentText(events[0]), "已自动确认") {
		t.Fatalf("retry events = %#v", events)
	}
}

func TestScheduleCancellationDoesNotWaitForCountdownRender(t *testing.T) {
	renderer := newBlockingScheduleRenderer()
	service, store := scheduleTestServiceWithRenderer(t, renderer)
	now := time.Now()
	draft := bridgeFixtureDraft(now)
	draft.AutoConfirmAt = now.Add(time.Minute)
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	service.scheduleConfirms[draft.ID] = pendingScheduleConfirmation{event: card.Event{Type: "schedule_confirmation", SessionID: "confirm-card"}, lastRemaining: 60}

	processed := make(chan error, 1)
	go func() { processed <- service.ProcessScheduleConfirmations(now.Add(time.Second)) }()
	select {
	case <-renderer.started:
	case <-time.After(time.Second):
		t.Fatal("countdown render did not start")
	}
	cancelled := make(chan error, 1)
	go func() { cancelled <- service.cancelScheduleDraft(draft.ID, draft.Creator) }()
	select {
	case err := <-cancelled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("cancellation blocked behind countdown render")
	}
	close(renderer.release)
	if err := <-processed; err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Task(draft.ID); ok {
		t.Fatal("cancelled draft became a task")
	}
}

func TestScheduleCancellationWinsBeforeAutoConfirmation(t *testing.T) {
	service, _, store := scheduleTestService(t)
	now := time.Now()
	draft := bridgeFixtureDraft(now)
	draft.AutoConfirmAt = now.Add(time.Second)
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	if err := service.cancelScheduleDraft(draft.ID, draft.Creator); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessScheduleConfirmations(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Task(draft.ID); ok {
		t.Fatal("cancelled draft was created by a late auto-confirm tick")
	}
}

func TestScheduleConfirmAndCancelRaceHasSingleWinner(t *testing.T) {
	service, _, store := scheduleTestService(t)
	now := time.Now()
	draft := bridgeFixtureDraft(now)
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := service.confirmScheduleDraft(draft.ID, draft.Creator, now)
		results <- err
	}()
	go func() {
		<-start
		results <- service.cancelScheduleDraft(draft.ID, draft.Creator)
	}()
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful terminal transitions = %d, want 1", successes)
	}
	if _, ok := store.Draft(draft.ID); ok {
		t.Fatal("terminal transition left draft pending")
	}
	if got := len(store.Tasks()); got > 1 {
		t.Fatalf("race created %d tasks, want at most 1", got)
	}
}

func TestCronConfirmAndCancelCommandsAreScoped(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		text      string
		mutate    func(*schedule.Draft)
		wantTask  bool
		wantDraft bool
	}{
		{name: "confirm matching id", text: "confirm abcd1234", wantTask: true},
		{name: "cancel matching id", text: "cancel abcd1234", wantDraft: false},
		{name: "reject other chat", text: "confirm abcd1234", mutate: func(d *schedule.Draft) { d.Target.ChatID = "oc_other" }, wantDraft: true},
		{name: "reject other thread", text: "confirm abcd1234", mutate: func(d *schedule.Draft) { d.Target.ThreadID = "omt_other" }, wantDraft: true},
		{name: "reject timer kind", text: "confirm abcd1234", mutate: func(d *schedule.Draft) { d.Kind = schedule.KindTimer }, wantDraft: true},
		{name: "reject expired", text: "confirm abcd1234", mutate: func(d *schedule.Draft) { d.ExpiresAt = now.Add(-time.Minute) }, wantDraft: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, store := scheduleTestService(t)
			draft := bridgeFixtureDraft(now)
			draft.Kind = schedule.KindCron
			draft.CronExpr = "0 9 * * *"
			draft.ScheduledAt = time.Time{}
			if tc.mutate != nil {
				tc.mutate(&draft)
			}
			if err := store.CreateDraft(draft); err != nil {
				t.Fatal(err)
			}
			msg := Message{ID: "cmd", Sender: "ou_creator", ChatID: "oc_chat", ThreadID: "omt_topic", Time: now}
			if err := svc.handleScheduleCommand(context.Background(), msg, Command{Type: CommandCron, Text: tc.text}, config.RuntimePreference{}); err != nil {
				t.Fatal(err)
			}
			_, taskOK := store.Task(draft.ID)
			_, draftOK := store.Draft(draft.ID)
			if taskOK != tc.wantTask || draftOK != tc.wantDraft {
				t.Fatalf("task=%v draft=%v", taskOK, draftOK)
			}
		})
	}
}

func TestCronConfirmWithoutIDRequiresOneScopedDraft(t *testing.T) {
	now := time.Now()
	t.Run("unique", func(t *testing.T) {
		svc, _, store := scheduleTestService(t)
		draft := bridgeFixtureDraft(now)
		draft.Kind = schedule.KindCron
		draft.CronExpr = "0 9 * * *"
		draft.ScheduledAt = time.Time{}
		if err := store.CreateDraft(draft); err != nil {
			t.Fatal(err)
		}
		msg := Message{ID: "unique", Sender: draft.Creator, ChatID: draft.Target.ChatID, ThreadID: draft.Target.ThreadID, Time: now}
		if err := svc.handleScheduleCommand(context.Background(), msg, Command{Type: CommandCron, Text: "confirm"}, config.RuntimePreference{}); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.Task(draft.ID); !ok {
			t.Fatal("unique scoped draft was not confirmed")
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		svc, renderer, store := scheduleTestService(t)
		for i, id := range []string{"draft-a", "draft-b"} {
			draft := bridgeFixtureDraft(now.Add(time.Duration(i) * time.Second))
			draft.ID = id
			draft.Kind = schedule.KindCron
			draft.CronExpr = "0 9 * * *"
			draft.ScheduledAt = time.Time{}
			if err := store.CreateDraft(draft); err != nil {
				t.Fatal(err)
			}
		}
		msg := Message{ID: "ambiguous", Sender: "ou_creator", ChatID: "oc_chat", ThreadID: "omt_topic", Time: now}
		if err := svc.handleScheduleCommand(context.Background(), msg, Command{Type: CommandCron, Text: "confirm"}, config.RuntimePreference{}); err != nil {
			t.Fatal(err)
		}
		if len(store.Tasks()) != 0 || len(store.Drafts()) != 2 {
			t.Fatalf("ambiguous command changed store: tasks=%d drafts=%d", len(store.Tasks()), len(store.Drafts()))
		}
		events := renderer.Events()
		if len(events) == 0 || !strings.Contains(segmentText(events[len(events)-1]), "指定草稿 ID") {
			t.Fatalf("ambiguous response = %#v", events)
		}
	})
}

func TestScheduleConfirmationActionRejectsOtherUser(t *testing.T) {
	service, _, store := scheduleTestService(t)
	draft := bridgeFixtureDraft(time.Now())
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "confirm-card", ActionID: "schedule.confirm", Value: draft.ID, Actor: "ou_other",
	}); err == nil {
		t.Fatal("expected ownership error")
	}
	if _, ok := store.Task(draft.ID); ok {
		t.Fatal("unauthorized confirmation created task")
	}
}

func TestExactTextConfirmationRequiresOneDraftInScope(t *testing.T) {
	service, renderer, store := scheduleTestService(t)
	draft := bridgeFixtureDraft(time.Now())
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	msg := Message{ID: "om_confirm", ChatID: draft.Target.ChatID, ThreadID: draft.Target.ThreadID, Sender: draft.Creator, Text: "确认", Time: time.Now()}
	if err := service.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Task(draft.ID); !ok {
		t.Fatal("text confirmation did not create task")
	}
	events := renderer.Events()
	if len(events) != 1 || !strings.Contains(events[0].Segments[0].Text, "已创建") {
		t.Fatalf("events = %#v", events)
	}
}

func TestCronListIsScopedToCurrentConversation(t *testing.T) {
	service, renderer, store := scheduleTestService(t)
	draft := bridgeFixtureDraft(time.Now())
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmDraft(draft.ID, draft.Creator, time.Now()); err != nil {
		t.Fatal(err)
	}
	other := bridgeFixtureDraft(time.Now())
	other.ID = "other123"
	other.Target.ChatID = "oc_other"
	if err := store.CreateDraft(other); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmDraft(other.ID, other.Creator, time.Now()); err != nil {
		t.Fatal(err)
	}
	msg := Message{ID: "om_list", ChatID: draft.Target.ChatID, ThreadID: draft.Target.ThreadID, Sender: draft.Creator, Text: "/timer", Time: time.Now()}
	if err := service.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if len(events) != 1 || !strings.Contains(events[0].Segments[0].Text, draft.ID) || strings.Contains(events[0].Segments[0].Text, other.ID) {
		t.Fatalf("scoped list = %#v", events)
	}
}

func TestTimerAddIsNotDeduplicatedBeforeAgentEnqueue(t *testing.T) {
	service, _, _ := scheduleTestService(t)
	now := time.Now()
	msg := Message{ID: "om_timer_add", ChatID: "oc_chat", ThreadID: "omt_topic", Sender: "ou_creator", Text: "/timer add 明天下午三点提醒我评审", Time: now}
	if err := service.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	key := session.Key{Agent: agent.Claude, ChatID: msg.ChatID, Thread: "schedule-proposal:" + msg.ThreadID}
	sess, ok := service.Sessions.Get(key)
	if !ok || len(sess.Queue) != 1 {
		t.Fatalf("timer add was not queued: session=%#v ok=%v", sess, ok)
	}
	if sess.Queue[0].ID != msg.ID || sess.Queue[0].ScheduleKind != string(schedule.KindTimer) || !sess.Queue[0].Reset {
		t.Fatalf("queued schedule input = %#v", sess.Queue[0])
	}
	conversationKey := session.Key{Agent: agent.Claude, ChatID: msg.ChatID, Thread: msg.ThreadID}
	if _, ok := service.Sessions.Get(conversationKey); ok {
		t.Fatalf("schedule proposal leaked into conversation session %s", conversationKey.ID())
	}
}

func TestInvalidScheduleArgumentsShowCompleteUsage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "cron missing add body", text: "/cron add", want: []string{"缺少自然语言任务描述", "/cron list all", "/cron add <自然语言任务>", "/cron run <id>", "/cron enable|disable <id>", "/cron del <id>"}},
		{name: "cron invalid list argument", text: "/cron list mine", want: []string{"参数不正确", "/cron list all", "/cron info <id>"}},
		{name: "cron unknown subcommand", text: "/cron wat", want: []string{"未知子命令", "/cron add <自然语言任务>", "/cron run <id>"}},
		{name: "timer missing id", text: "/timer del", want: []string{"任务 ID 参数不正确", "/timer list all", "/timer info <id>", "/timer del <id>", "不支持 `run`、`enable` 或 `disable`"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, renderer, _ := scheduleTestService(t)
			msg := Message{ID: "om_invalid", ChatID: "oc_chat", ThreadID: "omt_topic", Sender: "ou_creator", Text: tt.text, Time: time.Now()}
			if err := service.HandleMessage(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			events := renderer.Events()
			if len(events) != 1 || len(events[0].Segments) != 1 {
				t.Fatalf("events = %#v", events)
			}
			got := events[0].Segments[0].Text
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("usage for %q is missing %q:\n%s", tt.text, want, got)
				}
			}
		})
	}
}

// TestScheduleDeleteAcceptsUnifiedVerbs verifies /cron and /timer accept the
// same delete verbs as /ws (del / delete / remove / rm), so the delete word is
// uniform across all three commands.
func TestScheduleDeleteAcceptsUnifiedVerbs(t *testing.T) {
	for _, verb := range []string{"del", "delete", "remove", "rm"} {
		t.Run(verb, func(t *testing.T) {
			service, _, store := scheduleTestService(t)
			draft := bridgeFixtureDraft(time.Now())
			if err := store.CreateDraft(draft); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ConfirmDraft(draft.ID, draft.Creator, time.Now()); err != nil {
				t.Fatal(err)
			}
			msg := Message{
				ID: "om_del", ChatID: "oc_chat", ThreadID: "omt_topic", Sender: "ou_creator",
				Text: "/timer " + verb + " " + draft.ID, Time: time.Now(),
			}
			if err := service.HandleMessage(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if _, ok := store.Task(draft.ID); ok {
				t.Fatalf("task still present after /timer %s", verb)
			}
		})
	}
}

func TestScheduleDispatchUsesFrozenConfiguration(t *testing.T) {
	service, _, _ := scheduleTestService(t)
	workDir := t.TempDir()
	task := schedule.Task{
		ID: "task1234", Kind: schedule.KindCron, Prompt: "生成日报", Creator: "ou_creator",
		Target:    schedule.Target{ChatID: "oc_chat", ThreadID: "omt_topic", ReplyToMessageID: "om_origin", IsGroup: true},
		Execution: schedule.FrozenExecution{Agent: "codex", Model: "frozen-model", Effort: "high", AgentHome: "/tmp/codex-home", AgentBin: "/opt/codex", WorkDir: workDir, ReplyMode: "append-clean-card", AppendOverflowMode: "continue-card", ConversationMode: "topic"},
	}
	run := schedule.Run{ID: "cron:task1234:2026-07-21T09:00:00Z", TaskID: task.ID, ScheduledAt: time.Now(), State: schedule.RunPending}
	conversationKey := session.Key{Agent: agent.Codex, ChatID: task.Target.ChatID, Thread: task.Target.ThreadID}
	now := time.Now()
	if _, _, err := service.Sessions.EnqueueDurable(conversationKey, session.Input{ID: "ordinary", Text: "ordinary chat", WorkDir: workDir, Time: now, State: session.InputQueued}, workDir, session.BatchLimits{}); err != nil {
		t.Fatal(err)
	}
	_, ordinaryBatch, err := service.Sessions.FreezeReadyBatch(conversationKey, now, session.BatchLimits{})
	if err != nil || ordinaryBatch == nil {
		t.Fatalf("freeze ordinary batch: batch=%#v err=%v", ordinaryBatch, err)
	}
	if _, _, err := service.Sessions.MarkBatchRunning(conversationKey, ordinaryBatch.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sessions.FinishBatch(conversationKey, ordinaryBatch.ID, session.BatchCompletion{Status: session.InputCompleted, AgentSessionID: "E2E_AGENT_FIXTURE_invalid", At: now}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Enqueue(context.Background(), task, run)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate {
		t.Fatal("first enqueue reported duplicate")
	}
	key := session.Key{Agent: agent.Codex, ChatID: task.Target.ChatID, Thread: "schedule-run:" + run.ID}
	sess, ok := service.Sessions.Get(key)
	if !ok || len(sess.Queue) != 1 {
		t.Fatalf("session = %#v ok=%v", sess, ok)
	}
	conversation, ok := service.Sessions.Get(conversationKey)
	if !ok || conversation.AgentSessionID != "E2E_AGENT_FIXTURE_invalid" || len(conversation.Queue) != 0 {
		t.Fatalf("scheduled run mutated conversation session: %#v ok=%v", conversation, ok)
	}
	input := sess.Queue[0]
	if input.ID != run.ID || input.Text != task.Prompt || input.WorkDir != workDir || input.RequestedModel != "frozen-model" || input.RequestedEffort != "high" || input.AgentHome != "/tmp/codex-home" || input.AgentBin != "/opt/codex" {
		t.Fatalf("frozen input = %#v", input)
	}
	if input.ScheduleRunID != run.ID || input.ScheduleTaskID != task.ID || input.ConversationMode != "topic" || input.TopicID != task.Target.ThreadID || input.ReplyMode != "append-clean-card" || input.AppendOverflowMode != config.AppendOverflowModeContinueCard {
		t.Fatalf("schedule correlation = %#v", input)
	}
	duplicate, err := service.Enqueue(context.Background(), task, run)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate result = %#v err=%v", duplicate, err)
	}
}

func TestScheduleDispatchCreatesFreshSessionForEveryRun(t *testing.T) {
	for _, kind := range []schedule.Kind{schedule.KindCron, schedule.KindTimer} {
		t.Run(string(kind), func(t *testing.T) {
			service, _, _ := scheduleTestService(t)
			now := time.Now()
			task := schedule.Task{
				ID: "task1234", Kind: kind, Prompt: "生成日报", Creator: "ou_creator",
				Target:    schedule.Target{ChatID: "oc_chat", ReplyToMessageID: "om_origin"},
				Execution: schedule.FrozenExecution{Agent: "claude", WorkDir: t.TempDir()},
			}
			firstRun := schedule.Run{ID: string(kind) + ":task1234:2026-08-01T01:00:00Z", TaskID: task.ID, ScheduledAt: now, State: schedule.RunPending}
			if _, err := service.Enqueue(context.Background(), task, firstRun); err != nil {
				t.Fatal(err)
			}
			firstKey := session.Key{Agent: agent.Claude, ChatID: task.Target.ChatID, Thread: "schedule-run:" + firstRun.ID}
			_, firstBatch, err := service.Sessions.FreezeReadyBatch(firstKey, now, session.BatchLimits{})
			if err != nil || firstBatch == nil {
				t.Fatalf("freeze first run: batch=%#v err=%v", firstBatch, err)
			}
			if _, _, err := service.Sessions.MarkBatchRunning(firstKey, firstBatch.ID, nil, now); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Sessions.FinishBatch(firstKey, firstBatch.ID, session.BatchCompletion{Status: session.InputCompleted, AgentSessionID: "agent-session-from-first-run", At: now}); err != nil {
				t.Fatal(err)
			}

			secondRun := schedule.Run{ID: string(kind) + ":task1234:2026-08-02T01:00:00Z", TaskID: task.ID, ScheduledAt: now.Add(24 * time.Hour), State: schedule.RunPending}
			if _, err := service.Enqueue(context.Background(), task, secondRun); err != nil {
				t.Fatal(err)
			}
			secondKey := session.Key{Agent: agent.Claude, ChatID: task.Target.ChatID, Thread: "schedule-run:" + secondRun.ID}
			secondSession, ok := service.Sessions.Get(secondKey)
			if !ok || len(secondSession.Queue) != 1 {
				t.Fatalf("second run session = %#v ok=%v", secondSession, ok)
			}
			if firstKey.ID() == secondKey.ID() || secondSession.AgentSessionID != "" {
				t.Fatalf("second run reused first session: first=%q second=%q agent_session=%q", firstKey.ID(), secondKey.ID(), secondSession.AgentSessionID)
			}
			firstSession, ok := service.Sessions.Get(firstKey)
			if !ok || firstSession.AgentSessionID != "agent-session-from-first-run" {
				t.Fatalf("first run session was mutated: %#v ok=%v", firstSession, ok)
			}
		})
	}
}

type proposingRunner struct {
	mu       sync.Mutex
	request  AgentRunRequest
	proposal schedule.Proposal
	done     chan struct{}
}

type deadlineRunner struct {
	started chan struct{}
}

func TestCLIExecRunnerInjectsAbsoluteScheduleCLI(t *testing.T) {
	runtime, err := bridgeinstructions.NewRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	logPath := filepath.Join(t.TempDir(), "schedule-env.log")
	t.Setenv("FAKE_AGENT_LOG", logPath)
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
printf '%s\n%s\n%s\n' "$LAB_SCHEDULE_CLI" "$LAB_SCHEDULE_SOCKET" "$LAB_SCHEDULE_TOKEN" >"$FAKE_AGENT_LOG"
printf '%s\n' '{"type":"assistant","message":{"model":"fake","content":[{"type":"text","text":"ok"}]}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := CLIExecRunner{Instructions: runtime}
	if _, err := runner.Run(context.Background(), AgentRunRequest{
		Kind: agent.Claude, Bin: bin, Prompt: "propose", BridgeInstructionsVersion: bridgeinstructions.CurrentVersion,
		ScheduleSocket: "/tmp/schedule.sock", ScheduleToken: "single-use-token",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("schedule env line count = %d", len(lines))
	}
	if !filepath.IsAbs(lines[0]) {
		t.Fatal("schedule CLI is not absolute")
	}
	if lines[1] != "/tmp/schedule.sock" || lines[2] != "single-use-token" {
		t.Fatal("schedule socket or token was not injected")
	}
}

func (r *deadlineRunner) Run(ctx context.Context, _ AgentRunRequest) (AgentRunResult, error) {
	close(r.started)
	<-ctx.Done()
	return AgentRunResult{}, ctx.Err()
}

func (r *proposingRunner) Run(ctx context.Context, request AgentRunRequest) (AgentRunResult, error) {
	r.mu.Lock()
	r.request = request
	r.mu.Unlock()
	client := schedule.ProposeClient{SocketPath: request.ScheduleSocket, Timeout: time.Second}
	_, err := client.Propose(ctx, schedule.ProposeRequest{Token: request.ScheduleToken, Proposal: r.proposal})
	close(r.done)
	if err != nil {
		return AgentRunResult{}, err
	}
	return AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "proposal submitted"}}}, nil
}

func TestAgentProposalGetsScopedTokenAndReplacesTerminalCard(t *testing.T) {
	now := time.Now()
	workDir := t.TempDir()
	cfg := testConfig(t)
	cfg.DefaultWorkDir = workDir
	cfg.AppendOverflowMode = config.AppendOverflowModeContinueCard
	renderer := card.NewFakeRenderer()
	runner := &proposingRunner{done: make(chan struct{}), proposal: schedule.Proposal{
		Kind: schedule.KindCron, CronExpr: "0 9 * * 1-5", Timezone: "Asia/Shanghai", Description: "工作日总结", Prompt: "总结昨天的进展",
	}}
	service := NewService(cfg, renderer, runner, audit.NewRecorder())
	grants, err := actiongrant.OpenStore(filepath.Join(t.TempDir(), "action-grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	service.ActionGrants = grants
	store, err := schedule.NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := schedule.NewContextRegistry(time.Minute)
	socketPath := shortBridgeSocketPath(t)
	control := schedule.NewControlServer(socketPath, store, registry, schedule.ControlConfig{Now: func() time.Time { return now }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := control.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	service.Schedules = store
	service.ScheduleContexts = registry
	service.ScheduleSocket = socketPath

	msg := Message{ID: "om_schedule", ChatID: "oc_chat", ThreadID: "omt_topic", Sender: "ou_creator", Text: "/cron add 每个工作日九点总结项目进展", Time: now}
	if err := service.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := service.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.done:
	case <-time.After(2 * time.Second):
		t.Fatal("proposal runner did not finish")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := service.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	request := runner.request
	runner.mu.Unlock()
	if request.ScheduleSocket != socketPath || request.ScheduleToken == "" {
		t.Fatalf("schedule request context = %#v", request)
	}
	if got := len(store.Drafts()); got != 1 {
		t.Fatalf("draft count = %d", got)
	}
	armedDraft := store.Drafts()[0]
	if got := armedDraft.Target.ThreadID; got != msg.ThreadID {
		t.Fatalf("draft target thread = %q, want %q", got, msg.ThreadID)
	}
	if got := armedDraft.Execution.AppendOverflowMode; got != string(config.AppendOverflowModeContinueCard) {
		t.Fatalf("draft append overflow mode = %q", got)
	}
	if armedDraft.AutoConfirmAt.IsZero() {
		t.Fatal("auto-confirm deadline was not persisted")
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "schedule_confirmation" || len(last.Actions) != 2 || last.Actions[0].GrantID == "" || last.Actions[1].GrantID == "" || strings.Contains(last.Segments[0].Text, "proposal submitted") || !strings.Contains(last.Segments[0].Text, "60 秒后自动确认") {
		t.Fatalf("terminal event = %#v", last)
	}
	confirmGrant, cancelGrant := last.Actions[0].GrantID, last.Actions[1].GrantID
	if err := service.ProcessScheduleConfirmations(armedDraft.AutoConfirmAt.Add(-30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	countdown := renderer.Events()[len(renderer.Events())-1]
	if !strings.Contains(segmentText(countdown), "30 秒后自动确认") {
		t.Fatalf("countdown event = %#v", countdown)
	}
	if countdown.Actions[0].GrantID != confirmGrant || countdown.Actions[1].GrantID != cancelGrant {
		t.Fatalf("countdown grants changed: before=(%q,%q) after=(%q,%q)", confirmGrant, cancelGrant, countdown.Actions[0].GrantID, countdown.Actions[1].GrantID)
	}
	if err := service.ProcessScheduleConfirmations(armedDraft.AutoConfirmAt); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Task(armedDraft.ID); !ok {
		t.Fatal("countdown expiry did not create task")
	}
	autoConfirmed := renderer.Events()[len(renderer.Events())-1]
	if autoConfirmed.Type != "schedule_confirmed" || !strings.Contains(segmentText(autoConfirmed), "倒计时结束") || len(autoConfirmed.Actions) != 2 || !autoConfirmed.Actions[0].Disabled || !autoConfirmed.Actions[1].Disabled {
		t.Fatalf("auto-confirmed event = %#v", autoConfirmed)
	}
}

func TestScheduledBatchUpdatesRunLifecycle(t *testing.T) {
	workDir := t.TempDir()
	cfg := testConfig(t)
	cfg.DefaultWorkDir = workDir
	runner := newFakeRunner()
	service := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	store, err := schedule.NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	draft := bridgeFixtureDraft(now)
	draft.Kind = schedule.KindCron
	draft.CronExpr = "0 9 * * *"
	draft.ScheduledAt = time.Time{}
	draft.Next = []time.Time{now.Add(time.Hour)}
	draft.Execution.WorkDir = workDir
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	task, err := store.ConfirmDraft(draft.ID, draft.Creator, now)
	if err != nil {
		t.Fatal(err)
	}
	engine := schedule.NewEngine(store, service, service, schedule.EngineConfig{})
	service.Schedules = store
	service.Scheduler = engine
	if err := engine.RunNow(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled runner did not start")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	runs := store.Runs()
	if len(runs) != 1 || !runs[0].Manual || runs[0].State != schedule.RunDelivered || runs[0].StartedAt.IsZero() || runs[0].CompletedAt.IsZero() {
		t.Fatalf("run lifecycle = %#v", runs)
	}
}

func TestScheduledBatchHonorsExecutionTimeout(t *testing.T) {
	workDir := t.TempDir()
	cfg := testConfig(t)
	cfg.DefaultWorkDir = workDir
	runner := &deadlineRunner{started: make(chan struct{})}
	service := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	store, err := schedule.NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	draft := bridgeFixtureDraft(now)
	draft.Execution.WorkDir = workDir
	if err := store.CreateDraft(draft); err != nil {
		t.Fatal(err)
	}
	task, err := store.ConfirmDraft(draft.ID, draft.Creator, now)
	if err != nil {
		t.Fatal(err)
	}
	engine := schedule.NewEngine(store, service, service, schedule.EngineConfig{ExecutionTimeout: 30 * time.Millisecond})
	service.Schedules = store
	service.Scheduler = engine
	if err := engine.RunNow(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("scheduled runner did not start")
	}
	deadline := time.Now().Add(time.Second)
	for {
		runs := store.Runs()
		if len(runs) == 1 && runs[0].State == schedule.RunFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not time out: %#v", runs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func shortBridgeSocketPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("/tmp", "lab-bridge-"+strings.ReplaceAll(t.Name(), "/", "-"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "schedule.sock")
}

func scheduleTestService(t *testing.T) (*Service, *card.FakeRenderer, *schedule.Store) {
	t.Helper()
	renderer := card.NewFakeRenderer()
	service, store := scheduleTestServiceWithRenderer(t, renderer)
	return service, renderer, store
}

func scheduleTestServiceWithRenderer(t *testing.T, renderer card.Renderer) (*Service, *schedule.Store) {
	t.Helper()
	service := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	store, err := schedule.NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	service.Schedules = store
	return service, store
}

type selectiveScheduleRenderer struct {
	mu        sync.Mutex
	failAll   bool
	failTypes map[string]bool
	events    []card.Event
}

func (r *selectiveScheduleRenderer) Render(event card.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failAll || r.failTypes[event.Type] {
		return errors.New("injected render failure")
	}
	r.events = append(r.events, event)
	return nil
}

func (r *selectiveScheduleRenderer) SetFailAll(fail bool) {
	r.mu.Lock()
	r.failAll = fail
	r.mu.Unlock()
}

func (r *selectiveScheduleRenderer) Events() []card.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]card.Event(nil), r.events...)
}

type blockingScheduleRenderer struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func newBlockingScheduleRenderer() *blockingScheduleRenderer {
	return &blockingScheduleRenderer{started: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingScheduleRenderer) Render(card.Event) error {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	return nil
}

func bridgeFixtureDraft(now time.Time) schedule.Draft {
	return schedule.Draft{
		ID: "abcd1234", Kind: schedule.KindTimer, ScheduledAt: now.Add(time.Hour), Timezone: "Asia/Shanghai",
		Description: "提醒开会", Prompt: "提醒我参加项目会议", Creator: "ou_creator",
		Target:    schedule.Target{ChatID: "oc_chat", ThreadID: "omt_topic", ReplyToMessageID: "om_origin"},
		Execution: schedule.FrozenExecution{Agent: string(agent.Claude), Model: "sonnet", Effort: "low", WorkDir: tWorkDir(), ReplyMode: "append", ConversationMode: "topic"},
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute), Next: []time.Time{now.Add(time.Hour)},
	}
}

func tWorkDir() string { return "/tmp/lark-agent-schedule-test" }
