package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	sequenceUnknown  bool
	pendingSequence  int
	journal          NativeSequenceJournal
	binding          RenderBinding
	snapshot         *cardSnapshot
	observer         CardKitRenderObserver
	routerKey        string
	now              func() time.Time
}

type cardSnapshot struct {
	prepared          card.PreparedLarkCard
	staticFingerprint [32]byte
	nativeDisabled    bool
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
	journal   NativeSequenceJournal
	now       func() time.Time
	renderers map[string]*CardKitRenderer
}

type CardKitRenderObserver interface {
	Record(actor, action, sessionID, detail string)
}

func NewCardKitRouterRenderer(client CardKitClientAPI) *CardKitRouterRenderer {
	return NewCardKitRouterRendererWithObserver(client, nil)
}

func NewCardKitRouterRendererWithObserver(client CardKitClientAPI, observer CardKitRenderObserver) *CardKitRouterRenderer {
	return newCardKitRouterRendererWithClock(client, observer, nil, time.Now)
}

func NewCardKitRouterRendererWithObserverAndJournal(client CardKitClientAPI, observer CardKitRenderObserver, journal NativeSequenceJournal) *CardKitRouterRenderer {
	return newCardKitRouterRendererWithClock(client, observer, journal, time.Now)
}

func newCardKitRouterRendererWithClock(client CardKitClientAPI, observer CardKitRenderObserver, journal NativeSequenceJournal, now func() time.Time) *CardKitRouterRenderer {
	if now == nil {
		now = time.Now
	}
	return &CardKitRouterRenderer{client: client, observer: observer, journal: journal, now: now, renderers: map[string]*CardKitRenderer{}}
}

func NewCardKitRenderer(client CardKitClientAPI, replyToMessageID string) *CardKitRenderer {
	return NewCardKitRendererWithObserver(client, replyToMessageID, nil, "")
}

func NewCardKitRendererWithObserver(client CardKitClientAPI, replyToMessageID string, observer CardKitRenderObserver, routerKey string) *CardKitRenderer {
	return newCardKitRendererWithClock(client, replyToMessageID, observer, routerKey, RenderBinding{}, nil, time.Now)
}

func NewCardKitRendererWithNative(client CardKitClientAPI, replyToMessageID string, observer CardKitRenderObserver, binding RenderBinding, journal NativeSequenceJournal) *CardKitRenderer {
	return newCardKitRendererWithClock(client, replyToMessageID, observer, binding.RunCardSessionID, binding, journal, time.Now)
}

func newCardKitRendererWithClock(client CardKitClientAPI, replyToMessageID string, observer CardKitRenderObserver, routerKey string, binding RenderBinding, journal NativeSequenceJournal, now func() time.Time) *CardKitRenderer {
	if now == nil {
		now = time.Now
	}
	return &CardKitRenderer{client: client, replyToMessageID: replyToMessageID, observer: observer, routerKey: routerKey, binding: binding, journal: journal, now: now}
}

func (r *CardKitRouterRenderer) NewStreaming(ctx context.Context, sessionID, replyTo string) (ResumableRenderer, error) {
	return r.NewStreamingBound(ctx, RenderBinding{RunCardSessionID: sessionID}, replyTo)
}

func (r *CardKitRouterRenderer) NewStreamingBound(_ context.Context, binding RenderBinding, replyTo string) (ResumableRenderer, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("cardkit router renderer unavailable")
	}
	if binding.RunCardSessionID == "" || replyTo == "" {
		return nil, fmt.Errorf("cardkit streaming requires session and reply message ids")
	}
	renderer := newCardKitRendererWithClock(r.client, replyTo, r.observer, binding.RunCardSessionID, binding, r.journal, r.now)
	r.mu.Lock()
	r.renderers[binding.RunCardSessionID] = renderer
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
	renderer := newCardKitRendererWithClock(r.client, replyTo, r.observer, event.SessionID, RenderBinding{}, nil, r.now)
	return renderer.renderContext(ctx, event)
}

func (r *CardKitRouterRenderer) Rehydrate(sessionID string, ref session.RenderRef) ResumableRenderer {
	return r.RehydrateBound(RenderBinding{RunCardSessionID: sessionID}, ref)
}

func (r *CardKitRouterRenderer) RehydrateBound(binding RenderBinding, ref session.RenderRef) ResumableRenderer {
	renderer := newCardKitRendererWithClock(r.client, ref.ReplyMessageID, r.observer, binding.RunCardSessionID, binding, r.journal, r.now)
	renderer.cardID = ref.CardID
	renderer.replyMessageID = ref.ReplyMessageID
	renderer.sequence = ref.Version
	renderer.createdAt = ref.CreatedAt
	renderer.sequenceUnknown = ref.SequenceUnknown
	renderer.pendingSequence = ref.PendingSequence
	r.mu.Lock()
	r.renderers[binding.RunCardSessionID] = renderer
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
		renderer = newCardKitRendererWithClock(r.client, e.ReplyToMessageID, r.observer, key, RenderBinding{}, nil, r.now)
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
		createdAt := r.now().UTC()
		cardID := created.CardID
		r.recordRender("cardkit_create", e, fmt.Sprintf("key=%s card_id=%s reply_to=%s event=%s %s", r.renderKey(e), cardID, r.replyToMessageID, e.Type, renderAuditState(e)))
		if r.replyToMessageID != "" {
			replied, err := r.client.ReplyCard(ctx, CardKitReplyRequest{
				ReplyToMessageID: r.replyToMessageID,
				CardID:           cardID,
				UUID:             stableUUID("card-reply", r.replyToMessageID, cardID, e.SessionID, e.Type),
				ReplyInThread:    e.ReplyInThread,
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
			r.snapshot = snapshotForPrepared(prepared)
			r.recordRender("cardkit_reply", e, fmt.Sprintf("key=%s card_id=%s reply_to=%s message_id=%s event=%s %s", r.renderKey(e), cardID, r.replyToMessageID, replied.MessageID, e.Type, renderAuditState(e)))
			return nil
		}
		r.cardID = cardID
		r.snapshot = snapshotForPrepared(prepared)
		return nil
	}
	if err := r.updatePrepared(ctx, e, prepared); err != nil {
		return err
	}
	return nil
}

func (r *CardKitRenderer) updatePrepared(ctx context.Context, e card.Event, prepared card.PreparedLarkCard) error {
	if r.sequenceUnknown {
		r.recordRender("cardkit_sequence_unknown", e, fmt.Sprintf("key=%s card_id=%s pending_sequence=%d", r.renderKey(e), r.cardID, r.pendingSequence))
		return nil
	}
	if r.nativeCandidate(prepared) {
		return r.updateElementContent(ctx, e, prepared)
	}
	err := r.updateCard(ctx, e, prepared)
	if err == nil {
		r.snapshot = snapshotForPrepared(prepared)
	}
	return err
}

func (r *CardKitRenderer) nativeCandidate(prepared card.PreparedLarkCard) bool {
	if r.journal == nil || r.snapshot == nil || r.snapshot.nativeDisabled || r.interactionDepth != 0 || !validRenderBinding(r.binding) {
		return false
	}
	return prepared.EventCopy().Streaming && r.snapshot.prepared.NativeReady() && prepared.NativeReady() &&
		r.snapshot.staticFingerprint == staticFingerprint(prepared) &&
		prepared.Answer() != r.snapshot.prepared.Answer()
}

func (r *CardKitRenderer) updateElementContent(ctx context.Context, e card.Event, prepared card.PreparedLarkCard) error {
	candidate := r.sequence + 1
	intent := NativeSequenceIntent{
		SessionID: r.binding.BaseSessionID, BatchID: r.binding.BatchID, LatestScope: r.binding.LatestScope,
		Ref: r.renderRefLocked(), Candidate: candidate,
	}
	if err := r.journal.PrepareNative(ctx, intent); err != nil {
		r.sequenceUnknown = true
		r.pendingSequence = candidate
		r.recordRender("cardkit_sequence_unknown", e, fmt.Sprintf("key=%s card_id=%s sequence=%d branch=prepare_failed", r.renderKey(e), r.cardID, candidate))
		return nil
	}
	r.sequenceUnknown = true
	r.pendingSequence = candidate
	err := r.client.UpdateElementContent(ctx, CardKitUpdateElementContentRequest{
		CardID: r.cardID, ElementID: "answer", Content: prepared.Answer(), Sequence: candidate,
		UUID: stableUUID("card-element-content", r.cardID, "answer", fmt.Sprint(candidate)),
	})
	if errors.Is(err, ErrCardInteractionInProgress) {
		if abortErr := r.journal.AbortNative(ctx, intent); abortErr != nil {
			return nil
		}
		r.sequenceUnknown = false
		r.pendingSequence = 0
		r.recordRender("cardkit_text_stream_skipped_interaction", e, fmt.Sprintf("key=%s card_id=%s sequence=%d", r.renderKey(e), r.cardID, candidate))
		return nil
	}
	if err != nil {
		r.recordRender("cardkit_sequence_unknown", e, fmt.Sprintf("key=%s card_id=%s sequence=%d branch=delivery_unknown", r.renderKey(e), r.cardID, candidate))
		return nil
	}
	if err := r.journal.ConfirmNative(ctx, intent); err != nil {
		r.recordRender("cardkit_sequence_unknown", e, fmt.Sprintf("key=%s card_id=%s sequence=%d branch=confirm_failed", r.renderKey(e), r.cardID, candidate))
		return nil
	}
	r.sequence = candidate
	r.sequenceUnknown = false
	r.pendingSequence = 0
	r.snapshot = snapshotForPrepared(prepared)
	r.recordRender("cardkit_text_stream", e, fmt.Sprintf("key=%s card_id=%s element_id=answer sequence=%d bytes=%d", r.renderKey(e), r.cardID, candidate, len([]byte(prepared.Answer()))))
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
	return r.renderRefLocked()
}

func (r *CardKitRenderer) renderRefLocked() session.RenderRef {
	return session.RenderRef{CardID: r.cardID, ReplyMessageID: r.replyMessageID, Version: r.sequence, CreatedAt: r.createdAt, SequenceUnknown: r.sequenceUnknown, PendingSequence: r.pendingSequence}
}

func validRenderBinding(binding RenderBinding) bool {
	return binding.BaseSessionID != "" && binding.BatchID != "" && binding.RunCardSessionID != ""
}

func snapshotForPrepared(prepared card.PreparedLarkCard) *cardSnapshot {
	return &cardSnapshot{prepared: prepared, staticFingerprint: staticFingerprint(prepared)}
}

func staticFingerprint(prepared card.PreparedLarkCard) [32]byte {
	payload := prepared.PayloadCopy()
	blankAnswerContent(payload)
	normalizeHeaderElapsed(payload)
	encoded, _ := json.Marshal(payload)
	return sha256.Sum256(encoded)
}

func normalizeHeaderElapsed(payload map[string]any) {
	if header, ok := payload["header"].(map[string]any); ok {
		if title, ok := header["title"].(map[string]any); ok {
			normalizeElapsedContent(title)
		}
	}
	if config, ok := payload["config"].(map[string]any); ok {
		if summary, ok := config["summary"].(map[string]any); ok {
			normalizeElapsedContent(summary)
		}
	}
}

func normalizeElapsedContent(container map[string]any) {
	content, ok := container["content"].(string)
	if !ok {
		return
	}
	const separator = " · ⏱ "
	if index := strings.LastIndex(content, separator); index > 0 && index+len(separator) < len(content) {
		container["content"] = content[:index] + separator + "<elapsed>"
	}
}

func blankAnswerContent(value any) {
	switch node := value.(type) {
	case map[string]any:
		if node["tag"] == "markdown" && node["element_id"] == "answer" {
			node["content"] = ""
		}
		for _, child := range node {
			blankAnswerContent(child)
		}
	case []any:
		for _, child := range node {
			blankAnswerContent(child)
		}
	}
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
