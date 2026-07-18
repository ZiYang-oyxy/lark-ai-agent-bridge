package feishu

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/session"
)

type ResumableRenderer interface {
	card.Renderer
	RenderRef() session.RenderRef
}

type ContextRenderer interface {
	RenderContext(context.Context, card.Event) error
}

type CardKitRenderer struct {
	mu               sync.Mutex
	client           CardKitClientAPI
	replyToMessageID string
	replyMessageID   string
	cardID           string
	sequence         int
	createdAt        time.Time
	interactionDepth int
	observer         CardKitRenderObserver
	routerKey        string
}

func (r *CardKitRouterRenderer) BeginCardInteraction(sessionID string) func() {
	if r == nil || sessionID == "" {
		return func() {}
	}
	r.mu.Lock()
	renderer := r.renderers[sessionID]
	r.mu.Unlock()
	if renderer == nil {
		return func() {}
	}
	renderer.mu.Lock()
	renderer.interactionDepth++
	renderer.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			renderer.mu.Lock()
			if renderer.interactionDepth > 0 {
				renderer.interactionDepth--
			}
			renderer.mu.Unlock()
		})
	}
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

func (r *CardKitRouterRenderer) NewStreaming(_ context.Context, sessionID, replyTo string) (ResumableRenderer, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("cardkit router renderer unavailable")
	}
	if sessionID == "" || replyTo == "" {
		return nil, fmt.Errorf("cardkit streaming requires session and reply message ids")
	}
	renderer := NewCardKitRendererWithObserver(r.client, replyTo, r.observer, sessionID)
	r.mu.Lock()
	r.renderers[sessionID] = renderer
	r.mu.Unlock()
	return renderer, nil
}

func (r *CardKitRouterRenderer) AppendTerminal(ctx context.Context, replyTo string, event card.Event) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("cardkit router renderer unavailable")
	}
	if replyTo == "" {
		return fmt.Errorf("missing reply message id for terminal card")
	}
	renderer := NewCardKitRendererWithObserver(r.client, replyTo, r.observer, event.SessionID)
	return renderer.renderContext(ctx, event)
}

func (r *CardKitRouterRenderer) Rehydrate(sessionID string, ref session.RenderRef) ResumableRenderer {
	renderer := NewCardKitRendererWithObserver(r.client, ref.ReplyMessageID, r.observer, sessionID)
	renderer.cardID = ref.CardID
	renderer.replyMessageID = ref.ReplyMessageID
	renderer.sequence = ref.Version
	renderer.createdAt = ref.CreatedAt
	r.mu.Lock()
	r.renderers[sessionID] = renderer
	r.mu.Unlock()
	return renderer
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
	return r.renderContext(context.Background(), e)
}

func (r *CardKitRenderer) RenderContext(ctx context.Context, e card.Event) error {
	return r.renderContext(ctx, e)
}

func (r *CardKitRenderer) renderContext(ctx context.Context, e card.Event) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("cardkit renderer unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prepared, err := card.PrepareLarkCard(e)
	if err != nil {
		return err
	}
	if r.cardID == "" {
		created, err := r.client.CreateCard(ctx, CardKitCreateRequest{Prepared: &prepared})
		if errors.Is(err, card.ErrCardPayloadOversize) {
			emergency, emergencyErr := card.PrepareEmergencyLarkCard()
			if emergencyErr != nil {
				return emergencyErr
			}
			created, err = r.client.CreateCard(ctx, CardKitCreateRequest{Prepared: &emergency})
		}
		if err != nil {
			return err
		}
		createdAt := time.Now().UTC()
		cardID := created.CardID
		r.recordRender("cardkit_create", e, fmt.Sprintf("key=%s card_id=%s reply_to=%s event=%s %s", r.renderKey(e), cardID, r.replyToMessageID, e.Type, renderAuditState(e)))
		if r.replyToMessageID != "" {
			replied, err := r.client.ReplyCard(ctx, CardKitReplyRequest{
				ReplyToMessageID: r.replyToMessageID,
				CardID:           cardID,
				UUID:             stableUUID("card-reply", r.replyToMessageID, cardID, e.SessionID, e.Type),
			})
			if err != nil {
				return err
			}
			if replied.MessageID == "" {
				return fmt.Errorf("cardkit reply returned empty message id")
			}
			r.replyMessageID = replied.MessageID
			r.cardID = cardID
			r.createdAt = createdAt
			r.recordRender("cardkit_reply", e, fmt.Sprintf("key=%s card_id=%s reply_to=%s event=%s %s", r.renderKey(e), cardID, r.replyToMessageID, e.Type, renderAuditState(e)))
			return nil
		}
		r.cardID = cardID
		return nil
	}
	if err := r.updateCard(ctx, e, prepared); err != nil {
		return err
	}
	return nil
}

func (r *CardKitRenderer) updateCard(ctx context.Context, e card.Event, prepared card.PreparedLarkCard) error {
	nextSequence := r.sequence + 1
	request := CardKitUpdateCardRequest{
		CardID:   r.cardID,
		Prepared: &prepared,
		Sequence: nextSequence,
		UUID:     stableUUID("card-update", r.cardID, e.SessionID, e.Type, fmt.Sprint(nextSequence)),
	}
	err := r.client.UpdateCard(ctx, request)
	if errors.Is(err, card.ErrCardPayloadOversize) {
		emergency, emergencyErr := card.PrepareEmergencyLarkCard()
		if emergencyErr != nil {
			return emergencyErr
		}
		request.Prepared = &emergency
		err = r.client.UpdateCard(ctx, request)
	}
	if err != nil {
		return classifyRenderUpdateError(err)
	}
	r.sequence = nextSequence
	r.recordRender("cardkit_update", e, fmt.Sprintf("key=%s card_id=%s event=%s sequence=%d %s", r.renderKey(e), r.cardID, e.Type, r.sequence, renderAuditState(e)))
	return nil
}

func (r *CardKitRenderer) RenderRef() session.RenderRef {
	if r == nil {
		return session.RenderRef{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cardID == "" || r.replyMessageID == "" {
		return session.RenderRef{}
	}
	return session.RenderRef{CardID: r.cardID, ReplyMessageID: r.replyMessageID, Version: r.sequence, CreatedAt: r.createdAt}
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
