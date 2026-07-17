package feishu

import (
	"context"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestSDKLongConnDispatchesCardActionTrigger(t *testing.T) {
	var got CardAction
	client := NewLongConnClient(LongConnConfig{
		AppID:     "cli_test",
		AppSecret: "secret",
		ActionHandler: func(_ context.Context, action CardAction) (*CardActionResponse, error) {
			got = action
			return nil, nil
		},
	})
	sdk, ok := client.(*SDKLongConnClient)
	if !ok {
		t.Fatalf("client type = %T, want *SDKLongConnClient", client)
	}
	payload := []byte(`{
		"schema": "2.0",
		"header": {"event_type": "card.action.trigger"},
		"event": {
			"operator": {"open_id": "ou_user"},
			"action": {
				"tag": "button",
				"value": {
					"session": "claude:chat",
					"action_id": "stop",
					"value": ""
				}
			}
		}
	}`)
	if _, err := sdk.dispatcher.Do(context.Background(), payload); err != nil {
		t.Fatalf("dispatcher.Do error: %v", err)
	}
	if got.SessionID != "claude:chat" || got.ActionID != "stop" || got.Actor != "ou_user" {
		t.Fatalf("action = %#v", got)
	}
}

func TestSDKLongConnReturnsCardActionResponseCard(t *testing.T) {
	client := NewLongConnClient(LongConnConfig{
		AppID:     "cli_test",
		AppSecret: "secret",
		ActionHandler: func(_ context.Context, action CardAction) (*CardActionResponse, error) {
			return &CardActionResponse{Card: map[string]any{
				"schema": "2.0",
				"header": map[string]any{
					"title": map[string]any{"tag": "plain_text", "content": action.ActionID},
				},
			}}, nil
		},
	})
	sdk, ok := client.(*SDKLongConnClient)
	if !ok {
		t.Fatalf("client type = %T, want *SDKLongConnClient", client)
	}
	payload := []byte(`{
		"schema": "2.0",
		"header": {"event_type": "card.action.trigger"},
		"event": {
			"operator": {"open_id": "ou_user"},
			"action": {
				"tag": "button",
				"value": {
					"session": "claude:chat",
					"action_id": "stop",
					"value": ""
				}
			}
		}
	}`)
	resp, err := sdk.dispatcher.Do(context.Background(), payload)
	if err != nil {
		t.Fatalf("dispatcher.Do error: %v", err)
	}
	got, ok := resp.(*callback.CardActionTriggerResponse)
	if !ok {
		t.Fatalf("response = %T, want *callback.CardActionTriggerResponse", resp)
	}
	if got.Card == nil || got.Card.Type != "card_json" {
		t.Fatalf("response card = %#v, want card_json", got.Card)
	}
	data, ok := got.Card.Data.(map[string]any)
	if !ok || data["schema"] != "2.0" {
		t.Fatalf("response card data = %#v", got.Card.Data)
	}
}
