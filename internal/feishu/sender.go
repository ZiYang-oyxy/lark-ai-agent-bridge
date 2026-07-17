package feishu

import "context"

type ReactionType string

const (
	ReactionTypeGet       ReactionType = "Get"
	ReactionTypeTyping    ReactionType = "Typing"
	ReactionTypeOneSecond ReactionType = "OneSecond"
	ReactionTypeDone      ReactionType = "DONE"
)

type Reply struct {
	ReplyToMessageID string
	TopicID          string
	Message          string
	ShouldReply      bool
	MentionSender    bool
	MentionOpenID    string
	Mentions         []Mention
	Kind             ReplyKind
}

type ReplyKind string

const (
	ReplyKindPlaceholder ReplyKind = "placeholder"
	ReplyKindFinal       ReplyKind = "final"
	ReplyKindError       ReplyKind = "error"
)

type SendResult struct {
	MessageID string
	ThreadID  string
	ChatID    string
}

type ReactionRequest struct {
	MessageID string
	Type      ReactionType
}

type ReactionResult struct {
	ReactionID string
}

type ReactionDeleteRequest struct {
	MessageID  string
	ReactionID string
	Type       ReactionType
}

type Sender interface {
	SendReply(ctx context.Context, reply Reply) (SendResult, error)
	AddReaction(ctx context.Context, req ReactionRequest) (ReactionResult, error)
	DeleteReaction(ctx context.Context, req ReactionDeleteRequest) error
}

type NoopSender struct{}

func (NoopSender) SendReply(context.Context, Reply) (SendResult, error) {
	return SendResult{}, nil
}

func (NoopSender) AddReaction(context.Context, ReactionRequest) (ReactionResult, error) {
	return ReactionResult{}, nil
}

func (NoopSender) DeleteReaction(context.Context, ReactionDeleteRequest) error {
	return nil
}
