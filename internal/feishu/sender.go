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

type ReactionSink interface {
	AddReaction(ctx context.Context, messageID string, reactionType ReactionType) (string, error)
	DeleteReaction(ctx context.Context, messageID, reactionID string) error
}

type Sender interface {
	SendReply(ctx context.Context, reply Reply) (SendResult, error)
	ReactionSink
}

type NoopSender struct{}

func (NoopSender) SendReply(context.Context, Reply) (SendResult, error) {
	return SendResult{}, nil
}

func (NoopSender) AddReaction(context.Context, string, ReactionType) (string, error) {
	return "", nil
}

func (NoopSender) DeleteReaction(context.Context, string, string) error {
	return nil
}
