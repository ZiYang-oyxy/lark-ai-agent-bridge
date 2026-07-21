package bridge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

func TestActionRequestFromCardCallback(t *testing.T) {
	req, err := ActionRequestFromCardCallback([]byte(`{
		"operator": {"open_id": "user-1"},
		"action": {
			"value": {
				"session": "claude:chat",
				"action_id": "stop",
				"value": ""
			}
		}
	}`))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if req.SessionID != "claude:chat" || req.ActionID != "stop" || req.Actor != "user-1" {
		t.Fatalf("request = %#v", req)
	}
}

func TestActionRequestFromCardCallbackPreservesFormValues(t *testing.T) {
	req, err := ActionRequestFromCardCallback([]byte(`{
		"operator": {"open_id": "user-1"},
		"action": {
			"value": {"session": "claude:chat", "action_id": "config.save"},
			"form_value": {"model": "opus", "effort": "high", "ignored": 42}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.FormValues) != 2 || req.FormValues["model"] != "opus" || req.FormValues["effort"] != "high" {
		t.Fatalf("form values = %#v", req.FormValues)
	}
}

func TestActionRequestFromCardCallbackRejectsOversizedFormValues(t *testing.T) {
	fields := make([]string, 17)
	for i := range fields {
		fields[i] = fmt.Sprintf("%q:%q", fmt.Sprintf("field_%d", i), "value")
	}
	tooMany := fmt.Sprintf(`{"action":{"value":{"session":"s","action_id":"config.save"},"form_value":{%s}}}`, strings.Join(fields, ","))
	for _, payload := range []string{
		tooMany,
		fmt.Sprintf(`{"action":{"value":{"session":"s","action_id":"config.save"},"form_value":{"model":%q}}}`, strings.Repeat("x", 129)),
	} {
		if _, err := ActionRequestFromCardCallback([]byte(payload)); err == nil {
			t.Fatalf("ActionRequestFromCardCallback() error = nil for %s", payload)
		}
	}
}

func TestActionRequestFromNestedEventPayload(t *testing.T) {
	req, err := ActionRequestFromCardCallback([]byte(`{
		"event": {
			"operator": {"open_id": "user-1"},
			"context": {"open_message_id": "om_config"},
			"action": {
				"value": {
					"session": "claude:chat",
				"action_id": "create_workdir",
				"value": "/tmp/work"
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if req.ActionID != "create_workdir" || req.Value != "/tmp/work" || req.OpenMessageID != "om_config" {
		t.Fatalf("request = %#v", req)
	}
}

func TestActionRequestFromStringifiedValue(t *testing.T) {
	req, err := ActionRequestFromCardCallback([]byte(`{
		"operator": {"open_id": "user-1"},
		"action": {
			"value": "{\"session\":\"claude:chat\",\"action_id\":\"cancel_workdir\",\"value\":\"/tmp/work\"}"
		}
	}`))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if req.SessionID != "claude:chat" || req.ActionID != "cancel_workdir" || req.Value != "/tmp/work" {
		t.Fatalf("request = %#v", req)
	}
}

func TestActionRequestFallsBackToActionIDOnAction(t *testing.T) {
	req, err := ActionRequestFromCardCallback([]byte(`{
		"operator": {"open_id": "user-1"},
		"action": {
			"action_id": "stop",
			"value": {"session": "claude:chat"}
		}
	}`))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if req.ActionID != "stop" {
		t.Fatalf("action id = %q, want stop", req.ActionID)
	}
}

// lastEvent returns the most recently rendered event, failing when the renderer
// produced nothing.
func lastEvent(t *testing.T, renderer *card.FakeRenderer) card.Event {
	t.Helper()
	events := renderer.Events()
	if len(events) == 0 {
		t.Fatalf("no events rendered")
	}
	return events[len(events)-1]
}

func TestHelpRefreshCallbackRerendersHelpCard(t *testing.T) {
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "claude:chat", ActionID: "help.refresh", Actor: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := lastEvent(t, renderer)
	if event.Type != "help" {
		t.Fatalf("event type = %q, want help", event.Type)
	}
	if event.HelpCard == nil {
		t.Fatalf("help.refresh must carry a HelpCard: %#v", event)
	}
	if result.Event == nil || result.Event.Type != "help" {
		t.Fatalf("result = %#v, want help event", result)
	}
}

func TestHelpOpenConfigCallbackRendersConfigForm(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "claude:chat", ActionID: "help.open_config", Actor: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := lastEvent(t, renderer)
	if event.Type != "config" {
		t.Fatalf("event type = %q, want config", event.Type)
	}
	if event.ConfigForm == nil {
		t.Fatalf("help.open_config must carry a ConfigForm: %#v", event)
	}
	if result.Event == nil || result.Event.Type != "config" {
		t.Fatalf("result = %#v, want config event", result)
	}
}

func TestHelpCommandCarriesOnlyGroupChatContext(t *testing.T) {
	for _, tt := range []struct {
		name   string
		group  bool
		chatID string
		want   string
	}{
		{name: "group", group: true, chatID: "oc-a", want: "oc-a"},
		{name: "direct", group: false, chatID: "dm-a", want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			renderer := card.NewFakeRenderer()
			svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
			err := svc.HandleMessage(context.Background(), Message{
				ID: "help-" + tt.name, ChatID: tt.chatID, Sender: "user-1", Text: "/help",
				IsGroup: tt.group, Mentioned: tt.group,
			})
			if err != nil {
				t.Fatal(err)
			}
			event := lastEvent(t, renderer)
			if event.HelpCard == nil || event.HelpCard.ChatID != tt.want {
				t.Fatalf("HelpCard = %#v, want ChatID %q", event.HelpCard, tt.want)
			}
		})
	}
}

func TestHelpOpenLocalConfigCallbackRendersReadOnlyOverview(t *testing.T) {
	svc, store := localConfigService(t)
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "help-card", ActionID: "help.open_local_config", Value: "oc-a", Actor: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.Event.Type != "local_config_overview" {
		t.Fatalf("result = %#v, want local_config_overview", result)
	}
	if result.Event.LocalConfigOverview == nil || result.Event.LocalConfigOverview.ChatID != "oc-a" {
		t.Fatalf("overview = %#v, want chat oc-a", result.Event.LocalConfigOverview)
	}
	if _, ok := store.ChatOverride("oc-a"); ok {
		t.Fatal("opening local config from help must not write an override")
	}
}

func TestHelpOpenLocalConfigCallbackRequiresChatID(t *testing.T) {
	svc, store := localConfigService(t)
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "help-card", ActionID: "help.open_local_config", Actor: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.Event.Type != "error" {
		t.Fatalf("result = %#v, want error event", result)
	}
	if _, ok := store.ChatOverride(""); ok {
		t.Fatal("missing chat id must not write an override")
	}
}

func TestHelpStatusCallbackRendersStatusNotUnknown(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	_, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "claude:chat", ActionID: "help.status", Actor: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := lastEvent(t, renderer)
	if event.Type == "error" {
		t.Fatalf("help.status must not render an error card: %#v", event)
	}
	text := segmentText(event)
	if strings.Contains(text, "unknown action") {
		t.Fatalf("help.status fell through to default: %q", text)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatalf("help.status produced no status text: %#v", event)
	}
}

func TestLocalConfigEditCallback(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "claude:chat", ActionID: "local_config.edit", Value: "oc-a", Actor: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := lastEvent(t, renderer)
	if event.Type != "local_config" {
		t.Fatalf("event type = %q, want local_config", event.Type)
	}
	if event.ConfigForm == nil {
		t.Fatalf("local_config.edit must carry a ConfigForm: %#v", event)
	}
	if event.ConfigForm.ChatID != "oc-a" {
		t.Fatalf("ConfigForm.ChatID = %q, want oc-a", event.ConfigForm.ChatID)
	}
	if result.Event == nil || result.Event.Type != "local_config" {
		t.Fatalf("result = %#v, want local_config event", result)
	}
}

func TestLocalConfigResetCallback(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend}, cfg.AllowedModels)
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, newFakeRunner(), recorder)
	svc.Preferences = store

	latest := config.ReplyModeLatestCard
	if err := store.SetChat("oc-a", config.ChatOverride{ReplyMode: &latest}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.ChatOverride("oc-a"); !ok {
		t.Fatalf("precondition: chat override not set")
	}

	_, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "claude:chat", ActionID: "local_config.reset", Value: "oc-a", Actor: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.ChatOverride("oc-a"); ok {
		t.Fatalf("local_config.reset must clear the chat override")
	}
	if got := store.GetForChat("oc-a").ReplyMode; got != config.ReplyModeAppend {
		t.Fatalf("GetForChat reply mode = %q, want inherited append", got)
	}
	event := lastEvent(t, renderer)
	if event.Type == "error" {
		t.Fatalf("local_config.reset must not render an error card: %#v", event)
	}
	text := segmentText(event)
	if !strings.Contains(text, "清空") || !strings.Contains(text, "继承") {
		t.Fatalf("reset result text missing 清空/继承: %q", text)
	}
	if !auditContainsAction(recorder.Events(), "local_config_reset") {
		t.Fatalf("audit = %#v", recorder.Events())
	}
}

func TestLocalConfigResetRequiresAdmin(t *testing.T) {
	cfg := testConfig(t)
	accessStore, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, newFakeRunner(), recorder)
	svc.Preferences = store
	svc.Access, svc.AccessControls, svc.AccessAppID = accessStore, controls, "cli_app"

	latest := config.ReplyModeLatestCard
	if err := store.SetChat("oc-a", config.ChatOverride{ReplyMode: &latest}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "claude:chat", ActionID: "local_config.reset", Value: "oc-a", Actor: "ou_stranger",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.ChatOverride("oc-a"); !ok {
		t.Fatalf("denied local_config.reset must not clear the chat override")
	}
	if !auditContainsAction(recorder.Events(), "admin_denied") {
		t.Fatalf("audit = %#v", recorder.Events())
	}
}

func TestLocalConfigSaveCallbackScopedConfirmation(t *testing.T) {
	cfg := testConfig(t)
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend}, cfg.AllowedModels)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store

	_, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "claude:chat", ActionID: "local_config.save", Value: "oc-a", Actor: "user-1",
		FormValues: map[string]string{"reply_mode": string(config.ReplyModeLatestCard)},
	})
	if err != nil {
		t.Fatal(err)
	}
	event := lastEvent(t, renderer)
	if event.Type != "local_config_saved" {
		t.Fatalf("event type = %q, want local_config_saved", event.Type)
	}
	text := segmentText(event)
	if !strings.Contains(text, "仅本群生效") {
		t.Fatalf("save result text missing 仅本群生效: %q", text)
	}
}

// segmentText concatenates the text of every segment in an event.
func segmentText(event card.Event) string {
	var b strings.Builder
	for _, seg := range event.Segments {
		b.WriteString(seg.Text)
	}
	return b.String()
}
