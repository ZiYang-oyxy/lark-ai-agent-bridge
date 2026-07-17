package feishu

import (
	"context"
	"fmt"
	"sync"

	"lark-agent-bridge/internal/card"
)

type CardKitRenderer struct {
	mu               sync.Mutex
	client           CardKitClientAPI
	replyToMessageID string
	cardID           string
	sequence         int
	observer         CardKitRenderObserver
	routerKey        string
}

type CardKitRouterRenderer struct {
	mu        sync.Mutex
	client    CardKitClientAPI
	observer  CardKitRenderObserver
	renderers map[string]*CardKitRenderer
}

type CardKitRenderObserver interface {
	Record(actor, action, sessionID, detail string)
}

func NewCardKitRouterRenderer(client CardKitClientAPI) *CardKitRouterRenderer {
	return NewCardKitRouterRendererWithObserver(client, nil)
}

func NewCardKitRouterRendererWithObserver(client CardKitClientAPI, observer CardKitRenderObserver) *CardKitRouterRenderer {
	return &CardKitRouterRenderer{client: client, observer: observer, renderers: map[string]*CardKitRenderer{}}
}

func NewCardKitRenderer(client CardKitClientAPI, replyToMessageID string) *CardKitRenderer {
	return NewCardKitRendererWithObserver(client, replyToMessageID, nil, "")
}

func NewCardKitRendererWithObserver(client CardKitClientAPI, replyToMessageID string, observer CardKitRenderObserver, routerKey string) *CardKitRenderer {
	return &CardKitRenderer{client: client, replyToMessageID: replyToMessageID, observer: observer, routerKey: routerKey}
}

func (r *CardKitRouterRenderer) Render(e card.Event) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("cardkit router renderer unavailable")
	}
	key := e.SessionID
	if key == "" {
		key = e.ReplyToMessageID
	}
	r.mu.Lock()
	renderer := r.renderers[key]
	if renderer == nil {
		if e.ReplyToMessageID == "" {
			r.mu.Unlock()
			return fmt.Errorf("missing reply message id for new card session %q", key)
		}
		renderer = NewCardKitRendererWithObserver(r.client, e.ReplyToMessageID, r.observer, key)
		r.renderers[key] = renderer
	}
	r.mu.Unlock()
	return renderer.Render(e)
}

func (r *CardKitRouterRenderer) ActiveCards() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.renderers)
}

var _ card.Renderer = (*CardKitRouterRenderer)(nil)
var _ card.Renderer = (*CardKitRenderer)(nil)

func (r *CardKitRenderer) Render(e card.Event) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("cardkit renderer unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx := context.Background()
	payload := card.BuildLarkCard(e)
	if r.cardID == "" {
		created, err := r.client.CreateCard(ctx, CardKitCreateRequest{Card: payload})
		if err != nil {
			return err
		}
		r.cardID = created.CardID
		r.recordRender("cardkit_create", e, fmt.Sprintf("key=%s card_id=%s reply_to=%s event=%s %s", r.renderKey(e), r.cardID, r.replyToMessageID, e.Type, renderAuditState(e)))
		if r.replyToMessageID != "" {
			_, err = r.client.ReplyCard(ctx, CardKitReplyRequest{
				ReplyToMessageID: r.replyToMessageID,
				CardID:           r.cardID,
				UUID:             stableUUID("card-reply", r.replyToMessageID, r.cardID, e.SessionID, e.Type),
			})
			if err != nil {
				return err
			}
			r.recordRender("cardkit_reply", e, fmt.Sprintf("key=%s card_id=%s reply_to=%s event=%s %s", r.renderKey(e), r.cardID, r.replyToMessageID, e.Type, renderAuditState(e)))
		}
		return nil
	}
	if err := r.updateCard(ctx, e, payload); err != nil {
		return err
	}
	return nil
}

func (r *CardKitRenderer) updateCard(ctx context.Context, e card.Event, payload map[string]any) error {
	r.sequence++
	err := r.client.UpdateCard(ctx, CardKitUpdateCardRequest{
		CardID:   r.cardID,
		Card:     payload,
		Sequence: r.sequence,
		UUID:     stableUUID("card-update", r.cardID, e.SessionID, e.Type, fmt.Sprint(r.sequence)),
	})
	if err != nil {
		return err
	}
	r.recordRender("cardkit_update", e, fmt.Sprintf("key=%s card_id=%s event=%s sequence=%d %s", r.renderKey(e), r.cardID, e.Type, r.sequence, renderAuditState(e)))
	return nil
}

func (r *CardKitRenderer) recordRender(action string, e card.Event, detail string) {
	if r.observer == nil {
		return
	}
	sessionID := e.SessionID
	if sessionID == "" {
		sessionID = r.renderKey(e)
	}
	r.observer.Record("system", action, sessionID, detail)
}

func (r *CardKitRenderer) renderKey(e card.Event) string {
	if r.routerKey != "" {
		return r.routerKey
	}
	if e.SessionID != "" {
		return e.SessionID
	}
	return e.ReplyToMessageID
}

func renderAuditState(e card.Event) string {
	return fmt.Sprintf("title=%q template=%s streaming=%t stop_visible=%t stop_disabled=%t", e.HeaderTitle, e.HeaderTemplate, e.Streaming, e.StopButton.Visible, e.StopButton.Disabled)
}
