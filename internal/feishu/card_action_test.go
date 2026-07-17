package feishu

import (
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestBuildCardActionFromLarkValueObject(t *testing.T) {
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Action: &callback.CallBackAction{Tag: "button", Value: map[string]any{
			"session":   "claude:chat",
			"action_id": "stop",
			"value":     "ignored",
		}},
	}}

	got, err := BuildCardActionFromLark(event)
	if err != nil {
		t.Fatalf("BuildCardActionFromLark error: %v", err)
	}
	if got.SessionID != "claude:chat" || got.ActionID != "stop" || got.Value != "ignored" || got.Actor != "ou_user" || got.Tag != "button" {
		t.Fatalf("action = %#v", got)
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
				"session": "codex:chat",
			},
		},
	}}

	got, err := BuildCardActionFromLark(event)
	if err != nil {
		t.Fatalf("BuildCardActionFromLark error: %v", err)
	}
	if got.SessionID != "codex:chat" || got.ActionID != "choice_1" || got.Value != "2" || got.Actor != "user_1" {
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
