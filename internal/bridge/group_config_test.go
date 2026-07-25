package bridge

import (
	"context"
	"testing"
	"time"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
)

func TestConfigSavePersistsGroupModeAndBotSwitch(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	_, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config", ActionID: "config.save", Actor: "owner",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
			"group_message_mode": "participated_topics", "respond_to_bots": "true",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.GroupMessageMode != config.GroupMessageModeParticipatedTopics || !got.RespondToBots {
		t.Fatalf("preference = %#v", got)
	}
}

func TestConfigSaveTogglesNotifyOnComplete(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	if store.Get().NotifyOnComplete {
		t.Fatalf("precondition: NotifyOnComplete should default false")
	}
	// Turn it on via the /config form.
	if _, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config", ActionID: "config.save", Actor: "owner",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
			"group_message_mode": "mention_only", "respond_to_bots": "false", "notify_on_complete": "true",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if !store.Get().NotifyOnComplete {
		t.Fatalf("save did not enable NotifyOnComplete: %#v", store.Get())
	}
	// A save that omits the field must preserve the current (enabled) value.
	if _, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config", ActionID: "config.save", Actor: "owner",
		FormValues: map[string]string{"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat"},
	}); err != nil {
		t.Fatal(err)
	}
	if !store.Get().NotifyOnComplete {
		t.Fatalf("omitting notify_on_complete erased the setting: %#v", store.Get())
	}
	// And back off again.
	if _, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config", ActionID: "config.save", Actor: "owner",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
			"group_message_mode": "mention_only", "respond_to_bots": "false", "notify_on_complete": "false",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if store.Get().NotifyOnComplete {
		t.Fatalf("save did not disable NotifyOnComplete: %#v", store.Get())
	}
}

// 三个 status bar 独立开关(show_meta_row_agent / _runtime / _developer)可在
// /config 全局开关,也可在 /local-config 按群独立覆盖;本群开启不影响全局与其它群。
func TestConfigSaveAndLocalOverrideStatusBarRows(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	if got := store.Get(); got.ShowMetaRowAgent || got.ShowMetaRowRuntime || got.ShowMetaRowDeveloper {
		t.Fatalf("precondition: all row toggles should default false, got %+v", got)
	}
	// 全局 /config 打开 agent + developer 两行,runtime 保持关闭。
	if _, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config", ActionID: "config.save", Actor: "owner",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
			"group_message_mode": "mention_only", "respond_to_bots": "false", "notify_on_complete": "false",
			"show_meta_row_agent": "true", "show_meta_row_runtime": "false", "show_meta_row_developer": "true",
		},
	}); err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if !got.ShowMetaRowAgent || got.ShowMetaRowRuntime || !got.ShowMetaRowDeveloper {
		t.Fatalf("global save did not apply independent toggles: %+v", got)
	}
	// 全局全部关回 false,再用 /local-config 只给 oc-a 开启 runtime 行。
	if _, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config", ActionID: "config.save", Actor: "owner",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
			"group_message_mode": "mention_only", "respond_to_bots": "false", "notify_on_complete": "false",
			"show_meta_row_agent": "false", "show_meta_row_runtime": "false", "show_meta_row_developer": "false",
		},
	}); err != nil {
		t.Fatal(err)
	}
	got = store.Get()
	if got.ShowMetaRowAgent || got.ShowMetaRowRuntime || got.ShowMetaRowDeveloper {
		t.Fatalf("global save did not clear all toggles: %+v", got)
	}
	if _, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "local", ActionID: "local_config.save", Actor: "owner", Value: "oc-a",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
			"group_message_mode": "mention_only", "respond_to_bots": "false", "notify_on_complete": "false",
			"show_meta_row_agent": "false", "show_meta_row_runtime": "true", "show_meta_row_developer": "false",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.GetForChat("oc-a"); !got.ShowMetaRowRuntime || got.ShowMetaRowAgent || got.ShowMetaRowDeveloper {
		t.Fatalf("local override did not enable only runtime for oc-a: %+v", got)
	}
	if got := store.GetForChat("oc-b"); got.ShowMetaRowAgent || got.ShowMetaRowRuntime || got.ShowMetaRowDeveloper {
		t.Fatalf("oc-b should still inherit global false: %+v", got)
	}
	if got := store.Get(); got.ShowMetaRowAgent || got.ShowMetaRowRuntime || got.ShowMetaRowDeveloper {
		t.Fatalf("local override leaked into global: %+v", got)
	}
}

func TestConfigSaveFromOldCardPreservesGroupSettings(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low", GroupMessageMode: config.GroupMessageModeAll, RespondToBots: true}, cfg.AllowedModels)
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	_, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config", ActionID: "config.save", Actor: "owner",
		FormValues: map[string]string{"model": "opus", "effort": "high", "reply_mode": "append", "conversation_mode": "chat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.GroupMessageMode != config.GroupMessageModeAll || !got.RespondToBots {
		t.Fatalf("old callback erased group settings: %#v", got)
	}
}

func TestConfigSaveRejectsInvalidBotSwitchWithoutChangingStore(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	before := store.Get()
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config", ActionID: "config.save", Actor: "owner",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
			"group_message_mode": "all_group_messages", "respond_to_bots": "sometimes",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.Event.Type != "error" || store.Get() != before {
		t.Fatalf("result=%#v preference=%#v", result, store.Get())
	}
}

func TestConfigSaveChecksScopeForNonMentionMode(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	inspector := &scopeInspectorStub{states: []feishu.ScopeState{feishu.ScopePresent}}
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences, svc.ScopeInspector, svc.AccessAppID = store, inspector, "cli_app"
	_, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config", ActionID: "config.save", Actor: "owner",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
			"group_message_mode": "all_group_messages", "respond_to_bots": "false",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for inspector.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if inspector.Calls() != 1 {
		t.Fatalf("scope inspection calls = %d", inspector.Calls())
	}
	_ = svc.Shutdown(context.Background())
}
