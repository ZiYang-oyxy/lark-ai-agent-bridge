package feishu

import (
	"fmt"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

type CardAction struct {
	SessionID string
	ActionID  string
	Value     string
	Actor     string
	Tag       string
	Option    string
}

func BuildCardActionFromLark(event *callback.CardActionTriggerEvent) (CardAction, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return CardAction{}, fmt.Errorf("missing card action event")
	}
	action := event.Event.Action
	value := action.Value
	formValue := action.FormValue
	req := CardAction{
		SessionID: anyString(value["session"]),
		ActionID:  anyString(value["action_id"]),
		Value:     anyString(value["value"]),
		Actor:     operatorActor(event.Event.Operator),
		Tag:       action.Tag,
		Option:    action.Option,
	}
	if req.SessionID == "" {
		req.SessionID = anyString(formValue["session"])
	}
	if req.ActionID == "" {
		req.ActionID = firstNonEmpty(
			action.Name,
			anyString(formValue["action_id"]),
			anyString(formValue["name"]),
			action.Option,
		)
	}
	if req.Value == "" {
		req.Value = firstNonEmpty(
			action.Option,
			action.InputValue,
			anyString(formValue["value"]),
			anyString(formValue[action.Name]),
		)
	}
	if req.ActionID == "" {
		return CardAction{}, fmt.Errorf("missing action_id")
	}
	if req.SessionID == "" {
		return CardAction{}, fmt.Errorf("missing session")
	}
	return req, nil
}

func operatorActor(operator *callback.Operator) string {
	if operator == nil {
		return ""
	}
	if operator.OpenID != "" {
		return operator.OpenID
	}
	if operator.UserID != nil && *operator.UserID != "" {
		return *operator.UserID
	}
	if operator.TenantKey != nil {
		return *operator.TenantKey
	}
	return ""
}

func anyString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case []string:
		if len(v) > 0 {
			return v[0]
		}
	case []any:
		if len(v) > 0 {
			return anyString(v[0])
		}
	default:
		return fmt.Sprint(v)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
