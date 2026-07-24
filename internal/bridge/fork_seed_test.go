package bridge

import (
	"testing"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

// forkSeedForTopicSession's contract: only Claude, only topic mode with a
// non-empty ThreadID, only when the topic session has never run, and only
// when the chat's root session has an AgentSessionID to fork from.

func TestForkSeedForTopicSessionInheritsRootAgentSession(t *testing.T) {
	svc := &Service{Sessions: session.NewManager()}
	rootKey := session.Key{Agent: agent.Claude, ChatID: "chat"}
	svc.Sessions.GetOrCreate(rootKey, "")
	svc.Sessions.UpdateRunResult(rootKey.ID(), "root-uuid", "", 0)
	topicKey := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "omt-1"}
	got := svc.forkSeedForTopicSession(agent.Claude, topicKey, Message{ChatID: "chat", ThreadID: "omt-1"}, config.ConversationModeTopic)
	if got != "root-uuid" {
		t.Fatalf("fork seed = %q, want root-uuid", got)
	}
}

func TestForkSeedForTopicSessionSkipsWhenTopicAlreadyRan(t *testing.T) {
	svc := &Service{Sessions: session.NewManager()}
	rootKey := session.Key{Agent: agent.Claude, ChatID: "chat"}
	svc.Sessions.GetOrCreate(rootKey, "")
	svc.Sessions.UpdateRunResult(rootKey.ID(), "root-uuid", "", 0)
	topicKey := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "omt-1"}
	svc.Sessions.GetOrCreate(topicKey, "")
	svc.Sessions.UpdateRunResult(topicKey.ID(), "topic-uuid", "", 0)
	if got := svc.forkSeedForTopicSession(agent.Claude, topicKey, Message{ChatID: "chat", ThreadID: "omt-1"}, config.ConversationModeTopic); got != "" {
		t.Fatalf("fork seed = %q, want empty because topic has own session id", got)
	}
}

func TestForkSeedForTopicSessionSkipsCodex(t *testing.T) {
	svc := &Service{Sessions: session.NewManager()}
	rootKey := session.Key{Agent: agent.Codex, ChatID: "chat"}
	svc.Sessions.GetOrCreate(rootKey, "")
	svc.Sessions.UpdateRunResult(rootKey.ID(), "root-uuid", "", 0)
	topicKey := session.Key{Agent: agent.Codex, ChatID: "chat", Thread: "omt-1"}
	if got := svc.forkSeedForTopicSession(agent.Codex, topicKey, Message{ChatID: "chat", ThreadID: "omt-1"}, config.ConversationModeTopic); got != "" {
		t.Fatalf("codex fork seed = %q, want empty", got)
	}
}

func TestForkSeedForTopicSessionSkipsChatMode(t *testing.T) {
	svc := &Service{Sessions: session.NewManager()}
	rootKey := session.Key{Agent: agent.Claude, ChatID: "chat"}
	svc.Sessions.GetOrCreate(rootKey, "")
	svc.Sessions.UpdateRunResult(rootKey.ID(), "root-uuid", "", 0)
	topicKey := session.Key{Agent: agent.Claude, ChatID: "chat"}
	if got := svc.forkSeedForTopicSession(agent.Claude, topicKey, Message{ChatID: "chat", ThreadID: "omt-1"}, config.ConversationModeChat); got != "" {
		t.Fatalf("chat-mode fork seed = %q, want empty", got)
	}
}

func TestForkSeedForTopicSessionSkipsWhenNoRootSession(t *testing.T) {
	svc := &Service{Sessions: session.NewManager()}
	topicKey := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "omt-1"}
	if got := svc.forkSeedForTopicSession(agent.Claude, topicKey, Message{ChatID: "chat", ThreadID: "omt-1"}, config.ConversationModeTopic); got != "" {
		t.Fatalf("fork seed = %q, want empty when no root session exists", got)
	}
}

func TestForkSeedForTopicSessionSkipsWhenRootHasNoAgentSession(t *testing.T) {
	// A root session that has never run itself has an empty AgentSessionID;
	// there is nothing to fork from and Claude --fork-session would error out
	// on an empty resume id.
	svc := &Service{Sessions: session.NewManager()}
	rootKey := session.Key{Agent: agent.Claude, ChatID: "chat"}
	svc.Sessions.GetOrCreate(rootKey, "")
	topicKey := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "omt-1"}
	if got := svc.forkSeedForTopicSession(agent.Claude, topicKey, Message{ChatID: "chat", ThreadID: "omt-1"}, config.ConversationModeTopic); got != "" {
		t.Fatalf("fork seed = %q, want empty when root has no agent session id", got)
	}
}
