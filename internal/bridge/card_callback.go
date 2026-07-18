package bridge

import (
	"encoding/json"
	"fmt"
)

func ActionRequestFromCardCallback(payload []byte) (ActionRequest, error) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return ActionRequest{}, err
	}
	action, _ := raw["action"].(map[string]any)
	if action == nil {
		if event, _ := raw["event"].(map[string]any); event != nil {
			action, _ = event["action"].(map[string]any)
			if operator, _ := event["operator"].(map[string]any); operator != nil {
				raw["operator"] = operator
			}
		}
	}
	if action == nil {
		return ActionRequest{}, fmt.Errorf("missing card callback action")
	}
	value, err := actionValueMap(action)
	if err != nil {
		return ActionRequest{}, err
	}
	if value == nil {
		return ActionRequest{}, fmt.Errorf("missing card callback action value")
	}
	formValues, err := callbackFormValues(action)
	if err != nil {
		return ActionRequest{}, err
	}
	req := ActionRequest{
		SessionID:  stringField(value, "session"),
		ActionID:   stringField(value, "action_id"),
		Value:      stringField(value, "value"),
		Actor:      actorFromPayload(raw),
		FormValues: formValues,
	}
	if req.ActionID == "" {
		req.ActionID = stringField(action, "action_id")
	}
	if req.ActionID == "" {
		return ActionRequest{}, fmt.Errorf("missing action_id")
	}
	if req.SessionID == "" {
		return ActionRequest{}, fmt.Errorf("missing session")
	}
	return req, nil
}

func callbackFormValues(action map[string]any) (map[string]string, error) {
	var raw map[string]any
	for _, key := range []string{"form_value", "formValue"} {
		if value, ok := action[key]; ok {
			var valid bool
			raw, valid = value.(map[string]any)
			if !valid {
				return nil, fmt.Errorf("invalid card callback %s", key)
			}
			break
		}
	}
	if len(raw) > 16 {
		return nil, fmt.Errorf("too many card form values: %d", len(raw))
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		text, ok := value.(string)
		if !ok {
			continue
		}
		if len(key) > 128 || len(text) > 128 {
			return nil, fmt.Errorf("card form value exceeds 128 bytes")
		}
		out[key] = text
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func actionValueMap(action map[string]any) (map[string]any, error) {
	if value, ok := action["value"].(map[string]any); ok {
		return value, nil
	}
	if raw, ok := action["value"].(string); ok {
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, fmt.Errorf("parse card callback action value: %w", err)
		}
		return value, nil
	}
	return nil, nil
}

func actorFromPayload(raw map[string]any) string {
	if operator, _ := raw["operator"].(map[string]any); operator != nil {
		for _, key := range []string{"open_id", "openId", "user_id", "userId"} {
			if value := stringField(operator, key); value != "" {
				return value
			}
		}
	}
	return ""
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if value, ok := m[key].(string); ok {
		return value
	}
	return ""
}
