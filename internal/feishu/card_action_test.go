package feishu

import (
	"strings"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestBuildCardActionFromLarkValueObject(t *testing.T) {
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_config"},
		Action: &callback.CallBackAction{Tag: "button", Value: map[string]any{
			"session":             "claude:chat",
			"action_id":           "stop",
			"value":               "ignored",
			"grant_id":            "grant-1",
			"preference_revision": "7",
		}},
	}}

	got, err := BuildCardActionFromLark(event)
	if err != nil {
		t.Fatalf("BuildCardActionFromLark error: %v", err)
	}
	if got.SessionID != "claude:chat" || got.ActionID != "stop" || got.Value != "ignored" || got.Actor != "ou_user" || got.ChatID != "oc_chat" || got.Tag != "button" || got.OpenMessageID != "om_config" || got.GrantID != "grant-1" || !got.HasPreferenceRevision || got.PreferenceRevision != 7 {
		t.Fatalf("action = %#v", got)
	}
}

func TestBuildCardActionFromLarkPreservesStringFormValues(t *testing.T) {
	formValues := map[string]any{"model": "opus", "effort": "high", "ignored": 42}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Action: &callback.CallBackAction{
			Tag:       "button",
			FormValue: formValues,
			Value:     map[string]any{"session": "claude:chat", "action_id": "config.save"},
		},
	}}
	got, err := BuildCardActionFromLark(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FormValues) != 2 || got.FormValues["model"] != "opus" || got.FormValues["effort"] != "high" {
		t.Fatalf("form values = %#v", got.FormValues)
	}
	formValues["model"] = "haiku"
	if got.FormValues["model"] != "opus" {
		t.Fatalf("form values alias source map: %#v", got.FormValues)
	}
}

func TestBuildCardActionFromLarkRejectsOversizedFormValues(t *testing.T) {
	tooMany := make(map[string]any)
	for i := 0; i < 17; i++ {
		tooMany[string(rune('a'+i))] = "value"
	}
	for _, formValues := range []map[string]any{
		tooMany,
		{"model": strings.Repeat("x", 129)},
		{strings.Repeat("k", 129): "opus"},
	} {
		_, err := BuildCardActionFromLark(&callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
			Action: &callback.CallBackAction{FormValue: formValues, Value: map[string]any{"session": "claude:chat", "action_id": "config.save"}},
		}})
		if err == nil {
			t.Fatalf("BuildCardActionFromLark(%#v) error = nil", formValues)
		}
	}
}

func TestBuildCardActionFromLarkFallbacks(t *testing.T) {
	userID := "user_1"
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{UserID: &userID},
		Action: &callback.CallBackAction{
			Tag:    "select_static",
			Name:   "choice_1",
			Option: "2",
			FormValue: map[string]any{
				"session": "claude:chat",
			},
		},
	}}

	got, err := BuildCardActionFromLark(event)
	if err != nil {
		t.Fatalf("BuildCardActionFromLark error: %v", err)
	}
	if got.SessionID != "claude:chat" || got.ActionID != "choice_1" || got.Value != "2" || got.Actor != "user_1" {
		t.Fatalf("action = %#v", got)
	}
}

func TestBuildCardActionFromLarkRequiresActionIDAndSession(t *testing.T) {
	_, err := BuildCardActionFromLark(&callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Action: &callback.CallBackAction{Value: map[string]any{"session": "claude:chat"}},
	}})
	if err == nil {
		t.Fatal("expected missing action_id error")
	}
	_, err = BuildCardActionFromLark(&callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Action: &callback.CallBackAction{Value: map[string]any{"action_id": "stop"}},
	}})
	if err == nil {
		t.Fatal("expected missing session error")
	}
}
