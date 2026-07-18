package feishu

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	nextCardSeq int
	updateReqs  []CardKitUpdateCardRequest
	replyResult CardKitReplyResult
	replyErr    error
	updateErr   error
	updateErrs  []error
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
	if f.replyErr != nil {
		return CardKitReplyResult{}, f.replyErr
	}
	if f.replyResult.MessageID != "" {
		return f.replyResult, nil
	}
	return CardKitReplyResult{MessageID: "msg-1"}, nil
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

func (f *fakeCardKitClient) UpdateElementContent(context.Context, CardKitUpdateElementContentRequest) error {
	return nil
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
