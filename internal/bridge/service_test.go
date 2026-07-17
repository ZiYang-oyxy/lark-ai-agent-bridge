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

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

type fakeRunner struct {
	mu      sync.Mutex
	calls   []AgentRunRequest
	results []AgentRunResult
	errs    []error
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
	waitForEvents(t, renderer, 2)
	calls := runner.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	if calls[0].Kind != agent.Claude || calls[0].Prompt != "hello" || calls[0].ClaudeSessionID != "" {
		t.Fatalf("runner call = %#v", calls[0])
	}
	events := renderer.Events()
	if events[0].ReplyToMessageID != "msg-1" || !events[0].StopButton.Visible {
		t.Fatalf("initial event = %#v", events[0])
	}
	last := events[len(events)-1]
	if last.Type != "result" || last.Meta.Model != "claude-sonnet" || last.Meta.Tokens != 42 {
		t.Fatalf("result event = %#v", last)
	}
	status := svc.statusText(agent.Claude, Message{ChatID: "chat"})
	if !containsAll(status, "claude_session=sess-1", "state=idle") {
		t.Fatalf("status = %q, want stored claude session", status)
	}
}

func TestServiceNewWithoutPromptCreatesReadySession(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new", Time: time.Now()}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls())
	}
	events := renderer.Events()
	if len(events) != 1 || events[0].Type != "status" {
		t.Fatalf("events = %#v, want status ready card", events)
	}
}

func TestTopicPlainTextContinuesStoredClaudeSession(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.results = []AgentRunResult{
		{ClaudeSessionID: "sess-topic", Segments: []card.Segment{{Kind: card.SegmentText, Text: "first"}}},
		{ClaudeSessionID: "sess-topic", Segments: []card.Segment{{Kind: card.SegmentText, Text: "second"}}},
	}
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg1 := Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	waitForCalls(t, runner, 1)
	waitForEvents(t, renderer, 2)
	msg2 := Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "second", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[1].ClaudeSessionID != "sess-topic" {
		t.Fatalf("second call session id = %q, want sess-topic", calls[1].ClaudeSessionID)
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
	waitForCalls(t, runner, 1)
	waitForEvents(t, renderer, 2)
	msg2 := Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new second", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("second message error: %v", err)
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
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	events := renderer.Events()
	if events[len(events)-1].Type != "reaction" || events[len(events)-1].Message != "queued" {
		t.Fatalf("queued event = %#v", events[len(events)-1])
	}
	close(runner.block)
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
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "create_workdir", Actor: "u1"}); err != nil {
		t.Fatalf("create action error: %v", err)
	}
	if _, err := os.Stat(missing); err != nil {
		t.Fatalf("missing dir was not created: %v", err)
	}
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].WorkDir; got != missing {
		t.Fatalf("runner workdir = %q, want %q", got, missing)
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
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "cancel_workdir", Actor: "u1"}); err != nil {
		t.Fatalf("cancel action error: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir stat = %v, want not exist", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls())
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

	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + first + " first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
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
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].WorkDir; got != second {
		t.Fatalf("first runner workdir = %q, want %q", got, second)
	}

	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: events[0].SessionID, ActionID: "create_workdir", Actor: "u1"}); err != nil {
		t.Fatalf("first create action error: %v", err)
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
	<-runner.started
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "stop", Actor: "u1"}); err != nil {
		t.Fatalf("stop action error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "stop_button" || !last.StopButton.Disabled {
		t.Fatalf("stop event = %#v", last)
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

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
