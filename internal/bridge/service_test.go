package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

type fakeRunner struct {
	mu      sync.Mutex
	calls   []AgentRunRequest
	results []AgentRunResult
	errs    []error
	updates []AgentStreamUpdate
	block   chan struct{}
	started chan struct{}
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{started: make(chan struct{}, 10)}
}

func (r *fakeRunner) Run(ctx context.Context, req AgentRunRequest) (AgentRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req)
	idx := len(r.calls) - 1
	r.mu.Unlock()
	r.started <- struct{}{}
	for _, update := range r.updates {
		if req.OnEvent != nil {
			req.OnEvent(update)
		}
	}
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return AgentRunResult{}, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx < len(r.errs) && r.errs[idx] != nil {
		return AgentRunResult{}, r.errs[idx]
	}
	if idx < len(r.results) {
		return r.results[idx], nil
	}
	return AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "ok"}}}, nil
}

type failOnceRenderer struct {
	mu     sync.Mutex
	failed bool
	next   *card.FakeRenderer
}

type blockingStreamRenderer struct {
	next    *card.FakeRenderer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingStreamRenderer) Render(e card.Event) error {
	if e.Type == "stream" {
		r.once.Do(func() { close(r.entered) })
		<-r.release
	}
	return r.next.Render(e)
}

func (r *failOnceRenderer) Render(e card.Event) error {
	if e.Type != "stream" {
		return r.next.Render(e)
	}
	r.mu.Lock()
	if !r.failed {
		r.failed = true
		r.mu.Unlock()
		return errors.New("reply message revoked")
	}
	r.mu.Unlock()
	return r.next.Render(e)
}

func (r *fakeRunner) Calls() []AgentRunRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]AgentRunRequest, len(r.calls))
	copy(out, r.calls)
	return out
}

func TestServiceNewRunsClaudeOneShotAndRendersResult(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.results = []AgentRunResult{{Model: "claude-sonnet", Tokens: 42, ClaudeSessionID: "sess-1", Segments: []card.Segment{
		{Kind: card.SegmentText, Text: "answer"},
		{Kind: card.SegmentThought, Text: "thinking"},
		{Kind: card.SegmentTool, Text: "Bash(ls)"},
	}}}
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg := Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(msg.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForEvents(t, renderer, 2)
	calls := runner.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	if calls[0].Kind != agent.Claude || calls[0].Prompt != "hello" || calls[0].ClaudeSessionID != "" {
		t.Fatalf("runner call = %#v", calls[0])
	}
	events := renderer.Events()
	initial := events[len(events)-2]
	if initial.ReplyToMessageID != "msg-1" || !initial.StopButton.Visible {
		t.Fatalf("initial event = %#v", initial)
	}
	if initial.HeaderTemplate != "blue" || !containsAll(initial.HeaderTitle, "正在推理", "⏱") {
		t.Fatalf("initial header = %q/%q", initial.HeaderTemplate, initial.HeaderTitle)
	}
	last := events[len(events)-1]
	if last.Type != "result" || last.Meta.Model != "claude-sonnet" || last.Meta.RunTokens != 42 || last.Meta.TotalTokens != 42 || last.HeaderTemplate != "green" {
		t.Fatalf("result event = %#v", last)
	}
	if !last.StopButton.Visible || !last.StopButton.Disabled {
		t.Fatalf("completed event should show disabled stop button: %#v", last.StopButton)
	}
	status := svc.statusText(agent.Claude, Message{ChatID: "chat"})
	if !containsAll(status, "claude_session=sess-1", "state=idle") {
		t.Fatalf("status = %q, want stored claude session", status)
	}
}

func TestServiceQueuesRunUntilExplicitDrain(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "queued-1", ChatID: "chat", Sender: "u", Text: "/new hello", Time: now}); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls before drain = %d, want 0", got)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
}

func TestInitialCardRenderFailureDoesNotStartRunnerOrLeakActiveRun(t *testing.T) {
	cfg := testConfig(t)
	fake := card.NewFakeRenderer()
	renderer := &failOnceRenderer{next: fake}
	runner := newFakeRunner()
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, runner, recorder)
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls after failed card render = %#v, want none", runner.Calls())
	}
	status := svc.statusText(agent.Claude, Message{ChatID: "chat"})
	if !containsAll(status, "state=idle", "queue=0") {
		t.Fatalf("status after failed card render = %q, want idle with empty queue", status)
	}
	foundAudit := false
	for _, event := range recorder.Events() {
		if event.Action == "card_render_failed" && strings.Contains(event.Detail, "reply message revoked") {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %#v, want card_render_failed", recorder.Events())
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "/new second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].Prompt; got != "second" {
		t.Fatalf("runner prompt = %q, want second", got)
	}
	for _, event := range fake.Events() {
		if event.Type == "reaction" && event.Message == "queued" {
			t.Fatalf("second message was queued after failed card render: %#v", event)
		}
	}
}

func TestServiceStreamsRunnerUpdatesIntoSameCard(t *testing.T) {
	cfg := testConfig(t)
	cfg.CardUpdateEvery = time.Nanosecond
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.updates = []AgentStreamUpdate{
		{Segments: []card.Segment{{Kind: card.SegmentThought, Text: "plan"}}, Activity: streamActivityReasoning},
		{Segments: []card.Segment{{Kind: card.SegmentTool, Text: "Bash(ls)"}}, Activity: streamActivityTool},
		{Segments: []card.Segment{{Kind: card.SegmentText, Text: "answer"}}, Activity: streamActivityAnswering, Model: "claude-stream", Tokens: 3},
	}
	runner.results = []AgentRunResult{{Model: "claude-stream", Tokens: 3, Segments: []card.Segment{
		{Kind: card.SegmentText, Text: "answer"},
	}}}
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new hello", Time: time.Now()}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForEvents(t, renderer, 5)
	events := renderer.Events()
	if events[2].Type != "stream" || !events[2].ThoughtExpanded {
		t.Fatalf("thought stream event = %#v", events[2])
	}
	if events[3].Type != "stream" || !events[3].ToolsExpanded {
		t.Fatalf("tool stream event = %#v", events[3])
	}
	last := events[len(events)-1]
	if last.Type != "result" || last.HeaderTemplate != "green" {
		t.Fatalf("last event = %#v", last)
	}
	if !last.StopButton.Visible || !last.StopButton.Disabled {
		t.Fatalf("completed event should show disabled stop button: %#v", last.StopButton)
	}
	if last.Meta.RunTokens != 3 || last.Meta.TotalTokens != 3 {
		t.Fatalf("last meta = %#v, want run and total tokens", last.Meta)
	}
	if !containsAll(last.Segments[0].Text, "answer") || !containsAll(last.Segments[1].Text, "plan") || !containsAll(last.Segments[2].Text, "Bash(ls)") {
		t.Fatalf("final segments = %#v", last.Segments)
	}
}

func TestServiceNewWithoutPromptCreatesReadySession(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new", Time: now}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForEvents(t, renderer, 3)
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls())
	}
	events := renderer.Events()
	if len(events) < 3 || events[len(events)-1].Type != "result" {
		t.Fatalf("events = %#v, want queued and ready terminal cards", events)
	}
}

func TestPlainTextAfterReadySessionKeepsWorkDir(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	workDir := t.TempDir()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + workDir, Time: time.Now()}); err != nil {
		t.Fatalf("ready message error: %v", err)
	}
	waitForEvents(t, renderer, 1)
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none for ready session", runner.Calls())
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "show pwd", Time: time.Now()}); err != nil {
		t.Fatalf("plain message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	calls := runner.Calls()
	if calls[0].WorkDir != workDir {
		t.Fatalf("runner workdir = %q, want %q", calls[0].WorkDir, workDir)
	}
	if calls[0].ClaudeSessionID != "" {
		t.Fatalf("plain top-level message should start a fresh Claude session, got %q", calls[0].ClaudeSessionID)
	}
	waitForEvents(t, renderer, 3)
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Meta.WorkDir != workDir {
		t.Fatalf("result workdir = %q, want %q", last.Meta.WorkDir, workDir)
	}
}

func TestTopicPlainTextContinuesStoredClaudeSession(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.results = []AgentRunResult{
		{ClaudeSessionID: "sess-topic", Tokens: 2, Segments: []card.Segment{{Kind: card.SegmentText, Text: "first"}}},
		{ClaudeSessionID: "sess-topic", Tokens: 3, Segments: []card.Segment{{Kind: card.SegmentText, Text: "second"}}},
	}
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg1 := Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(msg1.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForEvents(t, renderer, 2)
	msg2 := Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "second", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(msg2.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[1].ClaudeSessionID != "sess-topic" {
		t.Fatalf("second call session id = %q, want sess-topic", calls[1].ClaudeSessionID)
	}
	waitForEvents(t, renderer, 5)
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Meta.RunTokens != 3 || last.Meta.TotalTokens != 5 {
		t.Fatalf("second run meta = %#v, want run=3 total=5", last.Meta)
	}
}

func TestNewInTopicResetsStoredClaudeSession(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.results = []AgentRunResult{
		{ClaudeSessionID: "sess-old", Segments: []card.Segment{{Kind: card.SegmentText, Text: "old"}}},
		{ClaudeSessionID: "sess-new", Segments: []card.Segment{{Kind: card.SegmentText, Text: "new"}}},
	}
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg1 := Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(msg1.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForEvents(t, renderer, 2)
	msg2 := Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new second", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(msg2.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[1].ClaudeSessionID != "" {
		t.Fatalf("new call session id = %q, want reset", calls[1].ClaudeSessionID)
	}
}

func TestServiceQueuesSecondInputUntilFirstCompletes(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	events := renderer.Events()
	if events[len(events)-1].Type != "reaction" || !strings.HasPrefix(events[len(events)-1].Message, "queued") {
		t.Fatalf("queued event = %#v", events[len(events)-1])
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic-a"})
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[1].Prompt != "second" {
		t.Fatalf("second runner prompt = %q", calls[1].Prompt)
	}
}

func TestDifferentTopicsRunInParallel(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-b", Sender: "u1", Text: "/new second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	close(runner.block)
}

func TestServiceMissingWorkdirAsksThenRunsAfterCreate(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg := Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + missing + " hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 1 || events[0].Type != "workdir_confirm" {
		t.Fatalf("events = %#v, want workdir confirm", events)
	}
	if events[0].SessionID != "claude:chat:message:msg-1" {
		t.Fatalf("confirm session id = %q", events[0].SessionID)
	}
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "create_workdir", Actor: "u1"})
	if err != nil {
		t.Fatalf("create action error: %v", err)
	}
	if result.Event == nil || result.Event.Type != "workdir_created" || result.Event.SessionID != "claude:chat:message:msg-1" {
		t.Fatalf("action result = %#v, want workdir_created on confirm card", result.Event)
	}
	if len(result.Event.Actions) != 2 || !result.Event.Actions[0].Disabled || !result.Event.Actions[1].Disabled {
		t.Fatalf("terminal actions = %#v, want disabled", result.Event.Actions)
	}
	if _, err := os.Stat(missing); err != nil {
		t.Fatalf("missing dir was not created: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].WorkDir; got != missing {
		t.Fatalf("runner workdir = %q, want %q", got, missing)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
	events = renderer.Events()
	if events[1].Type != "workdir_created" || events[1].SessionID != "claude:chat:message:msg-1" {
		t.Fatalf("terminal confirm event = %#v", events[1])
	}
	runEvent := events[3]
	if runEvent.Type != "stream" || runEvent.SessionID == events[1].SessionID || runEvent.ReplyToMessageID != "msg-1" {
		t.Fatalf("run event = %#v, want separate card replying to original message", runEvent)
	}
}

func TestPlainTextAfterCreatedWorkDirKeepsWorkDir(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + missing, Time: time.Now()}); err != nil {
		t.Fatalf("handle missing workdir error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 1 || events[0].Type != "workdir_confirm" {
		t.Fatalf("events = %#v, want workdir confirm", events)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: events[0].SessionID, ActionID: "create_workdir", Actor: "u1"}); err != nil {
		t.Fatalf("create action error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none for empty ready session", runner.Calls())
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "show pwd", Time: time.Now()}); err != nil {
		t.Fatalf("plain message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].WorkDir; got != missing {
		t.Fatalf("runner workdir = %q, want %q", got, missing)
	}
	waitForEvents(t, renderer, 5)
	events = renderer.Events()
	last := events[len(events)-1]
	if last.Meta.WorkDir != missing {
		t.Fatalf("result workdir = %q, want %q", last.Meta.WorkDir, missing)
	}
}

func TestWorkdirCancelDoesNotRun(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg := Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + missing + " hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "cancel_workdir", Actor: "u1"})
	if err != nil {
		t.Fatalf("cancel action error: %v", err)
	}
	if result.Event == nil || result.Event.Type != "workdir_cancelled" || result.Event.HeaderTemplate != "" {
		t.Fatalf("cancel result = %#v, want workdir_cancelled default grey card", result.Event)
	}
	if len(result.Event.Actions) != 2 || !result.Event.Actions[0].Disabled || !result.Event.Actions[1].Disabled {
		t.Fatalf("cancel actions = %#v, want disabled", result.Event.Actions)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir stat = %v, want not exist", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls())
	}
	events := renderer.Events()
	if events[len(events)-1].Type != "workdir_cancelled" {
		t.Fatalf("last event = %#v, want workdir_cancelled", events[len(events)-1])
	}
}

func TestConcurrentMissingWorkdirConfirmationsAreIsolatedByMessage(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())

	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + first + " first", Time: now}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + second + " second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}

	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events = %#v, want two confirmations", events)
	}
	if events[0].SessionID == events[1].SessionID {
		t.Fatalf("confirmations share session id %q", events[0].SessionID)
	}
	if !containsAll(events[0].Segments[0].Text, first) || !containsAll(events[1].Segments[0].Text, second) {
		t.Fatalf("confirmation texts = %#v", events)
	}

	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: events[1].SessionID, ActionID: "create_workdir", Actor: "u1"}); err != nil {
		t.Fatalf("second create action error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].WorkDir; got != second {
		t.Fatalf("first runner workdir = %q, want %q", got, second)
	}

	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: events[0].SessionID, ActionID: "create_workdir", Actor: "u1"}); err != nil {
		t.Fatalf("first create action error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := runner.Calls()[1].WorkDir; got != first {
		t.Fatalf("second runner workdir = %q, want %q", got, first)
	}
}

func TestServiceStopCancelsActiveOneShotRun(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new long", Time: time.Now()}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "stop", Actor: "u1"})
	if err != nil {
		t.Fatalf("stop action error: %v", err)
	}
	if result.Event == nil || result.Event.Type != "stopped" || !result.Event.StopButton.Disabled {
		t.Fatalf("stop action result = %#v, want disabled stopped", result.Event)
	}
	payload := result.BuildCard(cfg.CardMaxChars)
	elements := payload["body"].(map[string]any)["elements"].([]any)
	var button map[string]any
	for _, raw := range elements {
		element := raw.(map[string]any)
		if element["element_id"] == "btn_stop" {
			button = element
			break
		}
	}
	if button == nil {
		t.Fatalf("sync stop card elements = %#v, want stop button", elements)
	}
	if button["disabled"] != true {
		t.Fatalf("sync stop button = %#v, want disabled", button)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "stopped" || !last.StopButton.Disabled || last.HeaderTemplate != "grey" {
		t.Fatalf("stop event = %#v", last)
	}
}

func TestMessageRecallCancelsActiveRun(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, runner, recorder)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new long", Time: now}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "msg-1", ChatID: "chat", RecallType: "user"}); err != nil {
		t.Fatalf("recall message error: %v", err)
	}
	waitForEvents(t, renderer, 3)
	last := renderer.Events()[len(renderer.Events())-1]
	if last.Type != "stopped" || !last.StopButton.Disabled || last.HeaderTemplate != "grey" {
		t.Fatalf("recall stopped event = %#v", last)
	}
	status := svc.statusText(agent.Claude, Message{ChatID: "chat"})
	if !containsAll(status, "state=idle", "queue=0") {
		t.Fatalf("status after recall = %q, want idle with empty queue", status)
	}
	foundAudit := false
	for _, event := range recorder.Events() {
		if event.Action == "message_recalled_active_cancelled" && event.Detail == "msg-1" {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %#v, want message_recalled_active_cancelled", recorder.Events())
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "/new second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := runner.Calls()[1].Prompt; got != "second" {
		t.Fatalf("second prompt = %q, want second", got)
	}
}

func TestMessageRecallCancelsPendingWorkdirConfirmation(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, runner, recorder)
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + missing + " hello", Time: time.Now()}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "msg-1", ChatID: "chat", RecallType: "user"}); err != nil {
		t.Fatalf("recall message error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events = %#v, want confirm and cancelled", events)
	}
	last := events[len(events)-1]
	if last.Type != "workdir_cancelled" || last.SessionID != "claude:chat:message:msg-1" {
		t.Fatalf("last event = %#v, want workdir_cancelled", last)
	}
	if len(last.Actions) != 2 || !last.Actions[0].Disabled || !last.Actions[1].Disabled {
		t.Fatalf("cancelled actions = %#v, want disabled", last.Actions)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir stat = %v, want not exist", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls())
	}
	foundAudit := false
	for _, event := range recorder.Events() {
		if event.Action == "message_recalled_pending_cancelled" && event.Detail == "msg-1" {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %#v, want message_recalled_pending_cancelled", recorder.Events())
	}
}

func TestMessageRecallRemovesQueuedInput(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, runner, recorder)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: now}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "msg-2", ChatID: "chat", RecallType: "user"}); err != nil {
		t.Fatalf("recall message error: %v", err)
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic-a"})
	if len(runner.Calls()) != 1 {
		t.Fatalf("runner calls = %#v, want only first call", runner.Calls())
	}
	status := svc.statusText(agent.Claude, Message{ChatID: "chat", ThreadID: "topic-a"})
	if !containsAll(status, "state=idle", "queue=0") {
		t.Fatalf("status after queued recall = %q, want idle with empty queue", status)
	}
	foundAudit := false
	for _, event := range recorder.Events() {
		if event.Action == "message_recalled_queued_cancelled" && event.Detail == "msg-2" {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %#v, want message_recalled_queued_cancelled", recorder.Events())
	}
}

func TestQueuedRunPreservesInputWorkDir(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())

	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + first + " first", Time: now}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + second + " second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[0].WorkDir != first {
		t.Fatalf("first workdir = %q, want %q", calls[0].WorkDir, first)
	}
	if calls[1].WorkDir != second {
		t.Fatalf("queued workdir = %q, want %q", calls[1].WorkDir, second)
	}
}

func TestServiceSkipsDuplicateRunAndOldDelivery(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	msg := Message{ID: "dup", ChatID: "chat", Sender: "u", Text: "/new hello", Time: now}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "old", ChatID: "chat", Sender: "u", Text: "/new ignored", Time: svc.startedAt.Add(-3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("runner calls = %d, want one", got)
	}
}

func TestServiceRejectsTwentyFirstPendingInput(t *testing.T) {
	cfg := testConfig(t)
	cfg.QueueMaxPending = 20
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	now := time.Now()
	for i := 0; i < 20; i++ {
		if err := svc.HandleMessage(context.Background(), Message{ID: fmt.Sprintf("m-%d", i), ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "input", Time: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "m-20", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "overflow", Time: now}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "message" || !containsAll(last.Segments[0].Text, "队列已满") {
		t.Fatalf("overflow event = %#v", last)
	}
}

func TestServiceShutdownRejectsNewRunsAndCancelsActiveBatch(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "run", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	shutdownCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Shutdown(shutdownCtx) }()
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before cancellation: %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v, want canceled", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "new", ChatID: "chat", Sender: "u", Text: "/new rejected", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("runner calls = %d, want one", got)
	}
}

func TestServiceBackgroundReadyTickDrainsWithoutSleep(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "tick", ChatID: "chat", Sender: "u", Text: "/new tick", Time: now}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan time.Time, 1)
	svc.startBackgroundLoopsWithTicks(ctx, nil, ready)
	ready <- now.Add(time.Second)
	waitForCalls(t, runner, 1)
}

func TestServiceRecallDuringStartingCancelsBeforeRunnerSpawn(t *testing.T) {
	cfg := testConfig(t)
	fake := card.NewFakeRenderer()
	renderer := &blockingStreamRenderer{next: fake, entered: make(chan struct{}), release: make(chan struct{})}
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "starting", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- svc.DrainReady(now.Add(time.Second)) }()
	<-renderer.entered
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "starting", ChatID: "chat"}); err != nil {
		t.Fatal(err)
	}
	close(renderer.release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
	events := fake.Events()
	if last := events[len(events)-1]; last.Type != "stopped" || last.Streaming {
		t.Fatalf("early-cancel card = %#v, want stopped terminal", last)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
}

func TestServiceRecallBeforeInitialCardRendersStopped(t *testing.T) {
	cfg := testConfig(t)
	fake := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, fake, runner, audit.NewRecorder())
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.afterStoreActiveRunHook = func() { close(entered); <-release }
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "pre-card", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- svc.DrainReady(now.Add(time.Second)) }()
	<-entered
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "pre-card", ChatID: "chat"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
	events := fake.Events()
	if len(events) < 2 {
		t.Fatalf("events = %#v", events)
	}
	if last := events[len(events)-1]; last.Type != "stopped" || last.Streaming {
		t.Fatalf("early card = %#v, want stopped", last)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
}

func TestServiceShutdownWaitsForFrozenDispatchAndPreventsSpawn(t *testing.T) {
	cfg := testConfig(t)
	fake := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, fake, runner, audit.NewRecorder())
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.afterStoreActiveRunHook = func() { close(entered); <-release }
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "dispatch", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- svc.DrainReady(now.Add(time.Second)) }()
	<-entered
	shutdownCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- svc.Shutdown(shutdownCtx) }()
	waitForNotAccepting(t, svc)
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned while dispatch blocked: %v", err)
	default:
	}
	close(release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
}

func TestServiceShutdownDeadlineCancelsFrozenDispatchBeforeSpawn(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.afterStoreActiveRunHook = func() { close(entered); <-release }
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "deadline", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- svc.DrainReady(now.Add(time.Second)) }()
	<-entered
	shutdownCtx, cancel := context.WithCancel(context.Background())
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- svc.Shutdown(shutdownCtx) }()
	waitForNotAccepting(t, svc)
	cancel()
	close(release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v", err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
}

func TestServiceRestoreDoesNotRunClearedQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := session.Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Now()
	seed := session.NewManagerWithStore(path)
	if _, _, err := seed.AcceptAndEnqueue(key, session.Input{ID: "pending", ReplyToMessageID: "pending", WorkDir: t.TempDir(), Time: now, DebounceUntil: now, State: session.InputQueued}, now, time.Hour, 10, session.BatchLimits{MaxPending: 20}); err != nil {
		t.Fatal(err)
	}
	restored := session.NewManagerWithStore(path)
	notices, err := restored.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatalf("notices = %#v", notices)
	}
	runner := newFakeRunner()
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), runner, audit.NewRecorder(), restored, notices)
	if err := svc.DrainReady(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
}

func TestServiceCompletionPersistFailureStillRendersResultAndAudits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	manager := session.NewManagerWithStore(path)
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	runner.results = []AgentRunResult{{Segments: []card.Segment{{Kind: card.SegmentText, Text: "captured result"}}}}
	recorder := audit.NewRecorder()
	svc := NewServiceWithSessions(cfg, renderer, runner, recorder, manager, nil)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "persist", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(runner.block)
	waitForEvents(t, renderer, 3)
	last := renderer.Events()[len(renderer.Events())-1]
	if last.Type != "result" || !containsAll(last.Segments[0].Text, "captured result") {
		t.Fatalf("terminal card = %#v", last)
	}
	for _, event := range recorder.Events() {
		if event.Action == "completion_persist_failed" {
			return
		}
	}
	t.Fatalf("audit events = %#v, want completion_persist_failed", recorder.Events())
}

func TestServiceRetriesPersistedCompletionBeforeStartingLaterQueue(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions.json")
	manager := session.NewManagerWithStore(path)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	recorder := audit.NewRecorder()
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), runner, recorder, manager, nil)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "later", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "later", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(runner.block)
	waitForEvents(t, svc.Cards.(*card.LimitRenderer).Next.(*card.FakeRenderer), 4)
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("runner calls before repair = %d, want 1", got)
	}
	if got := len(svc.pendingCompletions); got != 1 {
		t.Fatalf("pending completions = %d, want 1", got)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := len(svc.pendingCompletions); got != 0 {
		t.Fatalf("pending completions after retry = %d", got)
	}
}

func TestServiceBacksOffPendingCompletionRetries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions.json")
	manager := session.NewManagerWithStore(path)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), runner, audit.NewRecorder(), manager, nil)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(runner.block)
	waitForEvents(t, svc.Cards.(*card.LimitRenderer).Next.(*card.FakeRenderer), 3)
	svc.mu.Lock()
	var pending pendingCompletion
	for _, entry := range svc.pendingCompletions {
		pending = entry
	}
	svc.mu.Unlock()
	if pending.Attempts != 0 {
		t.Fatalf("initial attempts = %d", pending.Attempts)
	}
	attempts := 0
	svc.beforePendingCompletionRetryHook = func() { attempts++ }
	if err := svc.DrainReady(pending.NextRetryAt.Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("retry before due attempts = %d", attempts)
	}
	if err := svc.DrainReady(pending.NextRetryAt); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("first due retry attempts = %d", attempts)
	}
	svc.mu.Lock()
	pending = svc.pendingCompletions[completionKey(pending.Key, pending.BatchID)]
	svc.mu.Unlock()
	if pending.Attempts != 1 {
		t.Fatalf("attempts after failure = %d", pending.Attempts)
	}
	if err := svc.DrainReady(pending.NextRetryAt.Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("retry before second due attempts = %d", attempts)
	}
	if err := svc.DrainReady(pending.NextRetryAt); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("second due retry attempts = %d", attempts)
	}
}

func TestServiceMergesBusyTopicInputsIntoNextBatch(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	first := Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}
	if err := svc.HandleMessage(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	for _, msg := range []Message{{ID: "two", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "two", Time: now}, {ID: "three", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "three", Time: now}} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"})
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if prompt := runner.Calls()[1].Prompt; !containsAll(prompt, "two", "three") {
		t.Fatalf("merged prompt = %q", prompt)
	}
}

func TestServiceDeduplicatesCommandAndAcceptsOldBoundary(t *testing.T) {
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	msg := Message{ID: "help", ChatID: "chat", Sender: "u", Text: "/help", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "boundary", ChatID: "chat", Sender: "u", Text: "/help", Time: svc.startedAt.Add(-2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	// Two accepted help cards: one receipt duplicate is silent and the -2s boundary is not old.
	if got := len(svc.Cards.(*card.LimitRenderer).Next.(*card.FakeRenderer).Events()); got != 2 {
		t.Fatalf("help cards = %d, want 2", got)
	}
}

func TestServiceRunningRecallCancelsMergedBatch(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	for _, msg := range []Message{{ID: "one", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "one", Time: now}, {ID: "two", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "two", Time: now}} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "one", ChatID: "chat"}); err != nil {
		t.Fatal(err)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"})
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("runner calls = %d, want one cancelled batch", got)
	}
}

func TestServiceStopKeepsLaterQueue(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "later", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "later", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat:thread:topic:message:first", ActionID: "stop", Actor: "u"}); err != nil {
		t.Fatal(err)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"})
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := runner.Calls()[1].Prompt; got != "later" {
		t.Fatalf("later prompt = %q", got)
	}
}

func TestServiceNewPromptIsBatchBoundaryAndResetsNextContext(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.results = []AgentRunResult{{ClaudeSessionID: "old"}, {ClaudeSessionID: "new"}, {ClaudeSessionID: "new"}}
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	inputs := []Message{{ID: "before", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "before", Time: now}, {ID: "new", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "/new reset", Time: now}, {ID: "after", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "after", Time: now}}
	for _, msg := range inputs {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	key := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForSessionNoActiveBatch(t, svc, key)
	if runner.Calls()[0].Prompt != "before" {
		t.Fatalf("first prompt = %q", runner.Calls()[0].Prompt)
	}
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	waitForSessionNoActiveBatch(t, svc, key)
	if call := runner.Calls()[1]; call.Prompt != "reset" || call.ClaudeSessionID != "" {
		t.Fatalf("reset call = %#v", call)
	}
	if err := svc.DrainReady(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 3)
	if call := runner.Calls()[2]; call.Prompt != "after" || call.ClaudeSessionID != "new" {
		t.Fatalf("after call = %#v", call)
	}
}

func TestServiceRunnerErrorSchedulesLaterQueue(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	runner.errs = []error{errors.New("runner failed")}
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "next", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "next", Time: now}); err != nil {
		t.Fatal(err)
	}
	close(runner.block)
	key := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"}
	waitForSessionNoActiveBatch(t, svc, key)
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := runner.Calls()[1].Prompt; got != "next" {
		t.Fatalf("next prompt = %q", got)
	}
}

func TestParseClaudeStreamOutputDoesNotDuplicateFinalResult(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"type":"assistant","message":{"model":"claude-opus","content":[{"type":"text","text":"E2E_ONESHOT"}],"usage":{"output_tokens":3},"session_id":"sess-1"}}`,
		`{"type":"result","result":"E2E_ONESHOT","usage":{"input_tokens":2}}`,
	}, "\n"))
	result := ParseClaudeStreamOutput(data)
	if result.ClaudeSessionID != "sess-1" || result.Model != "claude-opus" || result.Tokens != 5 {
		t.Fatalf("metadata = %#v", result)
	}
	if len(result.Segments) != 1 || result.Segments[0].Text != "E2E_ONESHOT" {
		t.Fatalf("segments = %#v, want single E2E_ONESHOT", result.Segments)
	}
}

func TestCLIExecRunnerUsesRequestedWorkDirAndPWD(t *testing.T) {
	fakeBin := t.TempDir()
	fakeClaude := filepath.Join(fakeBin, "claude")
	script := `#!/bin/sh
printf '{"type":"assistant","message":{"model":"fake-claude","content":[{"type":"text","text":"pwd=%s envpwd=%s"}],"usage":{"output_tokens":1},"session_id":"sess-1"}}\n' "$(pwd)" "$PWD"
`
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	workDir := t.TempDir()
	result, err := CLIExecRunner{}.Run(context.Background(), AgentRunRequest{
		Kind:    agent.Claude,
		Prompt:  "check cwd",
		WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("runner error: %v", err)
	}
	if len(result.Segments) != 1 {
		t.Fatalf("segments = %#v, want one answer", result.Segments)
	}
	got := result.Segments[0].Text
	if !containsAll(got, "pwd="+workDir, "envpwd="+workDir) {
		t.Fatalf("runner cwd output = %q, want pwd and envpwd %s", got, workDir)
	}
}

func TestStreamUpdateParsesClaudeDeltaThinking(t *testing.T) {
	var got []AgentStreamUpdate
	data := []byte(strings.Join([]string{
		`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hidden plan"}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"visible answer"}}`,
	}, "\n"))
	_ = ParseClaudeStreamOutput(data)
	_, err := parseClaudeStream(bytes.NewReader(data), nil, func(update AgentStreamUpdate) {
		got = append(got, update)
	})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("updates = %#v", got)
	}
	if got[0].Segments[0].Kind != card.SegmentThought || got[0].Segments[0].Text != "hidden plan" {
		t.Fatalf("thinking update = %#v", got[0])
	}
	if got[1].Segments[0].Kind != card.SegmentText || got[1].Segments[0].Text != "visible answer" {
		t.Fatalf("text update = %#v", got[1])
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Second}
}

func waitForEvents(t *testing.T, renderer *card.FakeRenderer, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(renderer.Events()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("events len = %d, want >= %d", len(renderer.Events()), n)
}

func waitForCalls(t *testing.T, runner *fakeRunner, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(runner.Calls()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("calls len = %d, want >= %d", len(runner.Calls()), n)
}

func waitForSessionNoActiveBatch(t *testing.T, svc *Service, key session.Key) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if sess, ok := svc.Sessions.Get(key); ok && sess.ActiveBatch == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session %s still has an active batch", key.ID())
}

func waitForNotAccepting(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !svc.isAccepting() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("service remained accepting after Shutdown started")
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
