package feishu

import "context"

type LongConnConfig struct {
	AppID         string
	AppSecret     string
	BotOpenID     string
	ActionHandler func(context.Context, CardAction) (*CardActionResponse, error)
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
