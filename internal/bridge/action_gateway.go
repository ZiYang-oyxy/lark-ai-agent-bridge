package bridge

import (
	"context"
	"errors"
)

type CardInteractionFencer interface {
	BeginCardInteraction(sessionID string) (release func())
}

type ActionGateway struct {
	Service *Service
	Fencer  CardInteractionFencer
}

func (g ActionGateway) Handle(ctx context.Context, req ActionRequest) (ActionResult, error) {
	release := func() {}
	if g.Fencer != nil {
		release = g.Fencer.BeginCardInteraction(req.SessionID)
		if release == nil {
			release = func() {}
		}
	}
	defer release()
	if g.Service == nil {
		return ActionResult{}, errors.New("action service unavailable")
	}
	return g.Service.HandleActionResult(ctx, req)
}
