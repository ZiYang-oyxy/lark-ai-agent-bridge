package bridge

import (
	"context"
	"strings"
	"testing"

	"lark-agent-bridge/internal/config"
)

func TestConfigSaveRejectsStaleCardRevision(t *testing.T) {
	svc, store := localConfigService(t)
	_, revision := store.Snapshot()
	if _, err := store.Update(func(current config.RuntimePreference) (config.RuntimePreference, error) {
		current.Effort = "high"
		return current, nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config-stale", ActionID: "config.save", Actor: "ou_admin", OpenMessageID: "om_config",
		PreferenceRevision: revision, HasPreferenceRevision: true,
		FormValues: map[string]string{"effort": "low", "reply_mode": "latest-card"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.Effort != "high" || got.ReplyMode != config.ReplyModeAppend {
		t.Fatalf("stale card changed preference: %#v", got)
	}
	if result.Event == nil || !strings.Contains(segmentText(*result.Event), "重新打开") {
		t.Fatalf("conflict result = %#v", result.Event)
	}
}

func TestLocalConfigSaveRejectsStaleCardRevision(t *testing.T) {
	svc, store := localConfigService(t)
	_, _, revision := store.SnapshotForChat("oc-a")
	if _, err := store.Update(func(current config.RuntimePreference) (config.RuntimePreference, error) {
		current.Effort = "high"
		return current, nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "local-stale", ActionID: "local_config.save", Actor: "ou_admin", OpenMessageID: "om_local", Value: "oc-a",
		PreferenceRevision: revision, HasPreferenceRevision: true,
		FormValues: map[string]string{"effort": "low", "reply_mode": "latest-card"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.ChatOverride("oc-a"); ok {
		t.Fatal("stale local card created an override")
	}
	if result.Event == nil || !strings.Contains(segmentText(*result.Event), "重新打开") {
		t.Fatalf("conflict result = %#v", result.Event)
	}
}

func TestConfigFormsCarryPreferenceRevision(t *testing.T) {
	svc, store := localConfigService(t)
	_, globalRevision := store.Snapshot()
	global := svc.configForm(store.Get())
	if global.PreferenceRevision != globalRevision {
		t.Fatalf("global revision = %d, want %d", global.PreferenceRevision, globalRevision)
	}
	local := svc.localConfigForm(store.GetForChat("oc-a"), "oc-a")
	if local.PreferenceRevision != globalRevision {
		t.Fatalf("local revision = %d, want %d", local.PreferenceRevision, globalRevision)
	}
}
