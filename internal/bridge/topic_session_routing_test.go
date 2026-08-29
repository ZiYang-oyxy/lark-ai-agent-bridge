package bridge

import (
	"context"
	"testing"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
)

func TestRunWithPreferenceHealsPollutedTopicAlias(t *testing.T) {
	realKey := session.Key{Agent: agent.Claude, ChatID: "oc_chat", Thread: "omt_topic"}
	syntheticKey := session.Key{Agent: agent.Claude, ChatID: "oc_chat", Thread: SyntheticTopicThreadPrefix + "om_root"}

	cfg := testConfig(t)
	svc := NewService(cfg, nil, newFakeRunner(), nil)
	svc.Sessions.GetOrCreate(realKey, cfg.DefaultWorkDir)
	svc.Sessions.UpdateRunResult(realKey.ID(), "3fe0c3-real", "", 0)
	svc.Sessions.GetOrCreate(syntheticKey, cfg.DefaultWorkDir)
	svc.Sessions.UpdateRunResult(syntheticKey.ID(), "5ddf80-synthetic", "", 0)
	svc.TopicAliases = NewTopicAliasStore()
	svc.TopicAliases.Bind("oc_chat", "omt_topic", syntheticKey.Thread)

	preference := svc.runtimePreference()
	preference.ConversationMode = config.ConversationModeTopic
	msg := Message{
		ID:       "om_followup",
		ChatID:   "oc_chat",
		ThreadID: "omt_topic",
		RootID:   "om_root",
		Sender:   "ou_user",
	}
	cmd := Command{Agent: agent.Claude, Text: "continue the existing topic"}

	if err := svc.runWithPreference(context.Background(), cmd, msg, "", preference); err != nil {
		t.Fatalf("runWithPreference: %v", err)
	}
	realSession, _ := svc.Sessions.Get(realKey)
	syntheticSession, _ := svc.Sessions.Get(syntheticKey)
	if len(realSession.Queue) != 1 {
		t.Fatalf("real topic queue length = %d, want 1", len(realSession.Queue))
	}
	if len(syntheticSession.Queue) != 0 {
		t.Fatalf("synthetic topic queue length = %d, want 0", len(syntheticSession.Queue))
	}
}

func TestServiceKeyForMessagePrefersExistingRealTopicSessionOverPollutedAlias(t *testing.T) {
	realKey := session.Key{Agent: agent.Claude, ChatID: "oc_chat", Thread: "omt_topic"}
	syntheticKey := session.Key{Agent: agent.Claude, ChatID: "oc_chat", Thread: SyntheticTopicThreadPrefix + "om_root"}

	sessions := session.NewManager()
	sessions.GetOrCreate(realKey, "")
	sessions.UpdateRunResult(realKey.ID(), "3fe0c3-real", "", 0)
	sessions.GetOrCreate(syntheticKey, "")
	sessions.UpdateRunResult(syntheticKey.ID(), "5ddf80-synthetic", "", 0)

	aliases := NewTopicAliasStore()
	aliases.Bind("oc_chat", "omt_topic", syntheticKey.Thread)
	svc := &Service{Sessions: sessions, TopicAliases: aliases}
	msg := Message{
		ID:       "om_followup",
		ChatID:   "oc_chat",
		ThreadID: "omt_topic",
		RootID:   "om_root",
	}

	if got := svc.keyForMessage(agent.Claude, msg, config.ConversationModeTopic); got != realKey {
		t.Fatalf("follow-up key = %+v, want existing real topic key %+v", got, realKey)
	}
}

func TestServiceKeyForMessageRecoversExistingSyntheticSessionFromRootID(t *testing.T) {
	syntheticKey := session.Key{Agent: agent.Claude, ChatID: "oc_chat", Thread: SyntheticTopicThreadPrefix + "om_root"}
	sessions := session.NewManager()
	sessions.GetOrCreate(syntheticKey, "")
	sessions.UpdateRunResult(syntheticKey.ID(), "synthetic-session", "", 0)

	svc := &Service{Sessions: sessions, TopicAliases: NewTopicAliasStore()}
	msg := Message{
		ID:       "om_followup",
		ChatID:   "oc_chat",
		ThreadID: "omt_topic",
		RootID:   "om_root",
	}

	if got := svc.keyForMessage(agent.Claude, msg, config.ConversationModeTopic); got != syntheticKey {
		t.Fatalf("follow-up key = %+v, want existing synthetic key %+v", got, syntheticKey)
	}
}

func TestServiceKeyForMessageDoesNotInventSyntheticSessionFromRootID(t *testing.T) {
	realKey := session.Key{Agent: agent.Claude, ChatID: "oc_chat", Thread: "omt_topic"}
	svc := &Service{Sessions: session.NewManager(), TopicAliases: NewTopicAliasStore()}
	msg := Message{
		ID:       "om_followup",
		ChatID:   "oc_chat",
		ThreadID: "omt_topic",
		RootID:   "om_root",
	}

	if got := svc.keyForMessage(agent.Claude, msg, config.ConversationModeTopic); got != realKey {
		t.Fatalf("follow-up key = %+v, want real topic key %+v", got, realKey)
	}
}
