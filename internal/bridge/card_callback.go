package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

func ActionRequestFromCardCallback(payload []byte) (ActionRequest, error) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return ActionRequest{}, err
	}
	action, _ := raw["action"].(map[string]any)
	contextValue, _ := raw["context"].(map[string]any)
	if action == nil {
		if event, _ := raw["event"].(map[string]any); event != nil {
			action, _ = event["action"].(map[string]any)
			contextValue, _ = event["context"].(map[string]any)
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
		SessionID:     stringField(value, "session"),
		ActionID:      stringField(value, "action_id"),
		Value:         stringField(value, "value"),
		Actor:         actorFromPayload(raw),
		ChatID:        stringField(contextValue, "open_chat_id"),
		OpenMessageID: stringField(contextValue, "open_message_id"),
		FormValues:    formValues,
		GrantID:       stringField(value, "grant_id"),
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
		text, ok := cardFormValueToString(value)
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

// cardFormValueToString 把飞书 CardKit 表单回调里可能出现的值都规约成字符串:
//   - string:原样
//   - bool(如 checker `checked` 的 form_value):"true"/"false",与 strconv.ParseBool 兼容
//   - []any(如 multi_select_static 的 form_value):兼容裸字符串和带 value 的选项
//     对象，并拼成逗号分隔，便于 handler 用 strings.Split 拆开
//   - 其它(数字、null 等):返回 false,交给 caller continue,与旧 string-only
//     行为语义一致(过去就是 continue)
//
// 之所以拼 CSV 而不是保 JSON:handler 里 `respond_to_bots` 一类 bool 值走
// strconv.ParseBool,checker 走同路径不用改;multi_select 只有 3 项,CSV 足够,
// 也避免调用方处理两种 encoding。
func cardFormValueToString(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			switch selected := item.(type) {
			case string:
				parts = append(parts, selected)
			case map[string]any:
				if value, ok := selected["value"].(string); ok {
					parts = append(parts, value)
				}
			}
		}
		return strings.Join(parts, ","), true
	default:
		return "", false
	}
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
