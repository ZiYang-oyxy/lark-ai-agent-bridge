package bridge

import (
	"bytes"
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
	if events[0].HeaderTemplate != "blue" || !containsAll(events[0].HeaderTitle, "正在推理", "⏱") {
		t.Fatalf("initial header = %q/%q", events[0].HeaderTemplate, events[0].HeaderTitle)
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
	waitForEvents(t, renderer, 5)
	events := renderer.Events()
	if events[1].Type != "stream" || !events[1].ThoughtExpanded {
		t.Fatalf("thought stream event = %#v", events[1])
	}
	if events[2].Type != "stream" || !events[2].ToolsExpanded {
		t.Fatalf("tool stream event = %#v", events[2])
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
		{ClaudeSessionID: "sess-topic", Tokens: 2, Segments: []card.Segment{{Kind: card.SegmentText, Text: "first"}}},
		{ClaudeSessionID: "sess-topic", Tokens: 3, Segments: []card.Segment{{Kind: card.SegmentText, Text: "second"}}},
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
	waitForEvents(t, renderer, 4)
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
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].WorkDir; got != missing {
		t.Fatalf("runner workdir = %q, want %q", got, missing)
	}
	waitForEvents(t, renderer, 4)
	events = renderer.Events()
	if events[1].Type != "workdir_created" || events[1].SessionID != "claude:chat:message:msg-1" {
		t.Fatalf("terminal confirm event = %#v", events[1])
	}
	runEvent := events[2]
	if runEvent.Type != "stream" || runEvent.SessionID == events[1].SessionID || runEvent.ReplyToMessageID != "msg-1" {
		t.Fatalf("run event = %#v, want separate card replying to original message", runEvent)
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

	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + first + " first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + second + " second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	close(runner.block)
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[0].WorkDir != first {
		t.Fatalf("first workdir = %q, want %q", calls[0].WorkDir, first)
	}
	if calls[1].WorkDir != second {
		t.Fatalf("queued workdir = %q, want %q", calls[1].WorkDir, second)
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

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
