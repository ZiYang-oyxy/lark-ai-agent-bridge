package feishu

import (
	"context"
	"testing"

	"lark-agent-bridge/internal/card"
)

type fakeCardKitClient struct {
	created     int
	replied     int
	updated     int
	lastCard    map[string]any
	replyUUIDs  []string
	nextCardSeq int
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
	f.lastCard = req.Card
	f.nextCardSeq++
	return CardKitCreateResult{CardID: "card-" + string(rune('0'+f.nextCardSeq))}, nil
}

func (f *fakeCardKitClient) ReplyCard(_ context.Context, req CardKitReplyRequest) (CardKitReplyResult, error) {
	f.replied++
	f.replyUUIDs = append(f.replyUUIDs, req.UUID)
	return CardKitReplyResult{MessageID: "msg-1"}, nil
}

func (f *fakeCardKitClient) UpdateCard(_ context.Context, req CardKitUpdateCardRequest) error {
	f.updated++
	f.lastCard = req.Card
	return nil
}

func (f *fakeCardKitClient) UpdateSettings(context.Context, CardKitUpdateSettingsRequest) error {
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
	second := card.Event{Type: "stop_button", SessionID: "claude:chat", StopButton: card.StopButton{Visible: true, Disabled: true}}
	if err := router.Render(first); err != nil {
		t.Fatalf("first render error: %v", err)
	}
	if err := router.Render(second); err != nil {
		t.Fatalf("second render error: %v", err)
	}
	wantActions := []string{"cardkit_create", "cardkit_reply", "cardkit_update", "cardkit_update", "cardkit_terminal_create", "cardkit_terminal_reply"}
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
	if client.created != 2 || client.replied != 2 || client.updated != 2 {
		t.Fatalf("created/replied/updated = %d/%d/%d, want 2/2/2", client.created, client.replied, client.updated)
	}
}

func TestCardKitRendererSendsStopTerminalCardOnce(t *testing.T) {
	client := &fakeCardKitClient{}
	renderer := NewCardKitRenderer(client, "message-1")
	if err := renderer.Render(card.Event{Type: "stream", SessionID: "claude:chat"}); err != nil {
		t.Fatalf("first render error: %v", err)
	}
	stop := card.Event{Type: "stop_button", SessionID: "claude:chat", StopButton: card.StopButton{Visible: true, Disabled: true}}
	if err := renderer.Render(stop); err != nil {
		t.Fatalf("stop render error: %v", err)
	}
	if err := renderer.Render(stop); err != nil {
		t.Fatalf("second stop render error: %v", err)
	}
	if client.created != 2 || client.replied != 2 {
		t.Fatalf("created/replied = %d/%d, want one initial card and one terminal card", client.created, client.replied)
	}
	if client.updated != 4 {
		t.Fatalf("updated = %d, want two updates per stop render", client.updated)
	}
}
