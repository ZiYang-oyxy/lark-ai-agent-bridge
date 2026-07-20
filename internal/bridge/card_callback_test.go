package bridge

import (
	"fmt"
	"strings"
	"testing"
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
