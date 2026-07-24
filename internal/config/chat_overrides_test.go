package config

import (
	"os"
	"path/filepath"
	"testing"
)

func baseDefaults() RuntimePreference {
	return RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, GroupMessageMode: GroupMessageModeMentionOnly, Agent: DefaultAgentKind}
}

// GetForChat with no override for the chat must equal the global effective
// preference exactly.
func TestGetForChatFallsBackToGlobal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := store.GetForChat("oc-a"), store.Get(); got != want {
		t.Fatalf("GetForChat unset = %#v, want global %#v", got, want)
	}
	if err := store.Set(RuntimePreference{Model: "opus", Effort: "high", ReplyMode: ReplyModeLatestCard, ConversationMode: ConversationModeChat}); err != nil {
		t.Fatal(err)
	}
	if got, want := store.GetForChat("oc-a"), store.Get(); got != want {
		t.Fatalf("GetForChat after global Set = %#v, want global %#v", got, want)
	}
}

// A single-field chat override changes only that field; all other fields still
// equal the global effective preference.
func TestSetChatOverridesSingleField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	topic := ConversationModeTopic
	if err := store.SetChat("oc-a", ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	got := store.GetForChat("oc-a")
	if got.ConversationMode != ConversationModeTopic {
		t.Fatalf("chat conversation mode = %q, want topic", got.ConversationMode)
	}
	// Every other field still matches global.
	global := store.Get()
	want := global
	want.ConversationMode = ConversationModeTopic
	if got != want {
		t.Fatalf("chat override leaked into other fields:\n got  %#v\n want %#v", got, want)
	}
	// Unrelated chats are unaffected.
	if store.GetForChat("oc-b") != global {
		t.Fatalf("chat oc-b should still equal global")
	}
}

// ShowMetaRows 是布尔覆盖:本群可独立开启,不影响全局与其它群。
func TestSetChatOverrideShowMetaRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if store.Get().ShowMetaRows {
		t.Fatal("global ShowMetaRows should default to false")
	}
	on := true
	if err := store.SetChat("oc-a", ChatOverride{ShowMetaRows: &on}); err != nil {
		t.Fatal(err)
	}
	if !store.GetForChat("oc-a").ShowMetaRows {
		t.Fatal("chat oc-a ShowMetaRows override not applied")
	}
	if store.GetForChat("oc-b").ShowMetaRows {
		t.Fatal("chat oc-b should still inherit global false")
	}
	if store.Get().ShowMetaRows {
		t.Fatal("global ShowMetaRows must stay false")
	}
}

// Per-field inheritance is live, not a snapshot: changing the global default
// for a field not overridden by the chat must flow through to GetForChat.
func TestGetForChatInheritsLiveGlobalChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	topic := ConversationModeTopic
	if err := store.SetChat("oc-a", ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	// Chat only overrides conversation mode; model stays inherited.
	if err := store.Set(RuntimePreference{Model: "opus", Effort: "high", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}); err != nil {
		t.Fatal(err)
	}
	got := store.GetForChat("oc-a")
	if got.Model != "opus" {
		t.Fatalf("chat model = %q, want inherited opus", got.Model)
	}
	if got.ConversationMode != ConversationModeTopic {
		t.Fatalf("chat conversation mode = %q, want overridden topic", got.ConversationMode)
	}
}

// SetChat that produces an illegal merged preference is rejected and nothing is
// persisted for that chat.
func TestSetChatRejectsIllegalMergedResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	bad := "not-an-allowed-model"
	if err := store.SetChat("oc-a", ChatOverride{Model: &bad}); err == nil {
		t.Fatal("SetChat with illegal model was accepted")
	}
	if _, ok := store.ChatOverride("oc-a"); ok {
		t.Fatal("rejected SetChat must not persist an override")
	}
}

// ResetChat removes the override and GetForChat returns to global; ChatOverride
// reports the chat as unset.
func TestResetChatRemovesOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	topic := ConversationModeTopic
	if err := store.SetChat("oc-a", ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.ChatOverride("oc-a"); !ok {
		t.Fatal("ChatOverride should report the chat as set")
	}
	if err := store.ResetChat("oc-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.ChatOverride("oc-a"); ok {
		t.Fatal("ChatOverride should report the chat as unset after reset")
	}
	if store.GetForChat("oc-a") != store.Get() {
		t.Fatal("GetForChat after reset should equal global")
	}
}

// Overrides persist across reopen with 0600 permission, and multiple chats stay
// independent.
func TestChatOverridesPersistAndIsolate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	topic := ConversationModeTopic
	latest := ReplyModeLatestCard
	if err := store.SetChat("oc-a", ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChat("oc-b", ChatOverride{ReplyMode: &latest}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("preference mode = %v, want 0600", info.Mode().Perm())
	}
	reopened, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.GetForChat("oc-a"); got.ConversationMode != ConversationModeTopic || got.ReplyMode != ReplyModeAppend {
		t.Fatalf("reopened oc-a = %#v", got)
	}
	if got := reopened.GetForChat("oc-b"); got.ReplyMode != ReplyModeLatestCard || got.ConversationMode != ConversationModeChat {
		t.Fatalf("reopened oc-b = %#v", got)
	}
}

// A legacy snapshot without chat_overrides loads cleanly as an empty override
// table.
func TestChatOverridesLegacySnapshotLoadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	legacy := `{"schema_version":1,"revision":3,"override":{"model":"sonnet","effort":"high","reply_mode":"append","conversation_mode":"chat"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatalf("open legacy snapshot: %v", err)
	}
	if _, ok := store.ChatOverride("oc-a"); ok {
		t.Fatal("legacy snapshot must yield no chat overrides")
	}
	if got := store.GetForChat("oc-a"); got != store.Get() {
		t.Fatal("legacy GetForChat should equal global")
	}
}

// Global Reset must not touch chat overrides.
func TestGlobalResetKeepsChatOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	topic := ConversationModeTopic
	if err := store.SetChat("oc-a", ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := store.GetForChat("oc-a"); got.ConversationMode != ConversationModeTopic {
		t.Fatalf("global reset dropped chat override: %#v", got)
	}
}
