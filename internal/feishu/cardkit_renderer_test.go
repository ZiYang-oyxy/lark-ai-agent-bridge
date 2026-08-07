package feishu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/session"
)

type fakeCardKitClient struct {
	created     int
	replied     int
	updated     int
	lastCard    map[string]any
	createReqs  []CardKitCreateRequest
	replyUUIDs  []string
	replyReqs   []CardKitReplyRequest
	nextCardSeq int
	updateReqs  []CardKitUpdateCardRequest
	replyResult CardKitReplyResult
	replyErr    error
	updateErr   error
	updateErrs  []error
	elementReqs []CardKitUpdateElementContentRequest
	elementErr  error
}

type blockingCardKitClient struct{}

func (blockingCardKitClient) CreateCard(ctx context.Context, _ CardKitCreateRequest) (CardKitCreateResult, error) {
	<-ctx.Done()
	return CardKitCreateResult{}, ctx.Err()
}

func (blockingCardKitClient) ReplyCard(ctx context.Context, _ CardKitReplyRequest) (CardKitReplyResult, error) {
	<-ctx.Done()
	return CardKitReplyResult{}, ctx.Err()
}

func (blockingCardKitClient) UpdateCard(ctx context.Context, _ CardKitUpdateCardRequest) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingCardKitClient) UpdateSettings(ctx context.Context, _ CardKitUpdateSettingsRequest) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingCardKitClient) UpdateElementContent(ctx context.Context, _ CardKitUpdateElementContentRequest) error {
	<-ctx.Done()
	return ctx.Err()
}

type retentionBlockingCardKitClient struct {
	mu            sync.Mutex
	created       int
	updateStarted chan struct{}
	releaseUpdate chan struct{}
	startOnce     sync.Once
}

func newRetentionBlockingCardKitClient() *retentionBlockingCardKitClient {
	return &retentionBlockingCardKitClient{updateStarted: make(chan struct{}), releaseUpdate: make(chan struct{})}
}

func (c *retentionBlockingCardKitClient) CreateCard(context.Context, CardKitCreateRequest) (CardKitCreateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.created++
	return CardKitCreateResult{CardID: fmt.Sprintf("retention-card-%d", c.created)}, nil
}

func (c *retentionBlockingCardKitClient) ReplyCard(context.Context, CardKitReplyRequest) (CardKitReplyResult, error) {
	return CardKitReplyResult{MessageID: "retention-reply"}, nil
}

func (c *retentionBlockingCardKitClient) UpdateCard(context.Context, CardKitUpdateCardRequest) error {
	c.startOnce.Do(func() { close(c.updateStarted) })
	<-c.releaseUpdate
	return nil
}

func (c *retentionBlockingCardKitClient) UpdateSettings(context.Context, CardKitUpdateSettingsRequest) error {
	return nil
}

func (c *retentionBlockingCardKitClient) UpdateElementContent(context.Context, CardKitUpdateElementContentRequest) error {
	return nil
}

type fakeCardKitObserver struct {
	actions []string
	details []string
}

func (f *fakeCardKitObserver) Record(_, action, _ string, detail string) {
	f.actions = append(f.actions, action)
	f.details = append(f.details, detail)
}

func (f *fakeCardKitClient) CreateCard(_ context.Context, req CardKitCreateRequest) (CardKitCreateResult, error) {
	f.created++
	f.createReqs = append(f.createReqs, req)
	f.lastCard = req.Card
	if req.Prepared != nil {
		f.lastCard = req.Prepared.PayloadCopy()
	}
	f.nextCardSeq++
	return CardKitCreateResult{CardID: "card-" + string(rune('0'+f.nextCardSeq))}, nil
}

func (f *fakeCardKitClient) ReplyCard(_ context.Context, req CardKitReplyRequest) (CardKitReplyResult, error) {
	f.replied++
	f.replyUUIDs = append(f.replyUUIDs, req.UUID)
	f.replyReqs = append(f.replyReqs, req)
	if f.replyErr != nil {
		return CardKitReplyResult{}, f.replyErr
	}
	if f.replyResult.MessageID != "" {
		return f.replyResult, nil
	}
	return CardKitReplyResult{MessageID: "msg-1"}, nil
}

func TestCardKitRendererForwardsReplyInThread(t *testing.T) {
	for _, want := range []bool{false, true} {
		t.Run(fmt.Sprintf("reply_in_thread_%t", want), func(t *testing.T) {
			client := &fakeCardKitClient{}
			renderer := NewCardKitRenderer(client, "source-message")
			if err := renderer.Render(card.Event{Type: "stream", SessionID: "claude:chat", ReplyInThread: want}); err != nil {
				t.Fatal(err)
			}
			if len(client.replyReqs) != 1 || client.replyReqs[0].ReplyInThread != want {
				t.Fatalf("reply requests = %#v, want reply_in_thread=%t", client.replyReqs, want)
			}
		})
	}
}

func (f *fakeCardKitClient) UpdateCard(_ context.Context, req CardKitUpdateCardRequest) error {
	f.updated++
	f.updateReqs = append(f.updateReqs, req)
	f.lastCard = req.Card
	if req.Prepared != nil {
		f.lastCard = req.Prepared.PayloadCopy()
	}
	if len(f.updateErrs) > 0 {
		err := f.updateErrs[0]
		f.updateErrs = f.updateErrs[1:]
		return err
	}
	return f.updateErr
}

func TestPreparedAccessorBoundary(t *testing.T) {
	prepared, err := card.PrepareLarkCard(card.Event{Type: "stream", Streaming: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = prepared.EventCopy()
	_ = prepared.PayloadCopy()
	_ = prepared.Answer()
	_ = prepared.NativeReady()
}

func TestCardKitRouterInteractionFenceIsScopedAndIdempotent(t *testing.T) {
	router := NewCardKitRouterRenderer(&fakeCardKitClient{})
	_, err := router.NewStreaming(context.Background(), "session", "message")
	if err != nil {
		t.Fatal(err)
	}
	router.mu.Lock()
	concrete := router.renderers["session"].renderer
	router.mu.Unlock()
	release := router.BeginCardInteraction("session")
	concrete.mu.Lock()
	if concrete.interactionDepth != 1 {
		t.Fatalf("interactionDepth=%d", concrete.interactionDepth)
	}
	concrete.mu.Unlock()
	release()
	release()
	concrete.mu.Lock()
	defer concrete.mu.Unlock()
	if concrete.interactionDepth != 0 {
		t.Fatalf("interactionDepth=%d after release", concrete.interactionDepth)
	}
}

func TestCardKitRendererPreparesOversizedCreateAndUpdate(t *testing.T) {
	client := &fakeCardKitClient{}
	renderer := NewCardKitRenderer(client, "message-1")
	oversized := card.Event{Type: "stream", SessionID: "session", Segments: []card.Segment{{Kind: card.SegmentText, Text: strings.Repeat("界", card.LarkCardSoftMaxJSONBytes)}}}
	if err := renderer.Render(oversized); err != nil {
		t.Fatalf("create render error: %v", err)
	}
	if err := renderer.Render(oversized); err != nil {
		t.Fatalf("update render error: %v", err)
	}
	if client.createReqs[0].Prepared == nil || client.updateReqs[0].Prepared == nil {
		t.Fatalf("renderer bypassed prepared boundary: create=%#v update=%#v", client.createReqs[0], client.updateReqs[0])
	}
	for _, prepared := range []*card.PreparedLarkCard{client.createReqs[0].Prepared, client.updateReqs[0].Prepared} {
		if got := prepared.Capacity(); got.JSONBytes > card.LarkCardSoftMaxJSONBytes || got.Components > card.LarkCardMaxComponents {
			t.Fatalf("prepared capacity = %#v", got)
		}
	}
}

func TestCardKitRendererRetriesLocalCapacityRejectionWithEmergencyAtSameSequence(t *testing.T) {
	client := &fakeCardKitClient{updateErrs: []error{card.ErrCardPayloadOversize}}
	renderer := NewCardKitRenderer(client, "message-1")
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "session"}); err != nil {
		t.Fatalf("create render error: %v", err)
	}
	if err := renderer.Render(card.Event{Type: "result", SessionID: "session", Segments: []card.Segment{{Kind: card.SegmentText, Text: "answer"}}}); err != nil {
		t.Fatalf("update render error: %v", err)
	}
	if len(client.updateReqs) != 2 || client.updateReqs[0].Sequence != 1 || client.updateReqs[1].Sequence != 1 {
		t.Fatalf("update requests = %#v, want two retries at sequence 1", client.updateReqs)
	}
	if client.updateReqs[1].Prepared == nil || client.updateReqs[1].Prepared.NativeReady() {
		t.Fatalf("retry prepared = %#v, want emergency card", client.updateReqs[1].Prepared)
	}
	if ref := renderer.RenderRef(); ref.Version != 1 {
		t.Fatalf("render ref = %#v, want version 1", ref)
	}
}

func (f *fakeCardKitClient) UpdateSettings(context.Context, CardKitUpdateSettingsRequest) error {
	return nil
}

func (f *fakeCardKitClient) UpdateElementContent(_ context.Context, req CardKitUpdateElementContentRequest) error {
	f.elementReqs = append(f.elementReqs, req)
	return f.elementErr
}

func TestCardKitRendererRenderHasBoundedRequestContext(t *testing.T) {
	previous := cardKitRenderTimeout
	cardKitRenderTimeout = 20 * time.Millisecond
	t.Cleanup(func() { cardKitRenderTimeout = previous })

	renderer := NewCardKitRenderer(blockingCardKitClient{}, "source-message")
	started := time.Now()
	err := renderer.Render(card.Event{Type: "stream", SessionID: "run"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("render error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded render took %s", elapsed)
	}
}

type fakeNativeJournal struct {
	prepared, confirmed, aborted     []NativeSequenceIntent
	prepareErr, confirmErr, abortErr error
}

func (f *fakeNativeJournal) PrepareNative(_ context.Context, intent NativeSequenceIntent) error {
	f.prepared = append(f.prepared, intent)
	return f.prepareErr
}
func (f *fakeNativeJournal) ConfirmNative(_ context.Context, intent NativeSequenceIntent) error {
	f.confirmed = append(f.confirmed, intent)
	return f.confirmErr
}
func (f *fakeNativeJournal) AbortNative(_ context.Context, intent NativeSequenceIntent) error {
	f.aborted = append(f.aborted, intent)
	return f.abortErr
}

type strictSequenceCardKitServer struct {
	mu sync.Mutex

	created int
	replied int

	lastAccepted       int
	updateCardReqs     []CardKitUpdateCardRequest
	elementContentReqs []CardKitUpdateElementContentRequest
	elementAfterAccept error
	replyHook          func()
}

func (s *strictSequenceCardKitServer) CreateCard(_ context.Context, _ CardKitCreateRequest) (CardKitCreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created++
	return CardKitCreateResult{CardID: fmt.Sprintf("strict-card-%d", s.created)}, nil
}

func (s *strictSequenceCardKitServer) ReplyCard(_ context.Context, _ CardKitReplyRequest) (CardKitReplyResult, error) {
	s.mu.Lock()
	hook := s.replyHook
	s.replied++
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return CardKitReplyResult{MessageID: "strict-reply"}, nil
}

func (s *strictSequenceCardKitServer) UpdateCard(_ context.Context, req CardKitUpdateCardRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Sequence != s.lastAccepted+1 {
		return fmt.Errorf("strict full sequence=%d, want %d", req.Sequence, s.lastAccepted+1)
	}
	s.lastAccepted = req.Sequence
	s.updateCardReqs = append(s.updateCardReqs, req)
	return nil
}

func (s *strictSequenceCardKitServer) UpdateSettings(context.Context, CardKitUpdateSettingsRequest) error {
	return nil
}

func (s *strictSequenceCardKitServer) UpdateElementContent(_ context.Context, req CardKitUpdateElementContentRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Sequence != s.lastAccepted+1 {
		return fmt.Errorf("strict element sequence=%d, want %d", req.Sequence, s.lastAccepted+1)
	}
	s.lastAccepted = req.Sequence
	s.elementContentReqs = append(s.elementContentReqs, req)
	return s.elementAfterAccept
}

type controlledRendererClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *controlledRendererClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *controlledRendererClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func TestCardKitRouterEvictsOldestTerminalRendererOverLimit(t *testing.T) {
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	clock := &controlledRendererClock{now: now}
	router := newCardKitRouterRendererWithRetention(&fakeCardKitClient{}, nil, nil, clock.Now, 1, 24*time.Hour)

	first, err := router.NewStreaming(t.Context(), "run-1", "message-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Render(card.Event{Type: "result", SessionID: "run-1"}); err != nil {
		t.Fatal(err)
	}

	clock.Set(now.Add(time.Minute))
	second, err := router.NewStreaming(t.Context(), "run-2", "message-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Render(card.Event{Type: "result", SessionID: "run-2"}); err != nil {
		t.Fatal(err)
	}

	if got := router.ActiveCards(); got != 1 {
		t.Fatalf("active cards = %d, want 1", got)
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if _, ok := router.renderers["run-1"]; ok {
		t.Fatal("oldest terminal renderer was retained")
	}
	if _, ok := router.renderers["run-2"]; !ok {
		t.Fatal("newest terminal renderer was evicted")
	}
}

func TestCardKitRouterRetentionNeverEvictsActiveRenderer(t *testing.T) {
	now := time.Date(2026, time.August, 6, 11, 0, 0, 0, time.UTC)
	clock := &controlledRendererClock{now: now}
	router := newCardKitRouterRendererWithRetention(&fakeCardKitClient{}, nil, nil, clock.Now, 1, 24*time.Hour)

	activeOne, err := router.NewStreaming(t.Context(), "active-1", "message-active-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := activeOne.Render(card.Event{Type: "stream", SessionID: "active-1"}); err != nil {
		t.Fatal(err)
	}
	terminalOne, err := router.NewStreaming(t.Context(), "terminal-1", "message-terminal-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := terminalOne.Render(card.Event{Type: "result", SessionID: "terminal-1"}); err != nil {
		t.Fatal(err)
	}

	clock.Set(now.Add(time.Minute))
	activeTwo, err := router.NewStreaming(t.Context(), "active-2", "message-active-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := activeTwo.Render(card.Event{Type: "stream", SessionID: "active-2"}); err != nil {
		t.Fatal(err)
	}
	terminalTwo, err := router.NewStreaming(t.Context(), "terminal-2", "message-terminal-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := terminalTwo.Render(card.Event{Type: "result", SessionID: "terminal-2"}); err != nil {
		t.Fatal(err)
	}

	router.mu.Lock()
	defer router.mu.Unlock()
	for _, key := range []string{"active-1", "active-2", "terminal-2"} {
		if _, ok := router.renderers[key]; !ok {
			t.Fatalf("renderer %q was evicted", key)
		}
	}
	if _, ok := router.renderers["terminal-1"]; ok {
		t.Fatal("oldest terminal renderer was retained")
	}
}

func TestCardKitRouterRetentionDefersInteractionEvictionUntilRelease(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	clock := &controlledRendererClock{now: now}
	router := newCardKitRouterRendererWithRetention(&fakeCardKitClient{}, nil, nil, clock.Now, 10, time.Hour)

	renderer, err := router.NewStreaming(t.Context(), "terminal", "message")
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.Render(card.Event{Type: "result", SessionID: "terminal"}); err != nil {
		t.Fatal(err)
	}
	release := router.BeginCardInteraction("terminal")
	clock.Set(now.Add(2 * time.Hour))
	if _, err := router.NewStreaming(t.Context(), "sweep-trigger", "message-2"); err != nil {
		t.Fatal(err)
	}

	router.mu.Lock()
	_, retainedDuringInteraction := router.renderers["terminal"]
	router.mu.Unlock()
	if !retainedDuringInteraction {
		t.Fatal("terminal renderer was evicted during interaction")
	}

	release()
	router.mu.Lock()
	_, retainedAfterRelease := router.renderers["terminal"]
	router.mu.Unlock()
	if retainedAfterRelease {
		t.Fatal("expired terminal renderer was retained after interaction release")
	}
}

func TestCardKitRouterFailedTerminalRenderRemainsActive(t *testing.T) {
	now := time.Date(2026, time.August, 6, 13, 0, 0, 0, time.UTC)
	clock := &controlledRendererClock{now: now}
	client := &fakeCardKitClient{updateErr: errors.New("update failed")}
	router := newCardKitRouterRendererWithRetention(client, nil, nil, clock.Now, 1, 24*time.Hour)
	failed := router.Rehydrate("failed", session.RenderRef{CardID: "card-failed", ReplyMessageID: "reply-failed", Version: 1})
	if err := failed.Render(card.Event{Type: "result", SessionID: "failed"}); err == nil {
		t.Fatal("terminal render error = nil")
	}

	client.updateErr = nil
	for i, key := range []string{"terminal-1", "terminal-2"} {
		clock.Set(now.Add(time.Duration(i+1) * time.Minute))
		renderer, err := router.NewStreaming(t.Context(), key, "message-"+key)
		if err != nil {
			t.Fatal(err)
		}
		if err := renderer.Render(card.Event{Type: "result", SessionID: key}); err != nil {
			t.Fatal(err)
		}
	}

	router.mu.Lock()
	defer router.mu.Unlock()
	entry := router.renderers["failed"]
	if entry == nil {
		t.Fatal("failed terminal renderer was evicted")
	}
	if entry.terminal {
		t.Fatal("failed terminal render was marked terminal")
	}
}

func TestCardKitRouterTrackedRendererPreservesRenderRef(t *testing.T) {
	now := time.Date(2026, time.August, 6, 14, 0, 0, 0, time.UTC)
	clock := &controlledRendererClock{now: now}
	server := &strictSequenceCardKitServer{}
	router := newCardKitRouterRendererWithRetention(server, nil, nil, clock.Now, 4, 24*time.Hour)
	renderer, err := router.NewStreaming(t.Context(), "run", "source")
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true, SessionID: "run"}); err != nil {
		t.Fatal(err)
	}
	first := renderer.RenderRef()
	if first.CardID == "" || first.ReplyMessageID == "" {
		t.Fatalf("initial RenderRef = %#v, want card and reply ids", first)
	}
	if !first.CreatedAt.Equal(now) {
		t.Fatalf("initial CreatedAt = %s, want %s", first.CreatedAt, now)
	}

	if err := renderer.Render(card.Event{Type: "result", SessionID: "run"}); err != nil {
		t.Fatal(err)
	}
	terminal := renderer.RenderRef()
	if terminal.CardID != first.CardID || terminal.ReplyMessageID != first.ReplyMessageID {
		t.Fatalf("terminal RenderRef = %#v, want ids from %#v", terminal, first)
	}
	if terminal.Version != first.Version+1 {
		t.Fatalf("terminal version = %d, want %d", terminal.Version, first.Version+1)
	}
	if !terminal.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("terminal CreatedAt = %s, want %s", terminal.CreatedAt, first.CreatedAt)
	}
}

func TestCardKitRouterTrackedRendererPreservesRenderContext(t *testing.T) {
	router := NewCardKitRouterRenderer(blockingCardKitClient{})
	renderer := router.Rehydrate("recovery", session.RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 1})
	contextRenderer, ok := renderer.(ContextRenderer)
	if !ok {
		t.Fatalf("rehydrated renderer type %T does not implement ContextRenderer", renderer)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := contextRenderer.RenderContext(ctx, card.Event{Type: "interrupted", SessionID: "recovery"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderContext error = %v, want context canceled", err)
	}
}

func TestCardKitRouterRetentionConcurrentRenderAndInteraction(t *testing.T) {
	now := time.Date(2026, time.August, 6, 15, 0, 0, 0, time.UTC)
	clock := &controlledRendererClock{now: now}
	server := &strictSequenceCardKitServer{}
	router := newCardKitRouterRendererWithRetention(server, nil, nil, clock.Now, 2, 24*time.Hour)
	active, err := router.NewStreaming(t.Context(), "active", "source")
	if err != nil {
		t.Fatal(err)
	}
	if err := active.Render(card.Event{Type: "stream", Streaming: true, SessionID: "active"}); err != nil {
		t.Fatal(err)
	}

	const callers = 12
	errCh := make(chan error, callers*2)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			errCh <- active.Render(card.Event{Type: "stream", Streaming: true, SessionID: "active", Segments: []card.Segment{{Kind: card.SegmentText, Text: fmt.Sprintf("frame-%d", i)}}})
		}(i)
		go func(i int) {
			defer wg.Done()
			release := router.BeginCardInteraction("active")
			release()
			terminal, createErr := router.NewStreaming(t.Context(), fmt.Sprintf("terminal-%d", i), fmt.Sprintf("source-%d", i))
			if createErr != nil {
				errCh <- createErr
				return
			}
			errCh <- terminal.Render(card.Event{Type: "result", SessionID: fmt.Sprintf("terminal-%d", i)})
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	router.mu.Lock()
	_, activeRetained := router.renderers["active"]
	router.mu.Unlock()
	if !activeRetained {
		t.Fatal("active renderer was evicted during concurrent retention activity")
	}
}

func TestCardKitRouterRetentionDoesNotEvictTerminalRendererWhileItRendersAgain(t *testing.T) {
	now := time.Date(2026, time.August, 6, 16, 0, 0, 0, time.UTC)
	clock := &controlledRendererClock{now: now}
	client := newRetentionBlockingCardKitClient()
	router := newCardKitRouterRendererWithRetention(client, nil, nil, clock.Now, 1, 24*time.Hour)
	first, err := router.NewStreaming(t.Context(), "first", "source-first")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Render(card.Event{Type: "result", SessionID: "first"}); err != nil {
		t.Fatal(err)
	}

	renderDone := make(chan error, 1)
	go func() {
		renderDone <- first.Render(card.Event{Type: "stream", Streaming: true, SessionID: "first"})
	}()
	<-client.updateStarted

	clock.Set(now.Add(time.Minute))
	second, err := router.NewStreaming(t.Context(), "second", "source-second")
	if err != nil {
		close(client.releaseUpdate)
		t.Fatal(err)
	}
	if err := second.Render(card.Event{Type: "result", SessionID: "second"}); err != nil {
		close(client.releaseUpdate)
		t.Fatal(err)
	}
	router.mu.Lock()
	_, retained := router.renderers["first"]
	router.mu.Unlock()
	close(client.releaseUpdate)
	if err := <-renderDone; err != nil {
		t.Fatal(err)
	}
	if !retained {
		t.Fatal("renderer was evicted while a new render was in progress")
	}
}

func TestCardKitRouterDirectRenderPinsTerminalRendererBeforeCapacitySweep(t *testing.T) {
	now := time.Date(2026, time.August, 6, 17, 0, 0, 0, time.UTC)
	clock := &controlledRendererClock{now: now}
	client := newRetentionBlockingCardKitClient()
	router := newCardKitRouterRendererWithRetention(client, nil, nil, clock.Now, 1, 24*time.Hour)
	if err := router.Render(card.Event{Type: "result", SessionID: "first", ReplyToMessageID: "source-first"}); err != nil {
		t.Fatal(err)
	}

	renderDone := make(chan error, 1)
	go func() {
		renderDone <- router.Render(card.Event{Type: "stream", Streaming: true, SessionID: "first"})
	}()
	<-client.updateStarted

	clock.Set(now.Add(time.Minute))
	if err := router.Render(card.Event{Type: "result", SessionID: "second", ReplyToMessageID: "source-second"}); err != nil {
		close(client.releaseUpdate)
		t.Fatal(err)
	}
	router.mu.Lock()
	_, retained := router.renderers["first"]
	router.mu.Unlock()
	close(client.releaseUpdate)
	if err := <-renderDone; err != nil {
		t.Fatal(err)
	}
	if !retained {
		t.Fatal("direct Render entry was evicted after lookup but before it was pinned")
	}
}

func TestCardKitRouterRetentionFollowsResultThenStreamCompletionOrder(t *testing.T) {
	now := time.Date(2026, time.August, 6, 18, 0, 0, 0, time.UTC)
	clock := &controlledRendererClock{now: now}
	client := newRetentionBlockingCardKitClient()
	router := newCardKitRouterRendererWithRetention(client, nil, nil, clock.Now, 4, 24*time.Hour)
	renderer, err := router.NewStreaming(t.Context(), "run", "source")
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true, SessionID: "run"}); err != nil {
		t.Fatal(err)
	}

	resultDone := make(chan error, 1)
	go func() {
		resultDone <- renderer.Render(card.Event{Type: "result", SessionID: "run"})
	}()
	<-client.updateStarted
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- renderer.Render(card.Event{Type: "stream", Streaming: true, SessionID: "run"})
	}()
	close(client.releaseUpdate)
	if err := <-resultDone; err != nil {
		t.Fatal(err)
	}
	if err := <-streamDone; err != nil {
		t.Fatal(err)
	}

	router.mu.Lock()
	entry := router.renderers["run"]
	router.mu.Unlock()
	if entry == nil {
		t.Fatal("renderer was evicted after trailing stream frame")
	}
	if entry.terminal {
		t.Fatal("trailing stream frame was overwritten by an earlier result completion")
	}
}

func TestCardKitRendererCreatedAtUsesCreateSuccessClockAndSurvivesLaterPaths(t *testing.T) {
	createdAt := time.Date(2026, time.July, 18, 9, 30, 0, 0, time.FixedZone("create", -7*60*60))
	replyCompletedAt := createdAt.Add(11 * time.Minute)
	nextCreatedAt := replyCompletedAt.Add(9 * time.Minute)
	clock := &controlledRendererClock{now: createdAt}
	server := &strictSequenceCardKitServer{}
	router := newCardKitRouterRendererWithClock(server, nil, nil, clock.Now)
	renderer, err := router.NewStreaming(context.Background(), "run", "source")
	if err != nil {
		t.Fatal(err)
	}
	var beforeReply session.RenderRef
	router.mu.Lock()
	concrete := router.renderers["run"].renderer
	router.mu.Unlock()
	server.replyHook = func() {
		beforeReply = session.RenderRef{CardID: concrete.cardID, ReplyMessageID: concrete.replyMessageID, CreatedAt: concrete.createdAt}
		clock.Set(replyCompletedAt)
	}
	first := card.Event{Type: "stream", Streaming: true, SessionID: "run", Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}}}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	if beforeReply != (session.RenderRef{}) {
		t.Fatalf("RenderRef during ReplyCard = %#v, want empty", beforeReply)
	}
	ref := renderer.RenderRef()
	if !ref.CreatedAt.Equal(createdAt.UTC()) || ref.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt = %s, want create-success time %s", ref.CreatedAt, createdAt.UTC())
	}

	second := first
	second.Segments = []card.Segment{{Kind: card.SegmentText, Text: "two"}}
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	advanced := renderer.RenderRef()
	if advanced.Version != ref.Version+1 || !advanced.CreatedAt.Equal(ref.CreatedAt) {
		t.Fatalf("advanced ref = %#v, want version %d and CreatedAt %s", advanced, ref.Version+1, ref.CreatedAt)
	}

	rehydrated := newCardKitRouterRendererWithClock(server, nil, nil, clock.Now).Rehydrate("rehydrated", advanced)
	if err := rehydrated.Render(card.Event{Type: "result", SessionID: "rehydrated", Segments: second.Segments}); err != nil {
		t.Fatal(err)
	}
	if got := rehydrated.RenderRef(); !got.CreatedAt.Equal(createdAt.UTC()) {
		t.Fatalf("rehydrated ref = %#v, want CreatedAt %s", got, createdAt.UTC())
	}

	clock.Set(nextCreatedAt)
	next, err := newCardKitRouterRendererWithClock(server, nil, nil, clock.Now).NewStreaming(context.Background(), "next", "source-next")
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Render(card.Event{Type: "stream", Streaming: true, SessionID: "next"}); err != nil {
		t.Fatal(err)
	}
	if got := next.RenderRef().CreatedAt; !got.Equal(nextCreatedAt.UTC()) {
		t.Fatalf("next CreatedAt = %s, want %s", got, nextCreatedAt.UTC())
	}

	failure, err := newCardKitRouterRendererWithClock(&fakeCardKitClient{replyErr: errors.New("reply failed")}, nil, nil, clock.Now).NewStreaming(context.Background(), "failed", "source-failed")
	if err != nil {
		t.Fatal(err)
	}
	if err := failure.Render(card.Event{Type: "stream", Streaming: true, SessionID: "failed"}); err == nil {
		t.Fatal("Render() error = nil, want reply failure")
	}
	if got := failure.RenderRef(); got != (session.RenderRef{}) {
		t.Fatalf("reply failure exposed partial ref %#v", got)
	}
}

func TestCardKitRendererStrictServerSharesSequenceAcrossConcurrentNativeAndFullUpdates(t *testing.T) {
	server := &strictSequenceCardKitServer{}
	renderer := NewCardKitRendererWithNative(server, "source", nil, RenderBinding{
		BaseSessionID: "base", BatchID: "batch", LatestScope: "base", RunCardSessionID: "run",
	}, &fakeNativeJournal{})
	seed := card.Event{Type: "stream", Streaming: true, SessionID: "run", Segments: []card.Segment{
		{Kind: card.SegmentText, Text: "seed"}, {Kind: card.SegmentThought, Text: "keep-static"},
	}}
	if err := renderer.Render(seed); err != nil {
		t.Fatal(err)
	}

	const callers = 12
	runConcurrent := func(event func(int) card.Event) {
		t.Helper()
		errs := make(chan error, callers)
		var wg sync.WaitGroup
		for i := range callers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs <- renderer.Render(event(i))
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	runConcurrent(func(i int) card.Event {
		return card.Event{Type: "stream", Streaming: true, SessionID: "run", Segments: []card.Segment{
			{Kind: card.SegmentText, Text: fmt.Sprintf("native-%d", i)}, {Kind: card.SegmentThought, Text: "keep-static"},
		}}
	})
	runConcurrent(func(i int) card.Event {
		return card.Event{Type: "result", SessionID: "run", Message: fmt.Sprintf("result-%d", i), Segments: []card.Segment{{Kind: card.SegmentText, Text: "done"}}}
	})

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.elementContentReqs) != callers || len(server.updateCardReqs) != callers {
		t.Fatalf("native/full calls = %d/%d, want %d/%d", len(server.elementContentReqs), len(server.updateCardReqs), callers, callers)
	}
	for i, req := range server.elementContentReqs {
		if req.Sequence != i+1 || req.ElementID != "answer" || req.Content == "" {
			t.Fatalf("native request %d = %#v", i, req)
		}
	}
	for i, req := range server.updateCardReqs {
		if req.Sequence != callers+i+1 || req.Prepared == nil {
			t.Fatalf("full request %d = %#v", i, req)
		}
	}
	if server.lastAccepted != callers*2 {
		t.Fatalf("strict server last sequence = %d, want %d", server.lastAccepted, callers*2)
	}
}

func TestCardKitRendererStrictServerDeliveryUnknownBlocksOldCardWrites(t *testing.T) {
	server := &strictSequenceCardKitServer{elementAfterAccept: errors.New("response lost after apply")}
	renderer := NewCardKitRendererWithNative(server, "source", nil, RenderBinding{
		BaseSessionID: "base", BatchID: "batch", LatestScope: "base", RunCardSessionID: "run",
	}, &fakeNativeJournal{})
	first := card.Event{Type: "stream", Streaming: true, SessionID: "run", Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}}}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Segments = []card.Segment{{Kind: card.SegmentText, Text: "two"}}
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Render(card.Event{Type: "result", SessionID: "run", Segments: second.Segments}); err != nil {
		t.Fatal(err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.elementContentReqs) != 1 || len(server.updateCardReqs) != 0 || server.lastAccepted != 1 {
		t.Fatalf("old-card writes element/full/last=%d/%d/%d", len(server.elementContentReqs), len(server.updateCardReqs), server.lastAccepted)
	}
	if ref := renderer.RenderRef(); ref.Version != 0 || !ref.SequenceUnknown || ref.PendingSequence != 1 {
		t.Fatalf("unknown ref = %#v", ref)
	}
}

func TestCardKitRendererNativeAnswerAndFullCardShareSequence(t *testing.T) {
	client := &fakeCardKitClient{}
	journal := &fakeNativeJournal{}
	renderer := NewCardKitRendererWithNative(client, "source", nil, RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", LatestScope: "claude:chat", RunCardSessionID: "run-card",
	}, journal)
	base := card.Event{Type: "stream", Streaming: true, SessionID: "run-card", Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}}}
	if err := renderer.Render(base); err != nil {
		t.Fatal(err)
	}
	next := base
	next.Segments = []card.Segment{{Kind: card.SegmentText, Text: "two"}}
	if err := renderer.Render(next); err != nil {
		t.Fatal(err)
	}
	thought := next
	thought.Segments = append(thought.Segments, card.Segment{Kind: card.SegmentThought, Text: "plan"})
	if err := renderer.Render(thought); err != nil {
		t.Fatal(err)
	}
	if len(client.elementReqs) != 1 || client.elementReqs[0].Sequence != 1 || client.elementReqs[0].Content != "two" {
		t.Fatalf("element requests=%#v", client.elementReqs)
	}
	if len(client.updateReqs) != 1 || client.updateReqs[0].Sequence != 2 {
		t.Fatalf("full updates=%#v", client.updateReqs)
	}
	if len(journal.prepared) != 1 || len(journal.confirmed) != 1 || journal.prepared[0].SessionID != "claude:chat" || journal.prepared[0].BatchID != "batch" {
		t.Fatalf("journal prepare=%#v confirm=%#v", journal.prepared, journal.confirmed)
	}
	if ref := renderer.RenderRef(); ref.Version != 2 || ref.SequenceUnknown || ref.PendingSequence != 0 {
		t.Fatalf("render ref=%#v", ref)
	}
}

func TestCardKitRendererNativeAnswerIgnoresVolatileMetaTokens(t *testing.T) {
	// v2 Step3:运行期 answer 增量伴随 meta token 变化时,仍走 native 局部更新,
	// 不因 token(允许滞后的字段)变化退回全卡刷新。
	client := &fakeCardKitClient{}
	renderer := NewCardKitRendererWithNative(client, "source", nil, RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", LatestScope: "claude:chat", RunCardSessionID: "run-card",
	}, &fakeNativeJournal{})
	first := card.Event{
		Type: "stream", Streaming: true, SessionID: "run-card", HeaderTitle: "✍️ 正在回复 · ⏱ 1s",
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}},
		Meta:     card.Meta{Agent: "claude", RunTokens: 10, TotalTokens: 10},
	}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.HeaderTitle = "✍️ 正在回复 · ⏱ 2s"
	second.Segments = []card.Segment{{Kind: card.SegmentText, Text: "one two"}}
	second.Meta = card.Meta{Agent: "claude", RunTokens: 55, TotalTokens: 55}
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	if len(client.elementReqs) != 1 || len(client.updateReqs) != 0 {
		t.Fatalf("meta-token change forced full update: element/full=%d/%d", len(client.elementReqs), len(client.updateReqs))
	}
	if client.elementReqs[0].Content != "one two" {
		t.Fatalf("native content = %q, want streamed answer", client.elementReqs[0].Content)
	}
}

func TestCardKitRendererNativeAnswerIgnoresElapsedHeaderSuffix(t *testing.T) {
	client := &fakeCardKitClient{}
	renderer := NewCardKitRendererWithNative(client, "source", nil, RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", LatestScope: "claude:chat", RunCardSessionID: "run-card",
	}, &fakeNativeJournal{})
	first := card.Event{
		Type: "stream", Streaming: true, SessionID: "run-card", HeaderTitle: "✍️ 正在回复 · ⏱ 1s",
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}},
	}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.HeaderTitle = "✍️ 正在回复 · ⏱ 2s"
	second.Segments = []card.Segment{{Kind: card.SegmentText, Text: "two"}}
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	if len(client.elementReqs) != 1 || len(client.updateReqs) != 0 {
		t.Fatalf("elapsed-only header change wrote element/full=%d/%d", len(client.elementReqs), len(client.updateReqs))
	}
}

func TestCardKitRendererHeartbeatForcesElapsedFullCardUpdate(t *testing.T) {
	client := &fakeCardKitClient{}
	renderer := NewCardKitRendererWithNative(client, "source", nil, RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", LatestScope: "claude:chat", RunCardSessionID: "run-card",
	}, &fakeNativeJournal{})
	first := card.Event{
		Type: "stream", Streaming: true, SessionID: "run-card", HeaderTitle: "🧠 正在推理 · ⏱ 0s", Message: "思考中…",
	}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	heartbeat := first
	heartbeat.HeaderTitle = "🧠 正在推理 · ⏱ 15s"
	heartbeat.Message = "任务仍在运行…"
	heartbeat.ForceFullUpdate = true
	if err := renderer.Render(heartbeat); err != nil {
		t.Fatal(err)
	}
	if len(client.elementReqs) != 0 || len(client.updateReqs) != 1 {
		t.Fatalf("heartbeat wrote element/full=%d/%d, want 0/1", len(client.elementReqs), len(client.updateReqs))
	}
	if got := client.updateReqs[0].Prepared.EventCopy().HeaderTitle; got != heartbeat.HeaderTitle {
		t.Fatalf("heartbeat full-card title = %q, want %q", got, heartbeat.HeaderTitle)
	}
}

// TestCardKitRendererSkipsUnchangedStreamingUpdate 覆盖 Coder+MarkdownLayout 流式期间
// 上游 flushPreview 因 contentRevision/metaRevision 递增反复触发,但 limitPreviewEvent
// 把 segments 裁到 MaxPreviewRunes 后生成同一份 markdown,导致 nativeFallbackReason=
// "answer_unchanged"。此时应跳过 CardKit 更新,不发全卡替换,让飞书客户端不再被无信息量
// 更新淹没(4f8fb9 那次 6m24s 卡里 226 次 update 有 167 次是 answer_unchanged,直接触发
// 客户端节流合并渲染,表现为"卡片停滞到终态才追赶")。同时记 audit 保留可观测性。
func TestCardKitRendererSkipsUnchangedStreamingUpdate(t *testing.T) {
	client := &fakeCardKitClient{}
	observer := &fakeCardKitObserver{}
	renderer := NewCardKitRendererWithNative(client, "source", observer, RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", LatestScope: "claude:chat", RunCardSessionID: "run-card",
	}, &fakeNativeJournal{})
	first := card.Event{
		Type: "stream", Streaming: true, SessionID: "run-card", HeaderTitle: "✍️ 正在回复 · ⏱ 1s",
		MarkdownLayout: true, Markdown: "hello world",
	}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	// 第二帧: title/meta 都没变,markdown 完全一样。这在实际 Coder 流式里就是
	// limitPreviewEvent 把 segments 裁剪后生成同一份 tail markdown 的场景。
	second := first
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	if len(client.elementReqs) != 0 || len(client.updateReqs) != 0 {
		t.Fatalf("unchanged frame leaked to CardKit: element=%d full=%d", len(client.elementReqs), len(client.updateReqs))
	}
	skipped := false
	for i, action := range observer.actions {
		if action == "cardkit_update_skipped" && strings.Contains(observer.details[i], "reason=answer_unchanged") {
			skipped = true
			break
		}
	}
	if !skipped {
		t.Fatalf("expected cardkit_update_skipped audit; got actions=%v", observer.actions)
	}
	// 第三帧: markdown 变化 → 正常走 native fast path
	third := first
	third.Markdown = "hello world updated"
	if err := renderer.Render(third); err != nil {
		t.Fatal(err)
	}
	if len(client.elementReqs) != 1 || len(client.updateReqs) != 0 {
		t.Fatalf("changed frame did not go native: element=%d full=%d", len(client.elementReqs), len(client.updateReqs))
	}
}

// TestCardKitRendererCoderFakeAgentReplayDropsUnchangedFrames 是 fake-agent 级回归用例:
// 复现 4f8fb9 那种 Coder 长流式 preview 场景——上游 agentCardStream.flushPreview 因
// contentRevision 递增反复触发,但 limitPreviewEvent 把 segments 裁到 MaxPreviewRunes 后
// RenderInlineTimelineWithLimit 反复生成同一份 tail markdown。序列里模拟了 10 帧,
// 其中 3 帧 markdown 真变化(打字机式推进)、7 帧内容相同(preview 触发但 markdown 没变),
// 断言跳过逻辑生效: CardKit 只收到 3 次 native fast-path 调用 + 0 次全卡替换,
// 剩余 7 帧被 cardkit_update_skipped audit,飞书客户端不再被同 payload 淹没。
func TestCardKitRendererCoderFakeAgentReplayDropsUnchangedFrames(t *testing.T) {
	client := &fakeCardKitClient{}
	observer := &fakeCardKitObserver{}
	renderer := NewCardKitRendererWithNative(client, "source", observer, RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", LatestScope: "claude:chat", RunCardSessionID: "run-card",
	}, &fakeNativeJournal{})
	// 每帧的 markdown。"" 代表沿用上一帧内容 (limitPreviewEvent 裁掉尾部 delta 后
	// 的稳定 tail);非空代表 markdown 真产生新增量。
	frames := []string{
		"> ✅ Read foo.go",                        // 帧 1:首次内容,应发
		"> ✅ Read foo.go",                        // 帧 2:同 markdown,应跳过
		"> ✅ Read foo.go",                        // 帧 3:同 markdown,应跳过
		"> ✅ Read foo.go\n\n> ⏳ Bash pwd",        // 帧 4:markdown 更新,应发
		"> ✅ Read foo.go\n\n> ⏳ Bash pwd",        // 帧 5:同,应跳过
		"> ✅ Read foo.go\n\n> ⏳ Bash pwd",        // 帧 6:同,应跳过
		"> ✅ Read foo.go\n\n> ⏳ Bash pwd",        // 帧 7:同,应跳过
		"> ✅ Read foo.go\n\n> ✅ Bash pwd — done", // 帧 8:markdown 更新,应发
		"> ✅ Read foo.go\n\n> ✅ Bash pwd — done", // 帧 9:同,应跳过
		"> ✅ Read foo.go\n\n> ✅ Bash pwd — done", // 帧 10:同,应跳过
	}
	base := card.Event{
		Type: "stream", Streaming: true, SessionID: "run-card", HeaderTitle: "✍️ 正在回复 · ⏱ 1s",
		MarkdownLayout: true,
	}
	for i, markdown := range frames {
		frame := base
		frame.Markdown = markdown
		if err := renderer.Render(frame); err != nil {
			t.Fatalf("frame %d render err: %v", i+1, err)
		}
	}
	// 帧 1: CreateCard+ReplyCard (首次,不进 updatePrepared)
	// 帧 4/8: markdown 变化 → 2 次 native fast-path element update
	if len(client.elementReqs) != 2 {
		t.Fatalf("expected 2 native updates for 2 distinct markdown transitions, got %d", len(client.elementReqs))
	}
	if len(client.updateReqs) != 0 {
		t.Fatalf("expected 0 full-card updates during coder streaming, got %d", len(client.updateReqs))
	}
	// 帧 2/3/5/6/7/9/10 = 7 次 answer_unchanged 跳过
	skipCount := 0
	for i, action := range observer.actions {
		if action == "cardkit_update_skipped" && strings.Contains(observer.details[i], "reason=answer_unchanged") {
			skipCount++
		}
	}
	if skipCount != 7 {
		t.Fatalf("expected 7 cardkit_update_skipped audits (frames 2/3/5/6/7/9/10), got %d; actions=%v", skipCount, observer.actions)
	}
}

// TestCardKitRendererHeartbeatBypassesUnchangedSkip 保证跳过逻辑不吞掉 heartbeat。
// heartbeat 显式设 ForceFullUpdate=true 走 full_update_requested 分支,即便 answer 与
// static 都没变也必须发全卡 update,防止长时间静默让飞书客户端认为卡片死了。
func TestCardKitRendererHeartbeatBypassesUnchangedSkip(t *testing.T) {
	client := &fakeCardKitClient{}
	renderer := NewCardKitRendererWithNative(client, "source", nil, RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", LatestScope: "claude:chat", RunCardSessionID: "run-card",
	}, &fakeNativeJournal{})
	first := card.Event{
		Type: "stream", Streaming: true, SessionID: "run-card", HeaderTitle: "🧠 正在推理 · ⏱ 0s",
		MarkdownLayout: true, Markdown: "waiting",
	}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	// heartbeat: 同 markdown + 同 static,但 ForceFullUpdate=true。
	heartbeat := first
	heartbeat.ForceFullUpdate = true
	if err := renderer.Render(heartbeat); err != nil {
		t.Fatal(err)
	}
	if len(client.updateReqs) != 1 {
		t.Fatalf("heartbeat did not force full update: element=%d full=%d", len(client.elementReqs), len(client.updateReqs))
	}
}

func TestCardKitRendererNativeAnswerPreservesHeaderPhaseChanges(t *testing.T) {
	client := &fakeCardKitClient{}
	observer := &fakeCardKitObserver{}
	renderer := NewCardKitRendererWithNative(client, "source", observer, RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", LatestScope: "claude:chat", RunCardSessionID: "run-card",
	}, &fakeNativeJournal{})
	first := card.Event{
		Type: "stream", Streaming: true, SessionID: "run-card", HeaderTitle: "🧠 正在推理 · ⏱ 1s",
		Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}},
	}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.HeaderTitle = "✍️ 正在回复 · ⏱ 2s"
	second.Segments = []card.Segment{{Kind: card.SegmentText, Text: "two"}}
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	if len(client.elementReqs) != 0 || len(client.updateReqs) != 1 {
		t.Fatalf("phase header change wrote element/full=%d/%d", len(client.elementReqs), len(client.updateReqs))
	}
	found := false
	for i, action := range observer.actions {
		if action == "cardkit_native_fallback" && strings.Contains(observer.details[i], "reason=static_fingerprint_changed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("native fallback audit actions/details = %#v / %#v", observer.actions, observer.details)
	}
}

func TestCardKitRendererNativeDeliveryUnknownStopsRunningWritesButRecoversTerminal(t *testing.T) {
	// v2 R1:native 丢包进入 sequenceUnknown 后,运行期不再续写旧卡(不产生新的 element/full 更新),
	// 但终态必须显式补偿——新建一张终态卡(Create+Reply),不静默停在运行态。
	client := &fakeCardKitClient{elementErr: errors.New("response lost")}
	journal := &fakeNativeJournal{}
	renderer := NewCardKitRendererWithNative(client, "source", nil, RenderBinding{BaseSessionID: "base", BatchID: "batch", RunCardSessionID: "run"}, journal)
	first := card.Event{Type: "stream", Streaming: true, SessionID: "run", Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}}}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	createsAfterFirst := client.created
	second := first
	second.Segments = []card.Segment{{Kind: card.SegmentText, Text: "two"}}
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	// 运行期第二帧只尝试一次 native(失败),不产生全卡更新,也不新建卡。
	if len(client.elementReqs) != 1 || len(client.updateReqs) != 0 || client.created != createsAfterFirst {
		t.Fatalf("running writes leaked: element=%d full=%d creates=%d", len(client.elementReqs), len(client.updateReqs), client.created)
	}
	terminal := card.Event{Type: "result", SessionID: "run", Segments: second.Segments}
	if err := renderer.Render(terminal); err != nil {
		t.Fatal(err)
	}
	// 终态补偿:新建了一张卡片(Create+Reply 各多一次),而不是静默返回。
	if client.created != createsAfterFirst+1 || len(client.replyReqs) != 2 {
		t.Fatalf("terminal recovery missing: creates=%d replies=%d", client.created, len(client.replyReqs))
	}
}

func TestCardKitRendererNativeInteractionAbortKeepsSequenceReusable(t *testing.T) {
	client := &fakeCardKitClient{elementErr: ErrCardInteractionInProgress}
	journal := &fakeNativeJournal{}
	renderer := NewCardKitRendererWithNative(client, "source", nil, RenderBinding{BaseSessionID: "base", BatchID: "batch", RunCardSessionID: "run"}, journal)
	first := card.Event{Type: "stream", Streaming: true, SessionID: "run", Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}}}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Segments = []card.Segment{{Kind: card.SegmentText, Text: "two"}}
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	client.elementErr = nil
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	if len(journal.aborted) != 1 || len(client.elementReqs) != 2 || client.elementReqs[0].Sequence != 1 || client.elementReqs[1].Sequence != 1 {
		t.Fatalf("abort=%#v requests=%#v", journal.aborted, client.elementReqs)
	}
	if ref := renderer.RenderRef(); ref.Version != 1 || ref.SequenceUnknown {
		t.Fatalf("ref=%#v", ref)
	}
}

func TestCardKitRendererNativeConfirmFailureStaysUnknown(t *testing.T) {
	client := &fakeCardKitClient{}
	journal := &fakeNativeJournal{confirmErr: errors.New("persist failed")}
	observer := &fakeCardKitObserver{}
	renderer := NewCardKitRendererWithNative(client, "source", observer, RenderBinding{BaseSessionID: "base", BatchID: "batch", RunCardSessionID: "run"}, journal)
	first := card.Event{Type: "stream", Streaming: true, SessionID: "run", Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}}}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Segments = []card.Segment{{Kind: card.SegmentText, Text: "two"}}
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	if ref := renderer.RenderRef(); ref.Version != 0 || !ref.SequenceUnknown || ref.PendingSequence != 1 {
		t.Fatalf("ref=%#v", ref)
	}
	if len(observer.details) == 0 || !strings.Contains(observer.details[len(observer.details)-1], "error=persist failed") {
		t.Fatalf("audit details=%#v, want confirm error", observer.details)
	}
}

func TestCardKitRendererNativePrepareFailureFailsClosed(t *testing.T) {
	client := &fakeCardKitClient{}
	journal := &fakeNativeJournal{prepareErr: errors.New("intent persistence uncertain")}
	renderer := NewCardKitRendererWithNative(client, "source", nil, RenderBinding{BaseSessionID: "base", BatchID: "batch", RunCardSessionID: "run"}, journal)
	first := card.Event{Type: "stream", Streaming: true, SessionID: "run", Segments: []card.Segment{{Kind: card.SegmentText, Text: "one"}}}
	if err := renderer.Render(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Segments = []card.Segment{{Kind: card.SegmentText, Text: "two"}}
	if err := renderer.Render(second); err != nil {
		t.Fatal(err)
	}
	if len(client.elementReqs) != 0 || len(client.updateReqs) != 0 {
		t.Fatalf("client writes element/full=%d/%d", len(client.elementReqs), len(client.updateReqs))
	}
	if ref := renderer.RenderRef(); !ref.SequenceUnknown || ref.PendingSequence != 1 || ref.Version != 0 {
		t.Fatalf("ref=%#v", ref)
	}
}

func TestCardKitRouterRehydratePreservesSequenceUnknownAndSuppressesWrites(t *testing.T) {
	client := &fakeCardKitClient{}
	ref := session.RenderRef{CardID: "card", ReplyMessageID: "reply", Version: 7, SequenceUnknown: true, PendingSequence: 8}
	renderer := NewCardKitRouterRenderer(client).Rehydrate("run", ref)
	if err := renderer.Render(card.Event{Type: "result", SessionID: "run"}); err != nil {
		t.Fatal(err)
	}
	if len(client.updateReqs) != 0 || len(client.elementReqs) != 0 {
		t.Fatalf("rehydrated unknown wrote full/native: %d/%d", len(client.updateReqs), len(client.elementReqs))
	}
	if got := renderer.RenderRef(); got.Version != 7 || !got.SequenceUnknown || got.PendingSequence != 8 {
		t.Fatalf("rehydrated ref=%#v", got)
	}
}

func TestCardKitRendererCreateReplyThenUpdate(t *testing.T) {
	client := &fakeCardKitClient{}
	renderer := NewCardKitRenderer(client, "message-1")
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "claude:chat", Segments: []card.Segment{{Kind: card.SegmentText, Text: "hello"}}}); err != nil {
		t.Fatalf("first render error: %v", err)
	}
	if err := renderer.Render(card.Event{Type: "workdir_confirm", SessionID: "claude:chat", Actions: card.WorkDirCreateActions("/tmp/work")}); err != nil {
		t.Fatalf("second render error: %v", err)
	}
	if client.created != 1 || client.replied != 1 || client.updated != 1 {
		t.Fatalf("created/replied/updated = %d/%d/%d", client.created, client.replied, client.updated)
	}
}

func TestCardKitRouterRendererRenderRefRehydratesWithIncreasingSequence(t *testing.T) {
	client := &fakeCardKitClient{replyResult: CardKitReplyResult{MessageID: "actual-reply-42"}}
	router := NewCardKitRouterRenderer(client)
	renderer, err := router.NewStreaming(context.Background(), "claude:chat", "source-message")
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"stream", "stream", "result"} {
		if err := renderer.Render(card.Event{Type: eventType, SessionID: "claude:chat"}); err != nil {
			t.Fatal(err)
		}
	}
	ref := renderer.RenderRef()
	if ref.CardID != "card-1" || ref.ReplyMessageID != "actual-reply-42" || ref.Version != 2 || ref.CreatedAt.IsZero() {
		t.Fatalf("render ref = %#v", ref)
	}

	restarted := NewCardKitRouterRenderer(client)
	rehydrated := restarted.Rehydrate("claude:chat", ref)
	if err := rehydrated.Render(card.Event{Type: "result", SessionID: "claude:chat"}); err != nil {
		t.Fatal(err)
	}
	if client.created != 1 || client.replied != 1 || len(client.updateReqs) != 3 {
		t.Fatalf("created/replied/updates = %d/%d/%d", client.created, client.replied, len(client.updateReqs))
	}
	last := client.updateReqs[len(client.updateReqs)-1]
	if last.CardID != "card-1" || last.Sequence != 3 {
		t.Fatalf("rehydrated update = %#v", last)
	}
	if got := rehydrated.RenderRef(); !got.CreatedAt.Equal(ref.CreatedAt) {
		t.Fatalf("rehydrated ref = %#v, want CreatedAt %s", got, ref.CreatedAt)
	}
}

func TestCardKitRendererCapturesCreatedAtOnlyAfterReplyBinding(t *testing.T) {
	client := &fakeCardKitClient{}
	renderer := NewCardKitRenderer(client, "source")
	before := time.Now().UTC()
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	first := renderer.RenderRef()
	if first.CardID == "" || first.ReplyMessageID == "" || first.CreatedAt.IsZero() {
		t.Fatalf("bound ref = %#v", first)
	}
	if first.CreatedAt.Location() != time.UTC || first.CreatedAt.Before(before) || first.CreatedAt.After(after) {
		t.Fatalf("CreatedAt = %s, want UTC in [%s, %s]", first.CreatedAt, before, after)
	}
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	advanced := renderer.RenderRef()
	if advanced.Version != first.Version+1 || !advanced.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("advanced ref = %#v, first = %#v", advanced, first)
	}

	rehydrated := NewCardKitRouterRenderer(client).Rehydrate("session", advanced)
	if err := rehydrated.Render(card.Event{Type: "result", SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	last := client.updateReqs[len(client.updateReqs)-1]
	if last.Sequence != advanced.Version+1 {
		t.Fatalf("rehydrated sequence = %d, want %d", last.Sequence, advanced.Version+1)
	}
	if got := rehydrated.RenderRef(); !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("rehydrated ref = %#v, want CreatedAt %s", got, first.CreatedAt)
	}
}

func TestCardKitRendererDoesNotExposeRefBeforeReplyBinding(t *testing.T) {
	t.Run("before first render", func(t *testing.T) {
		renderer := NewCardKitRenderer(&fakeCardKitClient{}, "source-message")
		if ref := renderer.RenderRef(); ref != (session.RenderRef{}) || !ref.CreatedAt.IsZero() {
			t.Fatalf("empty render ref = %#v", ref)
		}
	})
	t.Run("reply failure", func(t *testing.T) {
		client := &fakeCardKitClient{replyErr: errors.New("reply failed")}
		renderer := NewCardKitRenderer(client, "source-message")
		if err := renderer.Render(card.Event{Type: "stream", SessionID: "claude:chat"}); err == nil {
			t.Fatal("Render() error = nil, want reply failure")
		}
		if ref := renderer.RenderRef(); ref != (session.RenderRef{}) || !ref.CreatedAt.IsZero() {
			t.Fatalf("render ref exposed before reply binding: %#v", ref)
		}
	})
}

func TestCardKitRendererClassifiesOnlyStaleMappingErrors(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		stale bool
	}{
		{name: "not found", err: &FeishuAPIError{HTTPStatus: 400, Code: 200740, Message: "card entity does not exist"}, stale: true},
		{name: "expired", err: &FeishuAPIError{HTTPStatus: 400, Code: 200750, Message: "card entity has expired"}, stale: true},
		{name: "sequence", err: &FeishuAPIError{HTTPStatus: 400, Code: 300317, Message: "sequence did not increment"}, stale: true},
		{name: "legacy invalid card", err: &FeishuAPIError{HTTPStatus: 400, Code: 230099, Message: "invalid card_id"}, stale: true},
		{name: "cardkit invalid card", err: &FeishuAPIError{HTTPStatus: 200, Code: 10002, Message: "ErrMsg: cardid invalid; "}, stale: true},
		{name: "server", err: &FeishuAPIError{HTTPStatus: 500, Code: 999, Message: "server"}},
		{name: "network", err: errors.New("connection reset")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeCardKitClient{updateErr: tc.err}
			router := NewCardKitRouterRenderer(client)
			renderer := router.Rehydrate("scope", session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 7})
			err := renderer.Render(card.Event{Type: "result", SessionID: "scope"})
			if err == nil {
				t.Fatal("Render() error = nil")
			}
			if got := errors.Is(err, ErrStaleRenderRef); got != tc.stale {
				t.Fatalf("errors.Is(%v, ErrStaleRenderRef) = %t, want %t", err, got, tc.stale)
			}
			if client.updateReqs[0].Sequence != 8 {
				t.Fatalf("sequence = %d, want 8", client.updateReqs[0].Sequence)
			}
		})
	}
}

func TestCardKitRouterAppendTerminalCreatesIndependentCard(t *testing.T) {
	client := &fakeCardKitClient{}
	router := NewCardKitRouterRenderer(client)
	if err := router.AppendTerminal(context.Background(), "source", card.Event{Type: "result", SessionID: "terminal", Message: fmt.Sprint("done")}); err != nil {
		t.Fatal(err)
	}
	if client.created != 1 || client.replied != 1 || client.updated != 0 {
		t.Fatalf("created/replied/updated = %d/%d/%d", client.created, client.replied, client.updated)
	}
}

func TestCardKitRouterRendererReusesSessionCard(t *testing.T) {
	client := &fakeCardKitClient{}
	router := NewCardKitRouterRenderer(client)
	first := card.Event{Type: "stream", SessionID: "claude:chat", ReplyToMessageID: "message-1", Segments: []card.Segment{{Kind: card.SegmentText, Text: "hello"}}}
	second := card.Event{Type: "stream", SessionID: "claude:chat", Segments: []card.Segment{{Kind: card.SegmentText, Text: "world"}}}
	if err := router.Render(first); err != nil {
		t.Fatalf("first render error: %v", err)
	}
	if err := router.Render(second); err != nil {
		t.Fatalf("second render error: %v", err)
	}
	if router.ActiveCards() != 1 {
		t.Fatalf("active cards = %d, want 1", router.ActiveCards())
	}
	if client.created != 1 || client.updated != 1 {
		t.Fatalf("created/updated = %d/%d", client.created, client.updated)
	}
}

func TestCardKitRouterRendererReusesSessionForPagedStream(t *testing.T) {
	client := &fakeCardKitClient{}
	router := NewCardKitRouterRenderer(client)
	first := card.Event{Type: "stream", SessionID: "claude:chat", ReplyToMessageID: "message-1", Segments: []card.Segment{{Kind: card.SegmentText, Text: "page one"}}, Message: "page 1/2"}
	second := card.Event{Type: "stream", SessionID: "claude:chat", Segments: []card.Segment{{Kind: card.SegmentText, Text: "page two"}}, Message: "page 2/2"}
	if err := router.Render(first); err != nil {
		t.Fatalf("first page render error: %v", err)
	}
	if err := router.Render(second); err != nil {
		t.Fatalf("second page render error: %v", err)
	}
	if router.ActiveCards() != 1 {
		t.Fatalf("active cards = %d, want 1", router.ActiveCards())
	}
	if client.created != 1 || client.updated != 1 {
		t.Fatalf("created/updated = %d/%d", client.created, client.updated)
	}
}

func TestCardKitReplyUUIDIncludesReplyMessage(t *testing.T) {
	client := &fakeCardKitClient{}
	first := NewCardKitRenderer(client, "message-1")
	if err := first.Render(card.Event{Type: "stream", SessionID: "claude:chat"}); err != nil {
		t.Fatalf("first render error: %v", err)
	}
	second := NewCardKitRenderer(client, "message-2")
	if err := second.Render(card.Event{Type: "stream", SessionID: "claude:chat"}); err != nil {
		t.Fatalf("second render error: %v", err)
	}
	if len(client.replyUUIDs) != 2 {
		t.Fatalf("reply UUIDs = %#v", client.replyUUIDs)
	}
	if client.replyUUIDs[0] == client.replyUUIDs[1] {
		t.Fatalf("reply UUID reused for different messages: %s", client.replyUUIDs[0])
	}
}

func TestCardKitRouterRendererRecordsRenderAudit(t *testing.T) {
	client := &fakeCardKitClient{}
	observer := &fakeCardKitObserver{}
	router := NewCardKitRouterRendererWithObserver(client, observer)
	first := card.Event{Type: "stream", SessionID: "claude:chat", ReplyToMessageID: "message-1", Segments: []card.Segment{{Kind: card.SegmentText, Text: "hello"}}}
	second := card.Event{Type: "stopped", SessionID: "claude:chat", StopButton: card.StopButton{Visible: true, Disabled: true}, HeaderTemplate: "grey"}
	if err := router.Render(first); err != nil {
		t.Fatalf("first render error: %v", err)
	}
	if err := router.Render(second); err != nil {
		t.Fatalf("second render error: %v", err)
	}
	wantActions := []string{"cardkit_create", "cardkit_reply", "cardkit_update"}
	if len(observer.actions) != len(wantActions) {
		t.Fatalf("observer actions = %#v, want %#v", observer.actions, wantActions)
	}
	for i, want := range wantActions {
		if observer.actions[i] != want {
			t.Fatalf("observer action[%d] = %s, want %s", i, observer.actions[i], want)
		}
	}
	if observer.details[0] == "" || observer.details[2] == "" {
		t.Fatalf("observer details missing: %#v", observer.details)
	}
	if !strings.Contains(observer.details[1], "message_id=msg-1") {
		t.Fatalf("reply detail = %q, want reply message id", observer.details[1])
	}
	if !containsAll(observer.details[2], "event=stopped", "template=grey", "stop_visible=true", "stop_disabled=true") {
		t.Fatalf("stopped detail = %q", observer.details[2])
	}
	if client.created != 1 || client.replied != 1 || client.updated != 1 {
		t.Fatalf("created/replied/updated = %d/%d/%d, want 1/1/1", client.created, client.replied, client.updated)
	}
}

func TestCardKitRendererUpdatesStoppedCardInPlace(t *testing.T) {
	client := &fakeCardKitClient{}
	renderer := NewCardKitRenderer(client, "message-1")
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "claude:chat"}); err != nil {
		t.Fatalf("first render error: %v", err)
	}
	stop := card.Event{Type: "stopped", SessionID: "claude:chat", StopButton: card.StopButton{Visible: true, Disabled: true}, HeaderTemplate: "grey"}
	if err := renderer.Render(stop); err != nil {
		t.Fatalf("stop render error: %v", err)
	}
	if err := renderer.Render(stop); err != nil {
		t.Fatalf("second stop render error: %v", err)
	}
	if client.created != 1 || client.replied != 1 {
		t.Fatalf("created/replied = %d/%d, want one initial card only", client.created, client.replied)
	}
	if client.updated != 2 {
		t.Fatalf("updated = %d, want one update per stopped render", client.updated)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

// topicJoinCapture records CardKit topic-join notifications so the tests can
// assert the renderer forwards Feishu-assigned thread_id upward.
type topicJoinCapture struct {
	fakeCardKitObserver
	joins []topicJoinEvent
}

type topicJoinEvent struct {
	ChatID           string
	ThreadID         string
	ReplyToMessageID string
	MessageID        string
	SyntheticThread  string
}

func (c *topicJoinCapture) RecordCardReply(chatID, threadID, replyToMessageID, messageID, syntheticThread string, _ time.Time) {
	c.joins = append(c.joins, topicJoinEvent{
		ChatID:           chatID,
		ThreadID:         threadID,
		ReplyToMessageID: replyToMessageID,
		MessageID:        messageID,
		SyntheticThread:  syntheticThread,
	})
}

func TestCardKitRendererNotifiesTopicJoinOnReplyWithThreadID(t *testing.T) {
	client := &fakeCardKitClient{replyResult: CardKitReplyResult{MessageID: "om_reply", ChatID: "oc_chat", ThreadID: "omt_topic"}}
	obs := &topicJoinCapture{}
	renderer := NewCardKitRendererWithObserver(client, "om_origin", obs, "session-a")
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "session-a", ReplyInThread: true}); err != nil {
		t.Fatalf("render error: %v", err)
	}
	if got := len(obs.joins); got != 1 {
		t.Fatalf("topic joins = %d, want 1; audit=%v", got, obs.fakeCardKitObserver.actions)
	}
	got := obs.joins[0]
	want := topicJoinEvent{ChatID: "oc_chat", ThreadID: "omt_topic", ReplyToMessageID: "om_origin", MessageID: "om_reply"}
	if got != want {
		t.Fatalf("topic join = %+v, want %+v", got, want)
	}
}

func TestCardKitRendererForwardsBindingTopicThreadKey(t *testing.T) {
	// The renderer must forward the run's synthetic thread key (from the
	// RenderBinding, i.e. the session key Thread) to the observer, not re-derive
	// one from the reply. This is what lets the alias bind to the same session
	// for in-topic follow-ups whose replyTo is their own id.
	client := &fakeCardKitClient{replyResult: CardKitReplyResult{MessageID: "om_G", ChatID: "oc_chat", ThreadID: "omt_X"}}
	obs := &topicJoinCapture{}
	binding := RenderBinding{RunCardSessionID: "session-d", TopicThreadKey: "@bot:om_A"}
	renderer := newCardKitRendererWithClock(client, "om_G", obs, binding.RunCardSessionID, binding, nil, time.Now)
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "session-d", ReplyInThread: true}); err != nil {
		t.Fatalf("render error: %v", err)
	}
	if got := len(obs.joins); got != 1 {
		t.Fatalf("topic joins = %d, want 1", got)
	}
	if got := obs.joins[0].SyntheticThread; got != "@bot:om_A" {
		t.Fatalf("synthetic thread = %q, want %q (the routed session key, not derived from reply om_G)", got, "@bot:om_A")
	}
}

func TestCardKitRendererSkipsTopicJoinWhenThreadIDEmpty(t *testing.T) {
	// Non-topic reply (ReplyInThread=false or Feishu did not create a thread)
	// leaves ThreadID empty; the renderer must not fire a topic-join hint.
	client := &fakeCardKitClient{replyResult: CardKitReplyResult{MessageID: "om_reply", ChatID: "oc_chat"}}
	obs := &topicJoinCapture{}
	renderer := NewCardKitRendererWithObserver(client, "om_origin", obs, "session-b")
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "session-b", ReplyInThread: false}); err != nil {
		t.Fatalf("render error: %v", err)
	}
	if len(obs.joins) != 0 {
		t.Fatalf("topic joins = %d, want 0", len(obs.joins))
	}
}

func TestCardKitRendererSkipsTopicJoinWhenObserverDoesNotImplement(t *testing.T) {
	// Existing observers that only satisfy CardKitRenderObserver must keep
	// working — the topic-join hook is strictly optional.
	client := &fakeCardKitClient{replyResult: CardKitReplyResult{MessageID: "om_reply", ChatID: "oc_chat", ThreadID: "omt_topic"}}
	obs := &fakeCardKitObserver{}
	renderer := NewCardKitRendererWithObserver(client, "om_origin", obs, "session-c")
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "session-c", ReplyInThread: true}); err != nil {
		t.Fatalf("render error: %v", err)
	}
	// No panic, no join delivered anywhere else — success.
}
