package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPreferenceStoreUsesDefaultsWhenSnapshotIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, TopicSeedMode: TopicSeedModeQuote, GroupMessageMode: GroupMessageModeMentionOnly, AppendOverflowMode: AppendOverflowModeTruncate, Agent: DefaultAgentKind}
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
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, TopicSeedMode: TopicSeedModeQuote, GroupMessageMode: GroupMessageModeMentionOnly, AppendOverflowMode: AppendOverflowModeTruncate, Agent: DefaultAgentKind}
	store, err := OpenPreferenceStore(path, defaults, []string{"claude-custom-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := RuntimePreference{Model: "claude-custom-1", Effort: "high", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, TopicSeedMode: TopicSeedModeQuote, GroupMessageMode: GroupMessageModeMentionOnly, AppendOverflowMode: AppendOverflowModeTruncate, Agent: DefaultAgentKind}
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
		{Model: "sonnet", Effort: "low", ReplyMode: ReplyModeAppend, Agent: "gemini"},
	} {
		if err := ValidateRuntimePreference(preference, "claude-custom-1"); err == nil {
			t.Fatalf("ValidateRuntimePreference(%#v) error = nil", preference)
		}
	}
}

func TestPreferenceStoreValidatesAgentSelectionAgainstCatalogue(t *testing.T) {
	agents := DefaultAgentsConfig().Agents
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, Agent: DefaultAgentKind}
	store, err := OpenPreferenceStore(path, defaults, nil, agents...)
	if err != nil {
		t.Fatal(err)
	}
	// Default (empty) agent selections are accepted.
	if err := store.Set(RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, Agent: DefaultAgentKind}); err != nil {
		t.Fatalf("set default agent selection: %v", err)
	}
	// An unknown home label is rejected.
	if err := store.Set(RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, AgentHome: "nope"}); err == nil {
		t.Fatal("expected unknown agent home to be rejected")
	}
}

func TestPreferenceStoreBackwardCompatibleWithLegacySnapshot(t *testing.T) {
	// A snapshot written before agent fields existed must still load and
	// normalize to the default agent selection.
	path := filepath.Join(t.TempDir(), "preferences.json")
	legacy := `{"schema_version":1,"revision":3,"override":{"model":"sonnet","effort":"high","reply_mode":"append","conversation_mode":"chat"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, Agent: DefaultAgentKind}
	store, err := OpenPreferenceStore(path, defaults, nil, DefaultAgentsConfig().Agents...)
	if err != nil {
		t.Fatalf("open legacy snapshot: %v", err)
	}
	got := store.Get()
	if got.Agent != DefaultAgentKind || got.AgentHome != "" || got.AgentBin != "" {
		t.Fatalf("legacy snapshot agent fields = %q/%q/%q", got.Agent, got.AgentHome, got.AgentBin)
	}
	if got.Model != "sonnet" || got.Effort != "high" {
		t.Fatalf("legacy snapshot lost fields: %#v", got)
	}
	if got.AppendOverflowMode != AppendOverflowModeTruncate {
		t.Fatalf("legacy append overflow mode = %q, want %q", got.AppendOverflowMode, AppendOverflowModeTruncate)
	}
}

func TestRuntimePreferenceValidatesAndPersistsAppendOverflowMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", AppendOverflowMode: AppendOverflowModeTruncate}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []AppendOverflowMode{AppendOverflowModeTruncate, AppendOverflowModeContinueCard} {
		want := RuntimePreference{Model: "opus", Effort: "high", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, TopicSeedMode: TopicSeedModeQuote, GroupMessageMode: GroupMessageModeMentionOnly, AppendOverflowMode: mode, Agent: DefaultAgentKind}
		if err := store.Set(want); err != nil {
			t.Fatalf("Set(%q): %v", mode, err)
		}
		reopened, err := OpenPreferenceStore(path, defaults, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := reopened.Get(); got != want {
			t.Fatalf("append overflow preference = %#v, want %#v", got, want)
		}
	}
	if err := store.Set(RuntimePreference{Model: "sonnet", Effort: "low", AppendOverflowMode: "new-card"}); err == nil {
		t.Fatal("invalid append overflow mode was accepted")
	}
}

func TestRuntimePreferenceValidatesAndPersistsReplyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, Agent: DefaultAgentKind}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []ReplyMode{ReplyModeAppend, ReplyModeAppendCleanCard, ReplyModeLatestCard} {
		want := RuntimePreference{Model: "opus", Effort: "high", ReplyMode: mode, ConversationMode: ConversationModeChat, TopicSeedMode: TopicSeedModeQuote, GroupMessageMode: GroupMessageModeMentionOnly, AppendOverflowMode: AppendOverflowModeTruncate, Agent: DefaultAgentKind}
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

func TestReplyModeUsesProductNamesWhileKeepingStorageKeys(t *testing.T) {
	for _, want := range []struct {
		mode       ReplyMode
		storageKey string
		label      string
	}{
		{ReplyModeWorker, "append", "Worker（不展开过程，只显示结果）"},
		{ReplyModeCoder, "append-clean-card", "Coder（记录全部推理/工具调用过程）"},
		{ReplyModeSingleton, "latest-card", "Singleton（维持单个卡片更新，搭配 pin 使用）"},
	} {
		if string(want.mode) != want.storageKey || want.mode.Label() != want.label {
			t.Fatalf("mode=%q label=%q, want storage=%q label=%q", want.mode, want.mode.Label(), want.storageKey, want.label)
		}
	}
}

func TestPreferenceStoreResetPersistsRemovalAndRestoresDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, TopicSeedMode: TopicSeedModeQuote, GroupMessageMode: GroupMessageModeMentionOnly, AppendOverflowMode: AppendOverflowModeTruncate, Agent: DefaultAgentKind}
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
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, Agent: DefaultAgentKind}
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
	want := RuntimePreference{Model: "sonnet", Effort: "medium", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, TopicSeedMode: TopicSeedModeQuote, GroupMessageMode: GroupMessageModeMentionOnly, AppendOverflowMode: AppendOverflowModeTruncate, Agent: DefaultAgentKind}
	if got := store.Get(); got != want {
		t.Fatalf("preference after failed write = %#v, want unchanged %#v", got, want)
	}
}

func TestPreferenceStoreDefaultsLegacySnapshotToChatConversationMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"revision":2,"override":{"model":"opus","effort":"high","reply_mode":"append"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPreferenceStore(path, RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, Agent: DefaultAgentKind}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got.ConversationMode != ConversationModeChat {
		t.Fatalf("legacy conversation mode = %q, want %q", got.ConversationMode, ConversationModeChat)
	}
}

func TestRuntimePreferenceValidatesAndPersistsConversationMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", ReplyMode: ReplyModeAppend, ConversationMode: ConversationModeChat, Agent: DefaultAgentKind}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []ConversationMode{ConversationModeChat, ConversationModeTopic} {
		want := RuntimePreference{Model: "opus", Effort: "high", ReplyMode: ReplyModeLatestCard, ConversationMode: mode, TopicSeedMode: TopicSeedModeQuote, GroupMessageMode: GroupMessageModeMentionOnly, AppendOverflowMode: AppendOverflowModeTruncate, Agent: DefaultAgentKind}
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

func TestPreferenceStoreDefaultsLegacySnapshotToMentionOnlyAndIgnoresBots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	legacy := `{"schema_version":1,"revision":3,"override":{"model":"sonnet","effort":"high","reply_mode":"append","conversation_mode":"chat"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPreferenceStore(path, RuntimePreference{Model: "default", Effort: "low"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.GroupMessageMode != GroupMessageModeMentionOnly || got.RespondToBots {
		t.Fatalf("legacy intake preference = mode %q respond_to_bots=%t", got.GroupMessageMode, got.RespondToBots)
	}
}

func TestPreferenceStorePersistsGroupMessageModesAndBotSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", GroupMessageMode: GroupMessageModeMentionOnly}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []GroupMessageMode{GroupMessageModeMentionOnly, GroupMessageModeParticipatedTopics, GroupMessageModeAll} {
		want := RuntimePreference{Model: "opus", Effort: "high", GroupMessageMode: mode, RespondToBots: true}
		if err := store.Set(want); err != nil {
			t.Fatalf("Set(%q): %v", mode, err)
		}
		reopened, err := OpenPreferenceStore(path, defaults, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := reopened.Get(); got.GroupMessageMode != mode || !got.RespondToBots {
			t.Fatalf("reopened preference = %#v", got)
		}
	}
	before := store.Get()
	if err := store.Set(RuntimePreference{Model: "opus", Effort: "high", GroupMessageMode: "everything"}); err == nil {
		t.Fatal("invalid group message mode was accepted")
	}
	if got := store.Get(); got != before {
		t.Fatalf("invalid group message mode changed preference: %#v", got)
	}
}

func TestPreferenceStoreNotifyOnCompleteDefaultsFalseAndPersistsToggle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", GroupMessageMode: GroupMessageModeMentionOnly}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Absent from defaults / legacy snapshots ⇒ false, no panic.
	if store.Get().NotifyOnComplete {
		t.Fatalf("NotifyOnComplete default = true, want false")
	}
	// Saving the toggle on flips it and survives a reopen.
	want := RuntimePreference{Model: "opus", Effort: "high", GroupMessageMode: GroupMessageModeMentionOnly, NotifyOnComplete: true}
	if err := store.Set(want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	reopened, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Get().NotifyOnComplete {
		t.Fatalf("reopened NotifyOnComplete = false, want true")
	}
	// Reset falls back to the (false) default.
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if store.Get().NotifyOnComplete {
		t.Fatalf("after reset NotifyOnComplete = true, want false")
	}
}

func TestPreferenceStoreResetRestoresGroupMessageEnvironmentDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	defaults := RuntimePreference{Model: "default", Effort: "low", GroupMessageMode: GroupMessageModeAll, RespondToBots: true}
	store, err := OpenPreferenceStore(path, defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(RuntimePreference{Model: "opus", Effort: "high", GroupMessageMode: GroupMessageModeMentionOnly}); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got.GroupMessageMode != GroupMessageModeAll || !got.RespondToBots {
		t.Fatalf("reset preference = %#v", got)
	}
}
