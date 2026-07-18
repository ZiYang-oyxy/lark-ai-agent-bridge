package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPreferenceStoreUsesDefaultsWhenSnapshotIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got != defaults {
		t.Fatalf("preference = %#v, want defaults %#v", got, defaults)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("opening missing store created a file: %v", err)
	}
}

func TestPreferenceStorePersistsVersionedOverrideAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}
	store, err := OpenPreferenceStore(path, defaults, []string{"claude-custom-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := RuntimePreference{Model: "claude-custom-1", Effort: "high", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}
	if err := store.Set(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("preference mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		SchemaVersion int                `json:"schema_version"`
		Revision      uint64             `json:"revision"`
		Override      *RuntimePreference `json:"override"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != PreferenceSchemaVersion || snapshot.Revision != 1 || snapshot.Override == nil || *snapshot.Override != want {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	reopened, err := OpenPreferenceStore(path, defaults, []string{"claude-custom-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(); got != want {
		t.Fatalf("reopened preference = %#v, want %#v", got, want)
	}
}

func TestPreferenceStoreRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":99,"revision":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPreferenceStore(path, RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend}, nil); err == nil {
		t.Fatal("OpenPreferenceStore() error = nil, want unknown schema failure")
	}
}

func TestRuntimePreferenceValidationUsesBuiltinsAndAllowedModels(t *testing.T) {
	for _, preference := range []RuntimePreference{
		{Model: "default", Effort: "default", ReplyMode: ReplyModeAppend},
		{Model: "sonnet", Effort: "low", ReplyMode: ReplyModeAppendCleanCard},
		{Model: "opus", Effort: "medium", ReplyMode: ReplyModeLatestCard},
		{Model: "haiku", Effort: "high", ReplyMode: ReplyModeAppend},
		{Model: "claude-custom-1", Effort: "high", ReplyMode: ReplyModeAppend},
	} {
		if err := ValidateRuntimePreference(preference, "claude-custom-1"); err != nil {
			t.Fatalf("ValidateRuntimePreference(%#v): %v", preference, err)
		}
	}
	for _, preference := range []RuntimePreference{
		{Model: "unknown", Effort: "low", ReplyMode: ReplyModeAppend},
		{Model: "sonnet", Effort: "extreme", ReplyMode: ReplyModeAppend},
		{Model: "two models", Effort: "low", ReplyMode: ReplyModeAppend},
	} {
		if err := ValidateRuntimePreference(preference, "claude-custom-1"); err == nil {
			t.Fatalf("ValidateRuntimePreference(%#v) error = nil", preference)
		}
	}
}

func TestRuntimePreferenceValidatesAndPersistsReplyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []ReplyMode{ReplyModeAppend, ReplyModeAppendCleanCard, ReplyModeLatestCard} {
		want := RuntimePreference{Model: "opus", Effort: "high", ReplyMode: mode, ConversationMode: ConversationModeChat}
		if err := store.Set(want); err != nil {
			t.Fatalf("Set(%q): %v", mode, err)
		}
		reopened, err := OpenPreferenceStore(path, defaults, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := reopened.Get(); got != want {
			t.Fatalf("reply preference = %#v, want %#v", got, want)
		}
	}
	before := store.Get()
	if err := store.Set(RuntimePreference{Model: "sonnet", Effort: "low", ReplyMode: "replace-everything"}); err == nil {
		t.Fatal("invalid reply mode was accepted")
	}
	if got := store.Get(); got != before {
		t.Fatalf("invalid reply mode changed preference: %#v", got)
	}
}

func TestPreferenceStoreResetPersistsRemovalAndRestoresDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(RuntimePreference{Model: "opus", Effort: "high", ReplyMode: ReplyModeLatestCard}); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got != defaults {
		t.Fatalf("preference after reset = %#v, want %#v", got, defaults)
	}
	reopened, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(); got != defaults {
		t.Fatalf("reopened preference after reset = %#v, want %#v", got, defaults)
	}
}

func TestPreferenceStoreFailedReplacementDoesNotPublishCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(RuntimePreference{Model: "sonnet", Effort: "medium", ReplyMode: ReplyModeAppend}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(RuntimePreference{Model: "opus", Effort: "high", ReplyMode: ReplyModeLatestCard}); err == nil {
		t.Fatal("Set() error = nil, want replacement failure")
	}
	want := RuntimePreference{Model: "sonnet", Effort: "medium", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}
	if got := store.Get(); got != want {
		t.Fatalf("preference after failed write = %#v, want unchanged %#v", got, want)
	}
}

func TestPreferenceStoreDefaultsLegacySnapshotToChatConversationMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"revision":2,"override":{"model":"opus","effort":"high","reply_mode":"append"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPreferenceStore(path, RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got.ConversationMode != ConversationModeChat {
		t.Fatalf("legacy conversation mode = %q, want %q", got.ConversationMode, ConversationModeChat)
	}
}

func TestRuntimePreferenceValidatesAndPersistsConversationMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []ConversationMode{ConversationModeChat, ConversationModeTopic} {
		want := RuntimePreference{Model: "opus", Effort: "high", ReplyMode: ReplyModeLatestCard, ConversationMode: mode}
		if err := store.Set(want); err != nil {
			t.Fatalf("Set(%q): %v", mode, err)
		}
		reopened, err := OpenPreferenceStore(path, defaults, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := reopened.Get(); got != want {
			t.Fatalf("conversation preference = %#v, want %#v", got, want)
		}
	}
	before := store.Get()
	if err := store.Set(RuntimePreference{Model: "sonnet", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: "invalid"}); err == nil {
		t.Fatal("invalid conversation mode was accepted")
	}
	if got := store.Get(); got != before {
		t.Fatalf("invalid conversation mode changed preference: %#v", got)
	}
}
