package feishu

import (
	"context"
	"io"
)

type ReactionType string

const (
	ReactionTypeGet       ReactionType = "Get"
	ReactionTypeTyping    ReactionType = "Typing"
	ReactionTypeOneSecond ReactionType = "OneSecond"
	ReactionTypeDone      ReactionType = "DONE"
)

type Reply struct {
	ReplyToMessageID string
	ReplyInThread    bool
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
	// DeleteMessage removes a message the bot previously sent. Feishu allows
	// bots to recall their own messages within a bounded window (currently
	// ~2 minutes); callers that miss the window must tolerate a benign error.
	DeleteMessage(ctx context.Context, messageID string) error
	ReactionSink
}

type ImageReply struct {
	ReplyToMessageID string
	ReplyInThread    bool
	ImageKey         string
	UUID             string
}

type ImageSender interface {
	UploadImage(context.Context, io.Reader) (string, error)
	ReplyImage(context.Context, ImageReply) (SendResult, error)
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

func (NoopSender) DeleteMessage(context.Context, string) error {
	return nil
}

func (NoopSender) UploadImage(context.Context, io.Reader) (string, error) {
	return "", nil
}

func (NoopSender) ReplyImage(context.Context, ImageReply) (SendResult, error) {
	return SendResult{}, nil
}
