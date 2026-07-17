package feishu

import (
	"context"

	"lark-agent-bridge/internal/card"
)

type ReactionCardRenderer struct {
	Sender Sender
	Cards  card.Renderer
}

func NewReactionCardRenderer(sender Sender, cards card.Renderer) *ReactionCardRenderer {
	return &ReactionCardRenderer{Sender: sender, Cards: cards}
}

func (r *ReactionCardRenderer) Render(e card.Event) error {
	if e.Type == "reaction" && e.ReplyToMessageID != "" && r.Sender != nil {
		_, err := r.Sender.AddReaction(context.Background(), ReactionRequest{
			MessageID: e.ReplyToMessageID,
			Type:      ReactionTypeGet,
		})
		return err
	}
	if r.Cards == nil {
		return nil
	}
	return r.Cards.Render(e)
}

var _ card.Renderer = (*ReactionCardRenderer)(nil)
