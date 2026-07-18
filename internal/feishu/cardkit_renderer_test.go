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
	renderer, err := router.NewStreaming(context.Background(), "session", "message")
	if err != nil {
		t.Fatal(err)
	}
	concrete := renderer.(*CardKitRenderer)
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
	concrete := renderer.(*CardKitRenderer)
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

func TestCardKitRendererNativeAnswerPreservesHeaderPhaseChanges(t *testing.T) {
	client := &fakeCardKitClient{}
	renderer := NewCardKitRendererWithNative(client, "source", nil, RenderBinding{
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
}

func TestCardKitRendererNativeDeliveryUnknownStopsAllLaterWrites(t *testing.T) {
	client := &fakeCardKitClient{elementErr: errors.New("response lost")}
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
	terminal := card.Event{Type: "result", SessionID: "run", Segments: second.Segments}
	if err := renderer.Render(terminal); err != nil {
		t.Fatal(err)
	}
	if len(client.elementReqs) != 1 || len(client.updateReqs) != 0 {
		t.Fatalf("element/full=%d/%d", len(client.elementReqs), len(client.updateReqs))
	}
	if ref := renderer.RenderRef(); !ref.SequenceUnknown || ref.PendingSequence != 1 || ref.Version != 0 {
		t.Fatalf("unknown ref=%#v", ref)
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
	if ref := renderer.RenderRef(); ref.Version != 0 || !ref.SequenceUnknown || ref.PendingSequence != 1 {
		t.Fatalf("ref=%#v", ref)
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
