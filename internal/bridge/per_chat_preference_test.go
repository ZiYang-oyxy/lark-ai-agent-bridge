package bridge

import (
	"path/filepath"
	"testing"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/config"
)

func perChatDefaults() config.RuntimePreference {
	return config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, GroupMessageMode: config.GroupMessageModeMentionOnly, Agent: config.DefaultAgentKind}
}

// runtimePreferenceFor routes: DM (non-group) always uses the global
// preference; a group message uses the per-chat effective preference.
func TestRuntimePreferenceForRoutesByChatScope(t *testing.T) {
	store, err := config.OpenPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"), perChatDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	topic := config.ConversationModeTopic
	if err := store.SetChat("oc-a", config.ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	s := &Service{Preferences: store}

	// Group A has a topic override.
	if got := s.runtimePreferenceFor(Message{IsGroup: true, ChatID: "oc-a"}); got.ConversationMode != config.ConversationModeTopic {
		t.Fatalf("group A conversation mode = %q, want topic", got.ConversationMode)
	}
	// Group B has no override -> inherits global chat mode.
	if got := s.runtimePreferenceFor(Message{IsGroup: true, ChatID: "oc-b"}); got.ConversationMode != config.ConversationModeChat {
		t.Fatalf("group B conversation mode = %q, want chat", got.ConversationMode)
	}
	// A DM never consults per-chat overrides, even if a chat id happens to
	// collide with an overridden group id.
	if got := s.runtimePreferenceFor(Message{IsGroup: false, ChatID: "oc-a"}); got.ConversationMode != config.ConversationModeChat {
		t.Fatalf("DM conversation mode = %q, want global chat", got.ConversationMode)
	}
}

// The per-chat mode must feed sessionKeyForMode so group A (topic) keys include
// the thread while group B (chat) keys do not.
func TestPerChatModeDrivesSessionKeyGranularity(t *testing.T) {
	store, err := config.OpenPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"), perChatDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	topic := config.ConversationModeTopic
	if err := store.SetChat("oc-a", config.ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	s := &Service{Preferences: store}

	msgA := Message{IsGroup: true, ChatID: "oc-a", ThreadID: "omt-1"}
	keyA := sessionKeyForMode(agent.Claude, msgA, s.runtimePreferenceFor(msgA).ConversationMode)
	if keyA.Thread != "omt-1" {
		t.Fatalf("group A key thread = %q, want omt-1", keyA.Thread)
	}

	msgB := Message{IsGroup: true, ChatID: "oc-b", ThreadID: "omt-2"}
	keyB := sessionKeyForMode(agent.Claude, msgB, s.runtimePreferenceFor(msgB).ConversationMode)
	if keyB.Thread != "" {
		t.Fatalf("group B key thread = %q, want empty (chat mode)", keyB.Thread)
	}
}
