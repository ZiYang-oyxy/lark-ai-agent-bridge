package feishu

import (
	"context"
	"errors"
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

func TestSDKLongConnReturnsRawCardActionResponse(t *testing.T) {
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
	if got.Card == nil || got.Card.Type != "raw" {
		t.Fatalf("response card = %#v, want raw", got.Card)
	}
	data, ok := got.Card.Data.(map[string]any)
	if !ok || data["schema"] != "2.0" {
		t.Fatalf("response card data = %#v", got.Card.Data)
	}
}

func TestBuildCardActionTriggerResponseOmitsEmptyCardAndToast(t *testing.T) {
	got := buildCardActionTriggerResponse(&CardActionResponse{})
	if got.Card != nil || got.Toast != nil {
		t.Fatalf("response = %#v, want empty success", got)
	}
}

func TestSDKLongConnReturnsErrorToastWithoutCard(t *testing.T) {
	client := NewLongConnClient(LongConnConfig{
		AppID:     "cli_test",
		AppSecret: "secret",
		ActionHandler: func(context.Context, CardAction) (*CardActionResponse, error) {
			return nil, errors.New("delete failed")
		},
	})
	sdk := client.(*SDKLongConnClient)
	payload := []byte(`{
		"schema":"2.0",
		"header":{"event_type":"card.action.trigger"},
		"event":{"operator":{"open_id":"ou_user"},"context":{"open_message_id":"om_config"},"action":{"tag":"button","value":{"session":"config-card","action_id":"config.close"}}}
	}`)
	resp, err := sdk.dispatcher.Do(t.Context(), payload)
	if err != nil {
		t.Fatal(err)
	}
	got := resp.(*callback.CardActionTriggerResponse)
	if got.Card != nil || got.Toast == nil || got.Toast.Type != "error" {
		t.Fatalf("response = %#v, want error toast without card", got)
	}
}

func TestSDKLongConnDispatchesMessageRecalled(t *testing.T) {
	var got RecalledMessage
	client := NewLongConnClient(LongConnConfig{
		AppID:     "cli_test",
		AppSecret: "secret",
		MessageRecalledHandler: func(_ context.Context, recall RecalledMessage) error {
			got = recall
			return nil
		},
	})
	sdk, ok := client.(*SDKLongConnClient)
	if !ok {
		t.Fatalf("client type = %T, want *SDKLongConnClient", client)
	}
	payload := []byte(`{
		"schema": "2.0",
		"header": {
			"event_type": "im.message.recalled_v1",
			"app_id": "cli_test",
			"create_time": "1700000000000"
		},
		"event": {
			"message_id": "<FEISHU_MESSAGE_ID>",
			"chat_id": "oc_chat",
			"recall_time": "1700000001000",
			"recall_type": "user"
		}
	}`)
	if _, err := sdk.dispatcher.Do(context.Background(), payload); err != nil {
		t.Fatalf("dispatcher.Do error: %v", err)
	}
	if got.AppID != "cli_test" || got.ChatID != "oc_chat" || got.MessageID != "<FEISHU_MESSAGE_ID>" || got.RecallType != "user" {
		t.Fatalf("recall = %#v", got)
	}
	if got.OccurredAt.IsZero() {
		t.Fatalf("recall occurred at is zero: %#v", got)
	}
}
