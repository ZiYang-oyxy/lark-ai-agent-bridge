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
		// 运行期(非终态)维持隔离语义:旧卡序号不确定,不再续写。
		// 但终态必须显式收敛——若在这里静默返回 nil,一次 native 丢包后卡片会永久停在运行态、
		// 停止按钮不置灰,且上层误判成功。终态改走补偿:新建一张终态卡并记录 audit。
		if !prepared.EventCopy().Streaming {
			return r.recoverTerminalCard(ctx, e, prepared)
		}
		r.recordRender("cardkit_sequence_unknown", e, fmt.Sprintf("key=%s card_id=%s pending_sequence=%d", r.renderKey(e), r.cardID, r.pendingSequence))
		return nil
	}
	fallbackReason := r.nativeFallbackReason(prepared)
	if fallbackReason == "" {
		return r.updateElementContent(ctx, e, prepared)
	}
	if prepared.EventCopy().Streaming && r.snapshot != nil {
		r.recordRender("cardkit_native_fallback", e, fmt.Sprintf("key=%s card_id=%s reason=%s", r.renderKey(e), r.cardID, fallbackReason))
	}
	err := r.updateCard(ctx, e, prepared)
	if err == nil {
		r.snapshot = snapshotForPrepared(prepared)
	}
	return err
}

// recoverTerminalCard 在旧卡序号不确定时,为终态新建一张独立卡片(reply 到原消息),
// 使终态内容与按钮终态一定可见,并记录 audit;旧卡无法收敛的事实通过 audit 暴露,不静默吞掉。
func (r *CardKitRenderer) recoverTerminalCard(ctx context.Context, e card.Event, prepared card.PreparedLarkCard) error {
	if r.replyToMessageID == "" {
		// 无法 reply(缺原消息),只能记录旧卡无法收敛,交由上层观测。
		r.recordRender("cardkit_terminal_recovery_skipped", e, fmt.Sprintf("key=%s card_id=%s reason=missing_reply_to", r.renderKey(e), r.cardID))
		return nil
	}
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
	replied, err := r.client.ReplyCard(ctx, CardKitReplyRequest{
		ReplyToMessageID: r.replyToMessageID,
		CardID:           created.CardID,
		UUID:             stableUUID("card-terminal-recovery", r.replyToMessageID, created.CardID, e.SessionID, e.Type),
		ReplyInThread:    e.ReplyInThread,
	})
	if err != nil {
		return err
	}
	// 旧卡序号不确定的状态保留(pendingSequence),但终态已在新卡上收敛。
	r.recordRender("cardkit_terminal_recovered_new_card", e, fmt.Sprintf("key=%s stale_card_id=%s new_card_id=%s new_message_id=%s event=%s", r.renderKey(e), r.cardID, created.CardID, replied.MessageID, e.Type))
	return nil
}

func (r *CardKitRenderer) nativeFallbackReason(prepared card.PreparedLarkCard) string {
	if r.journal == nil {
		return "journal_missing"
	}
	if r.snapshot == nil {
		return "snapshot_missing"
	}
	if r.snapshot.nativeDisabled {
		return "snapshot_native_disabled"
	}
	if r.interactionDepth != 0 {
		return "interaction_in_progress"
	}
	if !validRenderBinding(r.binding) {
		return "binding_invalid"
	}
	if !prepared.EventCopy().Streaming {
		return "not_streaming"
	}
	if !r.snapshot.prepared.NativeReady() {
		return "snapshot_not_native_ready"
	}
	if !prepared.NativeReady() {
		return "prepared_not_native_ready"
	}
	if r.snapshot.staticFingerprint != staticFingerprint(prepared) {
		return "static_fingerprint_changed"
	}
	if prepared.Answer() == r.snapshot.prepared.Answer() {
		return "answer_unchanged"
	}
	return ""
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
	// 运行期允许滞后的字段(meta 底栏的 token/model)不进指纹:它们逐帧变化,
	// 若参与指纹会让正文流式退回全卡刷新。这些字段在终态全卡更新时统一校正。
	blankVolatileMeta(payload)
	encoded, _ := json.Marshal(payload)
	return sha256.Sum256(encoded)
}

// blankVolatileMeta 清空 meta 底栏两行的 content,使其运行期变化不影响静态指纹。
func blankVolatileMeta(value any) {
	switch node := value.(type) {
	case map[string]any:
		if node["tag"] == "markdown" {
			if id, _ := node["element_id"].(string); id == "meta_primary" || id == "meta_runtime" {
				node["content"] = ""
			}
		}
		for _, child := range node {
			blankVolatileMeta(child)
		}
	case []any:
		for _, child := range node {
			blankVolatileMeta(child)
		}
	}
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
