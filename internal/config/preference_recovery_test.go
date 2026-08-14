package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// twoAgentCatalogue mirrors the shape of a real agents.json: home and bin labels
// exist under exactly one agent kind, so a label borrowed from the other kind is
// an illegal combination.
func twoAgentCatalogue() []AgentDef {
	return []AgentDef{
		{
			Kind:  "claude",
			Label: "Claude Code",
			Homes: []AgentHome{{Label: "workspace .claude-home", Path: ".claude-home"}},
			Bins:  []AgentBin{{Label: "cc5", Path: "bin/cc5"}},
		},
		{
			Kind:  "codex",
			Label: "Codex CLI",
			Homes: []AgentHome{{Label: "workspace .codex-home", Path: ".codex-home"}},
			Bins:  []AgentBin{{Label: "cx3", Path: "bin/cx3"}},
		},
	}
}

func writePreferenceFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A stored per-chat override that no longer merges legally must cost that chat
// its override and nothing more. Aborting the open (the previous behaviour) took
// the whole bridge down over one bad chat, and the only way to fix the value was
// through the bot that could no longer start.
func TestOpenPreferenceStoreDropsIllegalChatOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	// Global agent is codex while the chat override carries claude's home/bin and
	// no agent of its own — it inherits codex and becomes illegal.
	writePreferenceFile(t, path, `{
	  "schema_version": 1,
	  "revision": 46,
	  "override": {"model":"default","effort":"medium","reply_mode":"append","conversation_mode":"chat","group_message_mode":"mention_only","append_overflow_mode":"truncate","agent":"codex"},
	  "chat_overrides": {
	    "oc-bad": {"effort":"high","agent_home":"workspace .claude-home","agent_bin":"cc5"},
	    "oc-ok": {"effort":"high"}
	  }
	}`)
	store, err := OpenPreferenceStore(path, baseDefaults(), nil, twoAgentCatalogue()...)
	if err != nil {
		t.Fatalf("illegal stored chat override must not fail the open: %v", err)
	}
	if got := store.Get().Agent; got != "codex" {
		t.Fatalf("global agent = %q, want the stored codex to survive", got)
	}
	if _, ok := store.ChatOverride("oc-bad"); ok {
		t.Fatal("illegal chat override must not be loaded")
	}
	if got, want := store.GetForChat("oc-bad"), store.Get(); got != want {
		t.Fatalf("dropped chat = %#v, want the global preference %#v", got, want)
	}
	if got := store.GetForChat("oc-ok").Effort; got != "high" {
		t.Fatalf("legal sibling override effort = %q, want high", got)
	}
	recovery := store.Recovery()
	if recovery.Empty() {
		t.Fatal("recovery must report the dropped override")
	}
	if len(recovery.ChatOverrides) != 1 {
		t.Fatalf("recovery reported %d chat overrides, want 1", len(recovery.ChatOverrides))
	}
	if got := recovery.ChatOverrides[0]; got.ChatID != "oc-bad" || got.Action != ChatOverrideDropped {
		t.Fatalf("recovery entry = %+v, want oc-bad dropped", got)
	}
	// Loading must not rewrite the file: an agents.json that failed to load falls
	// back to a minimal catalogue, and persisting these drops would then destroy
	// per-chat configuration that is still perfectly valid.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "oc-bad") || !strings.Contains(string(data), `"revision": 46`) {
		t.Fatalf("open rewrote the store file: %s", data)
	}
}

// An illegal stored global override degrades to the process defaults instead of
// blocking startup.
func TestOpenPreferenceStoreDropsIllegalGlobalOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	writePreferenceFile(t, path, `{
	  "schema_version": 1,
	  "revision": 7,
	  "override": {"model":"default","effort":"medium","reply_mode":"append","conversation_mode":"chat","group_message_mode":"mention_only","append_overflow_mode":"truncate","agent":"gemini"}
	}`)
	store, err := OpenPreferenceStore(path, baseDefaults(), nil, twoAgentCatalogue()...)
	if err != nil {
		t.Fatalf("illegal stored global override must not fail the open: %v", err)
	}
	if got, want := store.Get(), normalizeRuntimePreference(baseDefaults()); got != want {
		t.Fatalf("global preference = %#v, want the defaults %#v", got, want)
	}
	recovery := store.Recovery()
	if !recovery.GlobalOverrideDropped {
		t.Fatal("recovery must report the dropped global override")
	}
	if !strings.Contains(recovery.GlobalOverrideReason, "gemini") {
		t.Fatalf("recovery reason = %q, want it to name the offending agent", recovery.GlobalOverrideReason)
	}
}

// Selecting a home or bin implies selecting the agent that owns it, so the agent
// kind is pinned into the stored override. Without this the override goes illegal
// the moment the global agent changes.
func TestSetChatPinsAgentForHomeAndBinSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil, twoAgentCatalogue()...)
	if err != nil {
		t.Fatal(err)
	}
	home := "workspace .claude-home"
	bin := "cc5"
	if err := store.SetChat("oc-a", ChatOverride{AgentHome: &home, AgentBin: &bin}); err != nil {
		t.Fatal(err)
	}
	override, ok := store.ChatOverride("oc-a")
	if !ok {
		t.Fatal("override was not stored")
	}
	if override.Agent == nil {
		t.Fatal("agent must be pinned alongside a home/bin selection")
	}
	if *override.Agent != "claude" {
		t.Fatalf("pinned agent = %q, want the inherited claude", *override.Agent)
	}
	// An override that touches no agent dimension keeps following the global agent.
	effort := "high"
	if err := store.SetChat("oc-b", ChatOverride{Effort: &effort}); err != nil {
		t.Fatal(err)
	}
	plain, _ := store.ChatOverride("oc-b")
	if plain.Agent != nil {
		t.Fatalf("agent must stay inherited for a non-agent override, got %q", *plain.Agent)
	}
}

// Switching the global agent is the write that used to plant the crash: the chat
// override kept claude's home/bin while silently inheriting the new codex agent,
// and only the next restart discovered it. The write now reconciles overrides, so
// reopening the store finds nothing to repair.
func TestGlobalAgentSwitchKeepsChatOverridesLoadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil, twoAgentCatalogue()...)
	if err != nil {
		t.Fatal(err)
	}
	home := "workspace .claude-home"
	bin := "cc5"
	if err := store.SetChat("oc-a", ChatOverride{AgentHome: &home, AgentBin: &bin}); err != nil {
		t.Fatal(err)
	}
	global := normalizeRuntimePreference(baseDefaults())
	global.Agent = "codex"
	if err := store.Set(global); err != nil {
		t.Fatal(err)
	}
	if got := store.GetForChat("oc-a").Agent; got != "claude" {
		t.Fatalf("chat agent = %q, want claude to be preserved across the global switch", got)
	}
	reopened, err := OpenPreferenceStore(path, baseDefaults(), nil, twoAgentCatalogue()...)
	if err != nil {
		t.Fatalf("reopen after a global agent switch failed: %v", err)
	}
	if recovery := reopened.Recovery(); !recovery.Empty() {
		t.Fatalf("reopen had to repair state that the write should have settled: %+v", recovery)
	}
	if got := reopened.GetForChat("oc-a").Agent; got != "claude" {
		t.Fatalf("reopened chat agent = %q, want claude", got)
	}
}

// Reset drops the global override, which can invalidate per-chat overrides the
// same way any other global write can; they are reconciled rather than left
// illegal on disk.
func TestResetReconcilesChatOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := OpenPreferenceStore(path, baseDefaults(), nil, twoAgentCatalogue()...)
	if err != nil {
		t.Fatal(err)
	}
	global := normalizeRuntimePreference(baseDefaults())
	global.Agent = "codex"
	if err := store.Set(global); err != nil {
		t.Fatal(err)
	}
	bin := "cx3"
	if err := store.SetChat("oc-a", ChatOverride{AgentBin: &bin}); err != nil {
		t.Fatal(err)
	}
	// Defaults use claude, so the codex bin only stays legal because the write
	// pinned codex onto the override.
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := store.GetForChat("oc-a").Agent; got != "codex" {
		t.Fatalf("chat agent = %q, want the pinned codex to survive Reset", got)
	}
	reopened, err := OpenPreferenceStore(path, baseDefaults(), nil, twoAgentCatalogue()...)
	if err != nil {
		t.Fatal(err)
	}
	if recovery := reopened.Recovery(); !recovery.Empty() {
		t.Fatalf("reopen after Reset had to repair state: %+v", recovery)
	}
}

// reconcileChatOverridesLocked escalates through its three repairs. These
// branches are defensive: the write-side agent pinning keeps legal state from
// reaching them, so they are exercised directly.
func TestReconcileChatOverridesEscalatesRepairs(t *testing.T) {
	previous := normalizeRuntimePreference(baseDefaults()) // claude
	next := normalizeRuntimePreference(baseDefaults())
	next.Agent = "codex"

	claudeHome := "workspace .claude-home"
	claudeBin := "cc5"
	codex := "codex"
	effort := "high"
	store := &PreferenceStore{
		allowedModels: builtinModels,
		agents:        twoAgentCatalogue(),
		chatOverrides: map[string]ChatOverride{
			// Legal under the new global: left untouched.
			"oc-keep": {Effort: &effort},
			// Inherits the new codex agent but holds claude's presets: pinning the
			// previously inherited claude keeps the selection intact.
			"oc-pin": {AgentHome: &claudeHome, AgentBin: &claudeBin},
			// Explicit codex agent with a claude bin cannot be pinned, but dropping
			// the agent dimension preserves the rest.
			"oc-clear": {Effort: &effort, Agent: &codex, AgentBin: &claudeBin},
			// Nothing survives dropping the agent dimension.
			"oc-drop": {Agent: &codex, AgentBin: &claudeBin},
		},
	}
	reconciled, adjustments := store.reconcileChatOverridesLocked(next, previous)
	actions := make(map[string]ChatOverrideAdjustmentAction, len(adjustments))
	for _, adjustment := range adjustments {
		actions[adjustment.ChatID] = adjustment.Action
	}
	if want := 3; len(adjustments) != want {
		t.Fatalf("adjustments = %+v, want %d entries", adjustments, want)
	}
	if actions["oc-pin"] != ChatOverridePinnedAgent {
		t.Fatalf("oc-pin action = %q, want pinned", actions["oc-pin"])
	}
	if actions["oc-clear"] != ChatOverrideClearedAgent {
		t.Fatalf("oc-clear action = %q, want cleared", actions["oc-clear"])
	}
	if actions["oc-drop"] != ChatOverrideDropped {
		t.Fatalf("oc-drop action = %q, want dropped", actions["oc-drop"])
	}
	if _, ok := actions["oc-keep"]; ok {
		t.Fatal("a legal override must not be adjusted")
	}

	if pinned := reconciled["oc-pin"]; pinned.Agent == nil || *pinned.Agent != "claude" {
		t.Fatalf("oc-pin = %+v, want agent pinned to claude", pinned)
	}
	cleared := reconciled["oc-clear"]
	if cleared.Agent != nil || cleared.AgentBin != nil || cleared.AgentHome != nil {
		t.Fatalf("oc-clear = %+v, want the agent dimension cleared", cleared)
	}
	if cleared.Effort == nil || *cleared.Effort != "high" {
		t.Fatalf("oc-clear = %+v, want the effort override preserved", cleared)
	}
	if _, ok := reconciled["oc-drop"]; ok {
		t.Fatal("oc-drop must be removed")
	}
	if _, ok := reconciled["oc-keep"]; !ok {
		t.Fatal("oc-keep must be retained")
	}
	// Every repaired override now merges legally under the new global.
	for chatID, override := range reconciled {
		if _, err := mergeChatOverride(next, override, store.allowedModels, store.agents); err != nil {
			t.Fatalf("reconciled override %q is still illegal: %v", chatID, err)
		}
	}
}
