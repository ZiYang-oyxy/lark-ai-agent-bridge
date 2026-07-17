package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
	"lark-agent-bridge/internal/tmux"
)

func TestServiceCommandTextCarriesReplyMessageID(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, tmux.NewRecordingRunner()), audit.NewRecorder())
	msg := Message{ID: "message-1", ChatID: "chat", Sender: "u1", Text: "/help", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("help message error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].ReplyToMessageID != "message-1" {
		t.Fatalf("reply message id = %q, want message-1", events[0].ReplyToMessageID)
	}
}

func TestServiceQueuesSecondInputAndRendersReaction(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	first := Message{ChatID: "chat", Sender: "u1", Text: "/claude first", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), first); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	second := Message{ChatID: "chat", Sender: "u1", Text: "/claude second", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), second); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[1].Type != "reaction" || events[1].Message != "queued" {
		t.Fatalf("second event = %#v, want queued reaction", events[1])
	}
}

func TestDifferentChatsCreateIndependentSessions(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat-a", Sender: "u1", Text: "/claude first", Time: time.Now()}); err != nil {
		t.Fatalf("first chat message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat-b", Sender: "u2", Text: "/claude second", Time: time.Now()}); err != nil {
		t.Fatalf("second chat message error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Type != "stream" || events[0].SessionID != "claude:chat-a" {
		t.Fatalf("first event = %#v, want stream for claude:chat-a", events[0])
	}
	if events[1].Type != "stream" || events[1].SessionID != "claude:chat-b" {
		t.Fatalf("second event = %#v, want stream for claude:chat-b", events[1])
	}

	commands := runner.Snapshot()
	foundWindowA := false
	foundWindowB := false
	foundFirstSend := false
	foundSecondSend := false
	for _, cmd := range commands {
		if len(cmd.Args) > 0 && cmd.Args[0] == "new-window" {
			for _, arg := range cmd.Args {
				if arg == "agent-claude-chat-a" {
					foundWindowA = true
				}
				if arg == "agent-claude-chat-b" {
					foundWindowB = true
				}
			}
		}
		for _, arg := range cmd.Args {
			if arg == "first" {
				foundFirstSend = true
			}
			if arg == "second" {
				foundSecondSend = true
			}
		}
	}
	if !foundWindowA || !foundWindowB {
		t.Fatalf("independent chat windows not found in %#v", commands)
	}
	if !foundFirstSend || !foundSecondSend {
		t.Fatalf("independent chat prompts not sent in %#v", commands)
	}
}

func TestDifferentTopicsCreateIndependentSessions(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/claude first", Time: time.Now()}); err != nil {
		t.Fatalf("first topic message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", ThreadID: "topic-b", Sender: "u2", Text: "/claude second", Time: time.Now()}); err != nil {
		t.Fatalf("second topic message error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Type != "stream" || events[0].SessionID != "claude:chat:thread:topic-a" {
		t.Fatalf("first event = %#v, want stream for topic-a", events[0])
	}
	if events[1].Type != "stream" || events[1].SessionID != "claude:chat:thread:topic-b" {
		t.Fatalf("second event = %#v, want stream for topic-b", events[1])
	}

	commands := runner.Snapshot()
	foundTopicA := false
	foundTopicB := false
	for _, cmd := range commands {
		if len(cmd.Args) == 0 || cmd.Args[0] != "new-window" {
			continue
		}
		for _, arg := range cmd.Args {
			if arg == "agent-claude-chat-thread-topic-a" {
				foundTopicA = true
			}
			if arg == "agent-claude-chat-thread-topic-b" {
				foundTopicB = true
			}
		}
	}
	if !foundTopicA || !foundTopicB {
		t.Fatalf("independent topic windows not found in %#v", commands)
	}
}

func TestServiceAttachRendersTmuxCommand(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, tmux.NewRecordingRunner()), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/attach", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("attach error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].Segments[0].Text == "" {
		t.Fatal("attach command is empty")
	}
}

func TestServiceAsksBeforeCreatingMissingWorkdir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude --workdir " + missing + " hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].Type != "workdir_confirm" {
		t.Fatalf("event type = %s, want workdir_confirm", events[0].Type)
	}
	if len(runner.Snapshot()) != 0 {
		t.Fatalf("tmux commands = %#v, want none before confirmation", runner.Snapshot())
	}
}

func TestCreateWorkdirActionResumesPendingRun(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/claude --workdir " + missing + " hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "create_workdir", Value: missing, Actor: "u1"}); err != nil {
		t.Fatalf("action error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "stream" {
		t.Fatalf("last event type = %s, want stream", last.Type)
	}
	if last.Segments[0].Text != "hello" {
		t.Fatalf("prompt = %q, want hello", last.Segments[0].Text)
	}
	commands := runner.Snapshot()
	foundWorkdir := false
	foundPrompt := false
	for _, cmd := range commands {
		for _, arg := range cmd.Args {
			if arg == missing {
				foundWorkdir = true
			}
			if arg == "hello" {
				foundPrompt = true
			}
		}
	}
	if !foundWorkdir {
		t.Fatalf("workdir %q not found in tmux commands %#v", missing, commands)
	}
	if !foundPrompt {
		t.Fatalf("prompt send not found in tmux commands %#v", commands)
	}
}

func TestWorkdirConfirmTimesOutAndCancelsPendingRun(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude --workdir " + missing + " hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	if err := svc.RenderPendingRunTimeouts(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("timeout error: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing workdir was created or stat failed: %v", err)
	}
	if len(runner.Snapshot()) != 0 {
		t.Fatalf("tmux commands = %#v, want none after timeout", runner.Snapshot())
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "action" || last.Message != "workdir creation timed out: cancelled" {
		t.Fatalf("last event = %#v, want timeout cancel action", last)
	}
}

func TestCreateWorkdirActionClearsPendingRunTimeout(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/claude --workdir " + missing + " hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "create_workdir", Value: missing, Actor: "u1"}); err != nil {
		t.Fatalf("action error: %v", err)
	}
	if err := svc.RenderPendingRunTimeouts(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("timeout error: %v", err)
	}
	events := renderer.Events()
	for _, event := range events {
		if event.Message == "workdir creation timed out: cancelled" {
			t.Fatalf("unexpected timeout cancel after create action: %#v", events)
		}
	}
}

func TestServiceStopActionInterruptsAndDisablesButton(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "stop", Actor: "u1"}); err != nil {
		t.Fatalf("action error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "stop_button" || !last.StopButton.Disabled {
		t.Fatalf("last event = %#v, want disabled stop button", last)
	}
	if last.Meta.Status != "idle" {
		t.Fatalf("status after stop = %s, want idle", last.Meta.Status)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/claude next", Time: time.Now()}); err != nil {
		t.Fatalf("next message error: %v", err)
	}
	events = renderer.Events()
	last = events[len(events)-1]
	if last.Type != "stream" || last.SessionID != "claude:chat" {
		t.Fatalf("next event = %#v, want direct stream after stop", last)
	}
}

func TestServiceStopActionSuppressesTrailingInterruptOutput(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/claude sleep", Time: time.Now()}); err != nil {
		t.Fatalf("message error: %v", err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "stop", Actor: "u1"}); err != nil {
		t.Fatalf("action error: %v", err)
	}
	before := renderer.Events()
	last := before[len(before)-1]
	if last.Type != "stop_button" || !last.StopButton.Disabled {
		t.Fatalf("last event = %#v, want disabled stop button", last)
	}
	captureKey := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[captureKey] = [][]byte{[]byte("Conversation interrupted\n")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	after := renderer.Events()
	if len(after) != len(before) {
		t.Fatalf("events len = %d, want %d; last = %#v", len(after), len(before), after[len(after)-1])
	}
}

func TestCodexStopActionKillsPaneToolProcesses(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/codex sleep", Time: time.Now()}); err != nil {
		t.Fatalf("message error: %v", err)
	}
	runner.Responses["tmux display-message -p -t lark-agent-bridge:agent-codex-chat #{pane_pid}"] = [][]byte{[]byte("100\n")}
	runner.Responses["ps -axo pid=,ppid=,command="] = [][]byte{[]byte(`
100 1 fish -c codex
101 100 /opt/homebrew/bin/codex --dangerously-bypass-approvals-and-sandbox
102 101 /bin/zsh -c sleep 300 && echo LAB
103 102 sleep 300
`)}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "codex:chat", ActionID: "stop", Actor: "u1"}); err != nil {
		t.Fatalf("action error: %v", err)
	}
	foundKill := false
	for _, cmd := range runner.Snapshot() {
		if cmd.Name == "kill" {
			foundKill = true
			got := strings.Join(cmd.Args, " ")
			if got != "-TERM 102 103" {
				t.Fatalf("kill args = %q, want tool pids only", got)
			}
		}
	}
	if !foundKill {
		t.Fatalf("kill command not found in %#v", runner.Snapshot())
	}
}

func TestServiceStopActionDequeuesNextInput(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/claude first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u2", Text: "/claude second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "stop", Actor: "u1"}); err != nil {
		t.Fatalf("stop action error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "dequeue" || last.Segments[0].Text != "second" {
		t.Fatalf("last event = %#v, want dequeue of second prompt", last)
	}
	foundInterrupt := false
	foundSecondSend := false
	for _, cmd := range runner.Snapshot() {
		for _, arg := range cmd.Args {
			if arg == "C-c" {
				foundInterrupt = true
			}
			if arg == "second" {
				foundSecondSend = true
			}
		}
	}
	if !foundInterrupt || !foundSecondSend {
		t.Fatalf("interrupt=%t second_send=%t commands=%#v", foundInterrupt, foundSecondSend, runner.Snapshot())
	}
}

func TestServiceCleanupKillsTmuxSessionAndAudits(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	recorder := audit.NewRecorder()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, card.NewFakeRenderer(), tmux.NewManager(cfg.TmuxSession, runner), recorder)
	if err := svc.Cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup error: %v", err)
	}
	foundKill := false
	for _, cmd := range runner.Snapshot() {
		if cmd.Name == "tmux" && strings.Join(cmd.Args, " ") == "kill-session -t lark-agent-bridge" {
			foundKill = true
		}
	}
	if !foundKill {
		t.Fatalf("kill-session command not found in %#v", runner.Snapshot())
	}
	events := recorder.Events()
	if len(events) != 1 || events[0].Action != "cleanup" {
		t.Fatalf("audit events = %#v, want cleanup event", events)
	}
}

func TestServiceRedactsRenderedCardOutput(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, tmux.NewRecordingRunner()), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude token=secret-value", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	events := renderer.Events()
	if len(events) == 0 {
		t.Fatal("no events rendered")
	}
	got := events[0].Segments[0].Text
	if strings.Contains(got, "secret-value") {
		t.Fatalf("rendered segment leaked secret: %q", got)
	}
	if !strings.Contains(got, "token=[REDACTED]") {
		t.Fatalf("rendered segment = %q, want redacted token", got)
	}
}

func TestPollSessionOutputDetectsAuthorization(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("Tool permission required\nAllow once\nReject")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "authorization" {
		t.Fatalf("last type = %s, want authorization", last.Type)
	}
	if len(last.Actions) != 4 {
		t.Fatalf("actions = %#v, want 4 authorization actions", last.Actions)
	}
}

func TestChoiceActionWritesSelectionToAgent(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Minute}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("请选择:\n1. repo top3\n2. AI only")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	last := renderer.Events()[len(renderer.Events())-1]
	if last.Type != "choice" || len(last.Actions) != 2 {
		t.Fatalf("last event = %#v, want choice with two actions", last)
	}
	if last.Actions[0].ID != "choice_1" || last.Actions[0].Label != "repo top3" || last.Actions[0].Value != "1" {
		t.Fatalf("first choice action = %#v", last.Actions[0])
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "choice_1", Value: "1", Actor: "u1"}); err != nil {
		t.Fatalf("choice action error: %v", err)
	}
	found := false
	for _, cmd := range runner.Snapshot() {
		if cmd.Name == "tmux" && len(cmd.Args) > 0 && cmd.Args[0] == "send-keys" {
			for _, arg := range cmd.Args {
				if arg == "1" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("choice selection was not written to tmux: %#v", runner.Snapshot())
	}
}

func TestCodexInitialInputWaitsForReadyPrompt(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Minute}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/codex hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	for _, cmd := range runner.Snapshot() {
		if cmd.Name == "tmux" && len(cmd.Args) > 0 && cmd.Args[0] == "send-keys" {
			for _, arg := range cmd.Args {
				if arg == "hello" {
					t.Fatalf("codex initial input was sent before ready: %#v", runner.Snapshot())
				}
			}
		}
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-codex-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("gpt-5.5 xhigh · /tmp/work · Context 0% used\n› Implement {feature}")}
	if err := svc.PollSessionOutput(context.Background(), "codex:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	found := false
	for _, cmd := range runner.Snapshot() {
		if cmd.Name == "tmux" && len(cmd.Args) > 0 && cmd.Args[0] == "send-keys" {
			for _, arg := range cmd.Args {
				if arg == "hello" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("codex initial input was not sent after ready: %#v", runner.Snapshot())
	}
}

func TestPollActiveSessionsSkipsSessionBeforeWindowStarted(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "codex", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Minute}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	svc.Sessions.Enqueue(session.Key{Agent: agent.Codex, ChatID: "chat"}, session.Input{Sender: "u1", Text: "hello", Time: time.Now()}, cfg.DefaultWorkDir)
	if err := svc.PollActiveSessions(context.Background()); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	for _, cmd := range runner.Snapshot() {
		if cmd.Name == "tmux" && len(cmd.Args) > 0 && cmd.Args[0] == "capture-pane" {
			t.Fatalf("poll captured before window started: %#v", runner.Snapshot())
		}
	}
}

func TestResumeCandidateActionWritesSelectionToAgent(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Minute}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("resume session\n1 34ccac3d 0s ago query\n2 def456 1m ago inspect")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	last := renderer.Events()[len(renderer.Events())-1]
	if last.Type != "resume" || len(last.Actions) != 3 {
		t.Fatalf("last event = %#v, want resume with two candidates and cancel", last)
	}
	if last.Actions[0].ID != "resume_1" || last.Actions[0].Label != "34ccac3d 0s ago query" || last.Actions[0].Value != "1" {
		t.Fatalf("first resume action = %#v", last.Actions[0])
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "resume_1", Value: "1", Actor: "u1"}); err != nil {
		t.Fatalf("resume action error: %v", err)
	}
	found := false
	for _, cmd := range runner.Snapshot() {
		if cmd.Name == "tmux" && len(cmd.Args) > 0 && cmd.Args[0] == "send-keys" {
			for _, arg := range cmd.Args {
				if arg == "1" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("resume selection was not written to tmux: %#v", runner.Snapshot())
	}
	runner.Responses[key] = [][]byte{[]byte("screen redraw\nresume session\n1 34ccac3d 0s ago query\n2 def456 1m ago inspect\ncontinuing after selection")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll repeated resume output error: %v", err)
	}
	last = renderer.Events()[len(renderer.Events())-1]
	if last.Type == "resume" {
		t.Fatalf("repeated handled resume candidates rendered another resume event: %#v", last)
	}
}

func TestInteractionTimeoutRejectsAuthorization(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("Tool permission required\nAllow once\nReject")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	if err := svc.RenderInteractionTimeouts(context.Background(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatalf("timeout error: %v", err)
	}
	foundReject := false
	for _, cmd := range runner.Snapshot() {
		if len(cmd.Args) > 0 && cmd.Args[0] == "send-keys" {
			for _, arg := range cmd.Args {
				if arg == "reject" {
					foundReject = true
				}
			}
		}
	}
	if !foundReject {
		t.Fatalf("reject send not found in %#v", runner.Snapshot())
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "action" || last.Message != "interaction timed out: sent reject" {
		t.Fatalf("last event = %#v, want timeout action", last)
	}
}

func TestInteractionActionClearsTimeout(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("Tool permission required\nAllow once\nReject")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "allow_once", Value: "allow_once", Actor: "u1"}); err != nil {
		t.Fatalf("action error: %v", err)
	}
	if err := svc.RenderInteractionTimeouts(context.Background(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatalf("timeout error: %v", err)
	}
	sendValues := 0
	for _, cmd := range runner.Snapshot() {
		if len(cmd.Args) > 0 && cmd.Args[0] == "send-keys" {
			for _, arg := range cmd.Args {
				if arg == "allow_once" || arg == "reject" {
					sendValues++
				}
			}
		}
	}
	if sendValues != 1 {
		t.Fatalf("interaction send values = %d, want only allow_once: %#v", sendValues, runner.Snapshot())
	}
}

func TestUnknownActionDoesNotClearInteractionTimeout(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("Tool permission required\nAllow once\nReject")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "unknown", Actor: "u1"}); err != nil {
		t.Fatalf("unknown action render error: %v", err)
	}
	if err := svc.RenderInteractionTimeouts(context.Background(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatalf("timeout error: %v", err)
	}
	foundReject := false
	for _, cmd := range runner.Snapshot() {
		if len(cmd.Args) > 0 && cmd.Args[0] == "send-keys" {
			for _, arg := range cmd.Args {
				if arg == "reject" {
					foundReject = true
				}
			}
		}
	}
	if !foundReject {
		t.Fatalf("reject send not found after unknown action in %#v", runner.Snapshot())
	}
}

func TestPollSessionOutputMarksCrashedAndOffersRestart(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Fail[key] = errors.New("pane missing")
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "error" {
		t.Fatalf("last event type = %s, want error", last.Type)
	}
	if last.Meta.Status != "crashed" {
		t.Fatalf("status = %s, want crashed", last.Meta.Status)
	}
	if len(last.Actions) != 1 || last.Actions[0].ID != "restart_session" {
		t.Fatalf("actions = %#v, want restart_session", last.Actions)
	}
}

func TestRestartSessionReusesApprovalMode(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/codex --approval full inspect", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-codex-chat -S -300"
	runner.Fail[key] = errors.New("pane missing")
	if err := svc.PollSessionOutput(context.Background(), "codex:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "codex:chat", ActionID: "restart_session", Actor: "u1"}); err != nil {
		t.Fatalf("restart error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "status" || last.Meta.Status != "idle" || last.Message != "session restarted" {
		t.Fatalf("last event = %#v, want restarted idle status", last)
	}
	newWindowCommands := 0
	foundFullMode := false
	for _, cmd := range runner.Snapshot() {
		if len(cmd.Args) > 0 && cmd.Args[0] == "new-window" {
			newWindowCommands++
			for _, arg := range cmd.Args {
				if strings.Contains(arg, "--dangerously-bypass-approvals-and-sandbox") {
					foundFullMode = true
				}
			}
		}
	}
	if newWindowCommands != 2 {
		t.Fatalf("new-window commands = %d, want 2", newWindowCommands)
	}
	if !foundFullMode {
		t.Fatalf("full mode codex command not found in %#v", runner.Snapshot())
	}
}

func TestServiceResumeLaunchesAgentWindow(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/resume codex --approval full --last", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("resume message error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "status" || last.Message != "resume session launched" {
		t.Fatalf("last event = %#v, want resume status", last)
	}
	foundResume := false
	foundSendKeys := false
	for _, cmd := range runner.Snapshot() {
		if len(cmd.Args) > 0 && cmd.Args[0] == "new-window" {
			for _, arg := range cmd.Args {
				if strings.Contains(arg, "codex -c check_for_update_on_startup=false --dangerously-bypass-approvals-and-sandbox resume --last") {
					foundResume = true
				}
			}
		}
		if len(cmd.Args) > 0 && cmd.Args[0] == "send-keys" {
			foundSendKeys = true
		}
	}
	if !foundResume {
		t.Fatalf("codex resume command not found in %#v", runner.Snapshot())
	}
	if foundSendKeys {
		t.Fatalf("resume should not send stdin, commands = %#v", runner.Snapshot())
	}
}

func TestServiceResumeRequiresInactiveWindow(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}); err != nil {
		t.Fatalf("run message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/resume claude 34cc", Time: time.Now()}); err != nil {
		t.Fatalf("resume message error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "error" || !strings.Contains(last.Segments[0].Text, "active tmux window") {
		t.Fatalf("last event = %#v, want active window error", last)
	}
	newWindowCommands := 0
	for _, cmd := range runner.Snapshot() {
		if len(cmd.Args) > 0 && cmd.Args[0] == "new-window" {
			newWindowCommands++
		}
	}
	if newWindowCommands != 1 {
		t.Fatalf("new-window commands = %d, want only initial run", newWindowCommands)
	}
}

func TestRenderIdleReminders(t *testing.T) {
	cfg := config.Config{
		TmuxSession:       config.DefaultTmuxSession,
		DefaultAgent:      "claude",
		DefaultWorkDir:    t.TempDir(),
		CardMaxChars:      1000,
		IdleReminderAfter: 24 * time.Hour,
	}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	old := time.Now().Add(-25 * time.Hour)
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: old}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	svc.Sessions.CompleteAt(session.Key{Agent: agent.Claude, ChatID: "chat"}, cfg.DefaultWorkDir, old)
	if err := svc.RenderIdleReminders(time.Now()); err != nil {
		t.Fatalf("idle reminder error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "idle_reminder" {
		t.Fatalf("last type = %s, want idle_reminder", last.Type)
	}
	if len(last.Segments) == 0 || !strings.Contains(last.Segments[0].Text, "24h0m0s") {
		t.Fatalf("idle reminder text = %#v, want configured idle duration", last.Segments)
	}
	if len(last.Actions) != 1 || last.Actions[0].ID != "terminate_session" || last.Actions[0].Disabled {
		t.Fatalf("idle reminder actions = %#v, want enabled terminate_session", last.Actions)
	}
	for _, cmd := range runner.Snapshot() {
		if len(cmd.Args) > 0 && cmd.Args[0] == "kill-window" {
			t.Fatalf("idle reminder killed tmux window before user action: %#v", runner.Snapshot())
		}
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat", ActionID: "terminate_session", Actor: "u1"}); err != nil {
		t.Fatalf("terminate action error: %v", err)
	}
	events = renderer.Events()
	last = events[len(events)-1]
	if last.Type != "terminate_session" || last.Message != "session terminated" || last.Meta.Status != "stopped" {
		t.Fatalf("last event = %#v, want terminated stopped session", last)
	}
	if len(last.Actions) != 1 || !last.Actions[0].Disabled {
		t.Fatalf("terminate actions = %#v, want disabled action", last.Actions)
	}
	foundKillWindow := false
	for _, cmd := range runner.Snapshot() {
		if len(cmd.Args) > 0 && cmd.Args[0] == "kill-window" {
			for _, arg := range cmd.Args {
				if arg == "lark-agent-bridge:agent-claude-chat" {
					foundKillWindow = true
				}
			}
		}
	}
	if !foundKillWindow {
		t.Fatalf("kill-window command not found in %#v", runner.Snapshot())
	}
}

func TestPollOutputRefreshesIdleReminderClock(t *testing.T) {
	cfg := config.Config{
		TmuxSession:       config.DefaultTmuxSession,
		DefaultAgent:      "claude",
		DefaultWorkDir:    t.TempDir(),
		CardMaxChars:      1000,
		IdleReminderAfter: 24 * time.Hour,
	}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	old := time.Now().Add(-25 * time.Hour)
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: old}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := session.Key{Agent: agent.Claude, ChatID: "chat"}
	svc.Sessions.CompleteAt(key, cfg.DefaultWorkDir, old)
	captureKey := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[captureKey] = [][]byte{[]byte("late output after idle\n")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll output error: %v", err)
	}
	if err := svc.RenderIdleReminders(time.Now()); err != nil {
		t.Fatalf("idle reminder error: %v", err)
	}
	for _, event := range renderer.Events() {
		if event.Type == "idle_reminder" {
			t.Fatalf("unexpected idle reminder after output touch: %#v", event)
		}
	}
}

func TestPollActiveSessionsPollsRunningSession(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("thinking about it")}
	if err := svc.PollActiveSessions(context.Background()); err != nil {
		t.Fatalf("poll active error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "stream" {
		t.Fatalf("last type = %s, want stream", last.Type)
	}
}

func TestPollActiveSessionsIgnoresStartupWindowRace(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), recorder)
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Fail[key] = errors.New("can't find window: agent-claude-chat")
	if err := svc.PollActiveSessions(context.Background()); err != nil {
		t.Fatalf("poll active error: %v", err)
	}
	for _, event := range renderer.Events() {
		if event.Type == "error" {
			t.Fatalf("unexpected error event during startup race: %#v", event)
		}
	}
	for _, event := range recorder.Events() {
		if event.Action == "session_crashed" {
			t.Fatalf("unexpected crash audit during startup race: %#v", event)
		}
	}
}

func TestPollSessionOutputStillCrashesOnWindowMissing(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), recorder)
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Fail[key] = errors.New("can't find window: agent-claude-chat")
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll output error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "error" {
		t.Fatalf("last type = %s, want error", last.Type)
	}
	audits := recorder.Events()
	lastAudit := audits[len(audits)-1]
	if lastAudit.Action != "session_crashed" {
		t.Fatalf("last audit action = %s, want session_crashed", lastAudit.Action)
	}
}

func TestCrashedSessionUsesNewCommandWorkDir(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "codex", DefaultWorkDir: oldDir, CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), recorder)
	first := Message{ChatID: "chat", Sender: "u1", Text: "/codex --workdir " + oldDir + " first", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), first); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-codex-chat -S -300"
	runner.Fail[key] = errors.New("can't find window: agent-codex-chat")
	if err := svc.PollSessionOutput(context.Background(), "codex:chat"); err != nil {
		t.Fatalf("poll output error: %v", err)
	}
	delete(runner.Fail, key)
	second := Message{ChatID: "chat", Sender: "u1", Text: "/codex --workdir " + newDir + " second", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), second); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	foundNewWorkDir := false
	for _, cmd := range runner.Snapshot() {
		for i, arg := range cmd.Args {
			if arg == "-c" && i+1 < len(cmd.Args) && cmd.Args[i+1] == newDir {
				foundNewWorkDir = true
			}
		}
	}
	if !foundNewWorkDir {
		t.Fatalf("new workdir %q not found in tmux commands %#v", newDir, runner.Snapshot())
	}
}

func TestSegmentOutputSplitsMixedRichSegments(t *testing.T) {
	got := segmentOutput("plain\nThinking: inspect plan\nTool: Bash ls\nfinal\n")
	if len(got) != 4 {
		t.Fatalf("segments len = %d, want 4: %#v", len(got), got)
	}
	wants := []card.Segment{
		{Kind: card.SegmentText, Text: "plain\n"},
		{Kind: card.SegmentThought, Text: "Thinking: inspect plan\n"},
		{Kind: card.SegmentTool, Text: "Tool: Bash ls\n"},
		{Kind: card.SegmentText, Text: "final\n"},
	}
	for i := range wants {
		if got[i] != wants[i] {
			t.Fatalf("segment[%d] = %#v, want %#v", i, got[i], wants[i])
		}
	}
}

func TestPollSessionOutputRendersMixedRichSegments(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("plain\nThinking: inspect plan\nTool: Bash ls\nfinal\n")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "stream" {
		t.Fatalf("last type = %s, want stream", last.Type)
	}
	kinds := make([]card.SegmentKind, 0, len(last.Segments))
	for _, segment := range last.Segments {
		kinds = append(kinds, segment.Kind)
	}
	want := []card.SegmentKind{card.SegmentText, card.SegmentThought, card.SegmentTool, card.SegmentText}
	if strings.Join(segmentKinds(kinds), ",") != strings.Join(segmentKinds(want), ",") {
		t.Fatalf("segment kinds = %#v, want %#v", kinds, want)
	}
}

func segmentKinds(kinds []card.SegmentKind) []string {
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, string(kind))
	}
	return out
}

func TestServiceStatusIncludesCurrentSessionDetails(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/claude first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/claude second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/status", Time: time.Now()}); err != nil {
		t.Fatalf("status error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "message" {
		t.Fatalf("last event type = %s, want message", last.Type)
	}
	status := last.Segments[0].Text
	for _, want := range []string{
		"current_session=claude:chat",
		"state=running",
		"window=agent-claude-chat",
		"window_started=true",
		"queue=1",
		"history=2",
		"attach=tmux attach -t lark-agent-bridge:agent-claude-chat",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status %q does not contain %q", status, want)
		}
	}
}

func TestServiceSessionsIncludesOperationalDetails(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", ThreadID: "topic", Sender: "u1", Text: "/codex --approval full inspect", Time: time.Now()}); err != nil {
		t.Fatalf("message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", ThreadID: "topic", Sender: "u1", Text: "/sessions", Time: time.Now()}); err != nil {
		t.Fatalf("sessions error: %v", err)
	}
	events := renderer.Events()
	text := events[len(events)-1].Segments[0].Text
	for _, want := range []string{
		"codex:chat:thread:topic",
		"agent=codex chat=chat thread=topic",
		"state=running",
		"window=agent-codex-chat-thread-topic",
		"window_started=true",
		"queue=0 history=1",
		"approval=full",
		"last_active=",
		"attach=tmux attach -t lark-agent-bridge:agent-codex-chat-thread-topic",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("sessions text %q does not contain %q", text, want)
		}
	}
}

func TestServiceManagementCommandsCanTargetAgent(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/codex inspect", Time: time.Now()}); err != nil {
		t.Fatalf("codex message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/status codex", Time: time.Now()}); err != nil {
		t.Fatalf("status error: %v", err)
	}
	events := renderer.Events()
	status := events[len(events)-1].Segments[0].Text
	if !strings.Contains(status, "current_session=codex:chat") {
		t.Fatalf("status %q does not target codex session", status)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/stop codex", Time: time.Now()}); err != nil {
		t.Fatalf("stop error: %v", err)
	}
	foundKillCodex := false
	for _, cmd := range runner.Snapshot() {
		for _, arg := range cmd.Args {
			if arg == "lark-agent-bridge:agent-codex-chat" {
				foundKillCodex = true
			}
		}
	}
	if !foundKillCodex {
		t.Fatalf("codex tmux target not found in %#v", runner.Snapshot())
	}
}

func TestTopicModeControlsThreadSessionKey(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", ThreadID: "topic-1", Sender: "u1", Text: "/claude first", Time: time.Now()}); err != nil {
		t.Fatalf("thread message error: %v", err)
	}
	events := renderer.Events()
	first := events[len(events)-1]
	if first.SessionID != "claude:chat:thread:topic-1" {
		t.Fatalf("session id = %s, want thread-scoped session", first.SessionID)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", ThreadID: "topic-1", Sender: "u1", Text: "/topic off", Time: time.Now()}); err != nil {
		t.Fatalf("topic off error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", ThreadID: "topic-2", Sender: "u1", Text: "/claude second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	events = renderer.Events()
	last := events[len(events)-1]
	if last.SessionID != "claude:chat" {
		t.Fatalf("session id = %s, want chat-scoped session after topic off", last.SessionID)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", ThreadID: "topic-2", Sender: "u1", Text: "/status", Time: time.Now()}); err != nil {
		t.Fatalf("status error: %v", err)
	}
	status := renderer.Events()[len(renderer.Events())-1].Segments[0].Text
	if !strings.Contains(status, "topic_mode=false") {
		t.Fatalf("status %q does not show topic mode disabled", status)
	}
}

func TestPollSessionOutputUpdatesRuntimeMeta(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("model: claude-sonnet-4\ntokens: 123\nthinking")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Meta.Model != "claude-sonnet-4" {
		t.Fatalf("model = %q, want claude-sonnet-4", last.Meta.Model)
	}
	if last.Meta.Tokens != 123 {
		t.Fatalf("tokens = %d, want 123", last.Meta.Tokens)
	}
}

func TestPollSessionOutputPaginatesLongStreamEvents(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 40}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte(strings.Repeat("x", 200))}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	events := renderer.Events()
	var pages []card.Event
	for _, event := range events {
		if strings.HasPrefix(event.Message, "page ") {
			pages = append(pages, event)
		}
	}
	if len(pages) != 5 {
		t.Fatalf("page events len = %d, want 5: %#v", len(pages), pages)
	}
	for i, page := range pages {
		if page.SessionID != "claude:chat" {
			t.Fatalf("page session id = %q, want claude:chat", page.SessionID)
		}
		if page.Message != fmt.Sprintf("page %d/5", i+1) {
			t.Fatalf("page message = %q, want page %d/5", page.Message, i+1)
		}
		text := page.Segments[0].Text
		if got := len([]rune(text)); got > cfg.CardMaxChars {
			t.Fatalf("stream page length = %d, want <= %d", got, cfg.CardMaxChars)
		}
		if strings.Contains(text, "[truncated]") {
			t.Fatalf("stream page = %q, did not expect truncation marker", text)
		}
	}
}

func TestReadyOutputDequeuesNextInput(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	first := Message{ChatID: "chat", Sender: "u1", Text: "/claude first", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), first); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	second := Message{ChatID: "chat", Sender: "u1", Text: "/claude second", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), second); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("done\n>")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "dequeue" {
		t.Fatalf("last event type = %s, want dequeue", last.Type)
	}
	commands := runner.Snapshot()
	foundSecondSend := false
	for _, cmd := range commands {
		for _, arg := range cmd.Args {
			if arg == "second" {
				foundSecondSend = true
			}
		}
	}
	if !foundSecondSend {
		t.Fatalf("send-keys for second input not found in %#v", commands)
	}
}

func TestIdleSessionReusesExistingWindow(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	svc := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	msg := Message{ChatID: "chat", Sender: "u1", Text: "/claude first", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{[]byte("done\n>")}
	if err := svc.PollSessionOutput(context.Background(), "claude:chat"); err != nil {
		t.Fatalf("poll error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ChatID: "chat", Sender: "u1", Text: "/claude third", Time: time.Now()}); err != nil {
		t.Fatalf("third message error: %v", err)
	}
	newWindowCount := 0
	for _, cmd := range runner.Snapshot() {
		if len(cmd.Args) > 0 && cmd.Args[0] == "new-window" {
			newWindowCount++
		}
	}
	if newWindowCount != 1 {
		t.Fatalf("new-window count = %d, want 1", newWindowCount)
	}
}
