package bridge

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/config"
)

// In a group with an override, /status must name which fields are local
// overrides so the effective behavior is explainable.
func TestStatusTextShowsLocalOverrides(t *testing.T) {
	svc, store := localConfigService(t)
	topic := config.ConversationModeTopic
	if err := store.SetChat("oc-a", config.ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	msg := Message{ID: "m1", IsGroup: true, ChatID: "oc-a"}
	text := svc.statusTextWithPreference(agent.Claude, msg, store.GetForChat("oc-a"))
	if !strings.Contains(text, "local_overrides=") || !strings.Contains(text, "conversation_mode") {
		t.Fatalf("group status should list local overrides, got:\n%s", text)
	}
}

// A group without an override should not claim any local override.
func TestStatusTextNoLocalOverridesWhenUnset(t *testing.T) {
	svc, store := localConfigService(t)
	msg := Message{ID: "m1", IsGroup: true, ChatID: "oc-b"}
	text := svc.statusTextWithPreference(agent.Claude, msg, store.GetForChat("oc-b"))
	if strings.Contains(text, "local_overrides=") {
		t.Fatalf("group without override must not report local_overrides, got:\n%s", text)
	}
}

// A DM never reports local overrides.
func TestStatusTextDirectMessageHasNoLocalOverrides(t *testing.T) {
	svc, store := localConfigService(t)
	msg := Message{ID: "m1", IsGroup: false, ChatID: "dm"}
	text := svc.statusTextWithPreference(agent.Claude, msg, store.Get())
	if strings.Contains(text, "local_overrides=") {
		t.Fatalf("DM status must not report local_overrides, got:\n%s", text)
	}
}
