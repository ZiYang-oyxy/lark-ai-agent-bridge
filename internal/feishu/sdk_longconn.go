package feishu

import (
	"context"
	"fmt"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	dispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/larksuite/oapi-sdk-go/v3/ws"
)

type SDKLongConnClient struct {
	cfg        LongConnConfig
	dispatcher *dispatcher.EventDispatcher
	client     *ws.Client
	handler    func(context.Context, InboundMessage) error
}

func NewLongConnClient(cfg LongConnConfig) LongConnClient {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return NewNoopLongConnClient()
	}
	c := &SDKLongConnClient{cfg: cfg}
	eventDispatcher := c.newEventDispatcher()
	c.dispatcher = eventDispatcher
	c.client = ws.NewClient(
		cfg.AppID,
		cfg.AppSecret,
		ws.WithEventHandler(eventDispatcher),
		ws.WithAutoReconnect(true),
		ws.WithLogLevel(larkcore.LogLevelInfo),
	)
	return c
}

func (c *SDKLongConnClient) newEventDispatcher() *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			if c.handler == nil {
				return nil
			}
			return c.handler(ctx, BuildInboundMessageFromLark(event, c.cfg.BotOpenID))
		}).
		OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			if c.cfg.ActionHandler == nil {
				return &callback.CardActionTriggerResponse{}, nil
			}
			action, err := BuildCardActionFromLark(event)
			if err != nil {
				return cardActionToast("error", "invalid card action"), nil
			}
			resp, err := c.cfg.ActionHandler(ctx, action)
			if err != nil {
				return cardActionToast("error", "card action failed"), nil
			}
			return buildCardActionTriggerResponse(resp), nil
		}).
		OnP2MessageReadV1(func(context.Context, *larkim.P2MessageReadV1) error { return nil }).
		OnP2MessageReactionCreatedV1(func(context.Context, *larkim.P2MessageReactionCreatedV1) error { return nil }).
		OnP2MessageReactionDeletedV1(func(context.Context, *larkim.P2MessageReactionDeletedV1) error { return nil })
}

func cardActionToast(kind, content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: kind, Content: content},
	}
}

func buildCardActionTriggerResponse(resp *CardActionResponse) *callback.CardActionTriggerResponse {
	if resp == nil {
		return &callback.CardActionTriggerResponse{}
	}
	out := &callback.CardActionTriggerResponse{}
	if resp.ToastContent != "" {
		out.Toast = &callback.Toast{Type: resp.ToastType, Content: resp.ToastContent}
	}
	if resp.Card != nil {
		out.Card = &callback.Card{Type: "card_json", Data: resp.Card}
	}
	return out
}

func (c *SDKLongConnClient) Run(ctx context.Context, handler func(context.Context, InboundMessage) error) error {
	if c.client == nil {
		return fmt.Errorf("feishu long connection client not initialized")
	}
	c.handler = handler
	return c.client.Start(ctx)
}
