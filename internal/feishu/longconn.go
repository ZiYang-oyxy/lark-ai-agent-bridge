package feishu

import (
	"context"

	"lark-agent-bridge/internal/feishueventlog"
)

type LongConnConfig struct {
	AppID                  string
	AppSecret              string
	BotOpenID              string
	ActionHandler          func(context.Context, CardAction) (*CardActionResponse, error)
	MessageRecalledHandler func(context.Context, RecalledMessage) error
	RawEventHandler        func(context.Context, feishueventlog.Event)
}

type CardActionResponse struct {
	ToastType    string
	ToastContent string
	Card         map[string]any
}

type LongConnClient interface {
	Run(ctx context.Context, handler func(context.Context, InboundMessage) error) error
}

type NoopLongConnClient struct{}

func NewNoopLongConnClient() *NoopLongConnClient {
	return &NoopLongConnClient{}
}

func (NoopLongConnClient) Run(ctx context.Context, _ func(context.Context, InboundMessage) error) error {
	<-ctx.Done()
	return ctx.Err()
}
