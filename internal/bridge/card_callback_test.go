package bridge

import "testing"

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

func TestActionRequestFromNestedEventPayload(t *testing.T) {
	req, err := ActionRequestFromCardCallback([]byte(`{
		"event": {
			"operator": {"open_id": "user-1"},
			"action": {
				"value": {
					"session": "claude:chat",
					"action_id": "choice_1",
					"value": "1"
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if req.ActionID != "choice_1" || req.Value != "1" {
		t.Fatalf("request = %#v", req)
	}
}

func TestActionRequestFromStringifiedValue(t *testing.T) {
	req, err := ActionRequestFromCardCallback([]byte(`{
		"operator": {"open_id": "user-1"},
		"action": {
			"value": "{\"session\":\"claude:chat\",\"action_id\":\"restart_session\",\"value\":\"restart\"}"
		}
	}`))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if req.SessionID != "claude:chat" || req.ActionID != "restart_session" || req.Value != "restart" {
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
