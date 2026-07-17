package feishu

import (
	"context"
	"testing"

	"lark-agent-bridge/internal/card"
)

type fakeSender struct {
	reactions []ReactionRequest
}

func (f *fakeSender) SendReply(context.Context, Reply) (SendResult, error) {
	return SendResult{}, nil
}

func (f *fakeSender) AddReaction(_ context.Context, req ReactionRequest) (ReactionResult, error) {
	f.reactions = append(f.reactions, req)
	return ReactionResult{ReactionID: "r1"}, nil
}

func (f *fakeSender) DeleteReaction(context.Context, ReactionDeleteRequest) error {
	return nil
}

func TestReactionCardRendererRoutesQueuedReaction(t *testing.T) {
	sender := &fakeSender{}
	cards := card.NewFakeRenderer()
	renderer := NewReactionCardRenderer(sender, cards)
	err := renderer.Render(card.Event{Type: "reaction", ReplyToMessageID: "m1", Message: "queued"})
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if len(sender.reactions) != 1 {
		t.Fatalf("reactions len = %d, want 1", len(sender.reactions))
	}
	if len(cards.Events()) != 0 {
		t.Fatalf("card events len = %d, want 0", len(cards.Events()))
	}
}

func TestReactionCardRendererDelegatesNormalCard(t *testing.T) {
	sender := &fakeSender{}
	cards := card.NewFakeRenderer()
	renderer := NewReactionCardRenderer(sender, cards)
	err := renderer.Render(card.Event{Type: "stream", SessionID: "s1"})
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if len(cards.Events()) != 1 {
		t.Fatalf("card events len = %d, want 1", len(cards.Events()))
	}
}
