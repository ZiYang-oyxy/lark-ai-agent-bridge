package bridge

import (
	"context"
	"testing"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
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
