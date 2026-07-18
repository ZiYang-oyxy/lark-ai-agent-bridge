package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/session"
)

func TestReactionLifecycleDeletesAddThatCompletesAfterClose(t *testing.T) {
	sink := &fakeReactionSink{
		blockType: feishu.ReactionTypeOneSecond,
		entered:   make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	handle := newReactionLifecycle(sink, audit.NewRecorder(), "message", feishu.ReactionTypeOneSecond, 0)
	<-sink.entered
	handle.Close()
	close(sink.release)
	waitForReactionCounts(t, sink, 1, 1)
	_, deletes := sink.snapshot()
	if deletes[0].messageID != "message" || deletes[0].reactionID != "reaction-1" {
		t.Fatalf("late add cleanup = %#v", deletes)
	}
}

func TestReactionLifecycleAuditsDeleteFailure(t *testing.T) {
	sink := &fakeReactionSink{deleteErr: errors.New("delete unavailable")}
	recorder := audit.NewRecorder()
	handle := newReactionLifecycle(sink, recorder, "message", feishu.ReactionTypeTyping, 0)
	waitForReactionCounts(t, sink, 1, 0)
	handle.Close()
	waitForReactionCounts(t, sink, 1, 1)
	waitForAuditAction(t, recorder, "reaction_delete_failed")
}

func TestServiceDeletesTypingOnRunTerminalPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		finish func(*Service, *fakeRunner)
	}{
		{name: "success", finish: func(_ *Service, runner *fakeRunner) { close(runner.block) }},
		{name: "agent failure", finish: func(_ *Service, runner *fakeRunner) {
			runner.mu.Lock()
			runner.errs = []error{errors.New("spawn failed")}
			runner.mu.Unlock()
			close(runner.block)
		}},
		{name: "stop", finish: func(svc *Service, _ *fakeRunner) {
			if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat:message:message", ActionID: "stop", Actor: "user"}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := newFakeRunner()
			runner.block = make(chan struct{})
			sink := &fakeReactionSink{}
			svc := NewService(testConfig(t), card.NewFakeRenderer(), runner, audit.NewRecorder())
			svc.Reactions = sink
			svc.reactionDelay = time.Hour
			now := time.Now()
			if err := svc.HandleMessage(context.Background(), Message{ID: "message", ChatID: "chat", Sender: "user", Text: "work", Time: now}); err != nil {
				t.Fatal(err)
			}
			if err := svc.DrainReady(now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			<-runner.started
			waitForReactionCounts(t, sink, 1, 0)
			adds, _ := sink.snapshot()
			if adds[0] != (reactionCall{messageID: "message", typeName: feishu.ReactionTypeTyping}) {
				t.Fatalf("typing add = %#v", adds)
			}
			tc.finish(svc, runner)
			waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
			waitForReactionCounts(t, sink, 1, 1)
		})
	}
}

func TestServiceQueuedRecallDeletesWaitingWithoutTyping(t *testing.T) {
	sink := &fakeReactionSink{}
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Reactions = sink
	svc.reactionDelay = 0
	if err := svc.HandleMessage(context.Background(), Message{ID: "queued", ChatID: "chat", Sender: "user", Text: "work", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	waitForReactionCounts(t, sink, 1, 0)
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "queued", ChatID: "chat"}); err != nil {
		t.Fatal(err)
	}
	waitForReactionCounts(t, sink, 1, 1)
	adds, _ := sink.snapshot()
	if len(adds) != 1 || adds[0].typeName != feishu.ReactionTypeOneSecond {
		t.Fatalf("queued recall reactions = %#v", adds)
	}
}
