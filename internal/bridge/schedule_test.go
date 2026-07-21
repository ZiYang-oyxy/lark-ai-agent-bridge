package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/bridgeinstructions"
	"lark-agent-bridge/internal/card"
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

func TestScheduleDispatchUsesFrozenConfiguration(t *testing.T) {
	service, _, _ := scheduleTestService(t)
	workDir := t.TempDir()
	task := schedule.Task{
		ID: "task1234", Kind: schedule.KindCron, Prompt: "生成日报", Creator: "ou_creator",
		Target:    schedule.Target{ChatID: "oc_chat", ThreadID: "omt_topic", ReplyToMessageID: "om_origin", IsGroup: true},
		Execution: schedule.FrozenExecution{Agent: "codex", Model: "frozen-model", Effort: "high", AgentHome: "/tmp/codex-home", AgentBin: "/opt/codex", WorkDir: workDir, ReplyMode: "append-clean-card", ConversationMode: "topic"},
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
	key := session.Key{Agent: agent.Codex, ChatID: task.Target.ChatID, Thread: "schedule:" + task.ID}
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
	if input.ScheduleRunID != run.ID || input.ScheduleTaskID != task.ID || input.ConversationMode != "topic" || input.ReplyMode != "append-clean-card" {
		t.Fatalf("schedule correlation = %#v", input)
	}
	duplicate, err := service.Enqueue(context.Background(), task, run)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate result = %#v err=%v", duplicate, err)
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
	renderer := card.NewFakeRenderer()
	runner := &proposingRunner{done: make(chan struct{}), proposal: schedule.Proposal{
		Kind: schedule.KindCron, CronExpr: "0 9 * * 1-5", Timezone: "Asia/Shanghai", Description: "工作日总结", Prompt: "总结昨天的进展",
	}}
	service := NewService(cfg, renderer, runner, audit.NewRecorder())
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
	if got := store.Drafts()[0].Target.ThreadID; got != msg.ThreadID {
		t.Fatalf("draft target thread = %q, want %q", got, msg.ThreadID)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "schedule_confirmation" || len(last.Actions) != 2 || strings.Contains(last.Segments[0].Text, "proposal submitted") {
		t.Fatalf("terminal event = %#v", last)
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
	service := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	store, err := schedule.NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	service.Schedules = store
	return service, renderer, store
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
