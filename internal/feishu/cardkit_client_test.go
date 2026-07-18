package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/card"
)

type countingTokenSource struct{ calls int }

func (s *countingTokenSource) Token(context.Context) (string, error) {
	s.calls++
	return "token", nil
}

type recordingCardKitHTTP struct {
	body   []byte
	calls  int
	method string
	path   string
}

func (h *recordingCardKitHTTP) Do(req *http.Request) (*http.Response, error) {
	h.calls++
	h.method = req.Method
	h.path = req.URL.EscapedPath()
	var err error
	h.body, err = io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"card_id":"card-1"}}`))}, nil
}

func TestCardKitClientUpdateElementContentRequestShape(t *testing.T) {
	tokens := &countingTokenSource{}
	httpClient := &recordingCardKitHTTP{}
	client := NewCardKitClientWithTokenSource(tokens)
	client.http = httpClient
	client.limiter = nil
	err := client.UpdateElementContent(context.Background(), CardKitUpdateElementContentRequest{
		CardID: "card / 1", ElementID: "answer / 1", Content: "完整正文", Sequence: 7, UUID: "native-7",
	})
	if err != nil {
		t.Fatalf("UpdateElementContent() error: %v", err)
	}
	if httpClient.method != http.MethodPut || httpClient.path != "/open-apis/cardkit/v1/cards/card%20%2F%201/elements/answer%20%2F%201/content" {
		t.Fatalf("request = %s %s", httpClient.method, httpClient.path)
	}
	var body map[string]any
	if err := json.Unmarshal(httpClient.body, &body); err != nil {
		t.Fatal(err)
	}
	if body["content"] != "完整正文" || body["sequence"] != float64(7) || body["uuid"] != "native-7" || len(body) != 3 {
		t.Fatalf("body = %#v", body)
	}
	if tokens.calls != 1 || httpClient.calls != 1 {
		t.Fatalf("calls token=%d http=%d", tokens.calls, httpClient.calls)
	}
}

func TestCardKitClientUpdateElementContentRejectsInvalidOrOversizeBeforeSideEffects(t *testing.T) {
	cases := []CardKitUpdateElementContentRequest{
		{ElementID: "answer", Content: "x", Sequence: 1, UUID: "u"},
		{CardID: "card", Content: "x", Sequence: 1, UUID: "u"},
		{CardID: "card", ElementID: "answer", Content: "x", Sequence: 0, UUID: "u"},
		{CardID: "card", ElementID: "answer", Content: "x", Sequence: 1},
		{CardID: "card", ElementID: "answer", Content: strings.Repeat("界", card.LarkCardSoftMaxJSONBytes), Sequence: 1, UUID: "u"},
	}
	for i, req := range cases {
		tokens := &countingTokenSource{}
		httpClient := &recordingCardKitHTTP{}
		client := NewCardKitClientWithTokenSource(tokens)
		client.http = httpClient
		client.limiter = newSerialRateLimiter(time.Hour)
		err := client.UpdateElementContent(context.Background(), req)
		if err == nil {
			t.Fatalf("case %d: error = nil", i)
		}
		if i == len(cases)-1 && !errors.Is(err, card.ErrCardPayloadOversize) {
			t.Fatalf("case %d: error = %v, want oversize", i, err)
		}
		if tokens.calls != 0 || httpClient.calls != 0 || !client.limiter.next.IsZero() {
			t.Fatalf("case %d side effects token=%d http=%d limiter=%v", i, tokens.calls, httpClient.calls, client.limiter.next)
		}
	}
}

func TestCardKitClientUpdateElementContentClassifiesInteractionOnly(t *testing.T) {
	for _, tc := range []struct {
		code            int
		wantInteraction bool
	}{
		{code: 200810, wantInteraction: true},
		{code: 300317, wantInteraction: false},
		{code: 200740, wantInteraction: false},
	} {
		client := NewCardKitClientWithTokenSource(&countingTokenSource{})
		client.limiter = nil
		client.http = HTTPDoerFunc(func(*http.Request) (*http.Response, error) {
			body := fmt.Sprintf(`{"code":%d,"msg":"rejected"}`, tc.code)
			return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		err := client.UpdateElementContent(context.Background(), CardKitUpdateElementContentRequest{CardID: "card", ElementID: "answer", Content: "x", Sequence: 1, UUID: "u"})
		if errors.Is(err, ErrCardInteractionInProgress) != tc.wantInteraction {
			t.Fatalf("code %d error=%v interaction=%t", tc.code, err, tc.wantInteraction)
		}
	}
}

type HTTPDoerFunc func(*http.Request) (*http.Response, error)

func (f HTTPDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestCardKitClientCapacityRejectsDirectCreateAndUpdateBeforeTokenLookup(t *testing.T) {
	for _, method := range []struct {
		name string
		call func(*CardKitClient, map[string]any) error
	}{
		{"create", func(client *CardKitClient, payload map[string]any) error {
			_, err := client.CreateCard(context.Background(), CardKitCreateRequest{Card: payload})
			return err
		}},
		{"update", func(client *CardKitClient, payload map[string]any) error {
			return client.UpdateCard(context.Background(), CardKitUpdateCardRequest{CardID: "card", Card: payload})
		}},
	} {
		t.Run(method.name, func(t *testing.T) {
			tokens := &countingTokenSource{}
			httpClient := &recordingCardKitHTTP{}
			client := NewCardKitClientWithTokenSource(tokens)
			client.http = httpClient
			client.limiter = newSerialRateLimiter(time.Hour)
			err := method.call(client, map[string]any{"tag": "markdown", "content": strings.Repeat("界", card.LarkCardSoftMaxJSONBytes)})
			if !errors.Is(err, card.ErrCardPayloadOversize) {
				t.Fatalf("error = %v, want ErrCardPayloadOversize", err)
			}
			if tokens.calls != 0 {
				t.Fatalf("token lookups = %d, want 0", tokens.calls)
			}
			if !client.limiter.next.IsZero() || httpClient.calls != 0 {
				t.Fatalf("limiter/http were used on local rejection: next=%v calls=%d", client.limiter.next, httpClient.calls)
			}
		})
	}
}

func TestCardKitClientForwardsReplyInThread(t *testing.T) {
	for _, want := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "topic"}[want], func(t *testing.T) {
			tokens := &countingTokenSource{}
			httpClient := &recordingCardKitHTTP{}
			client := NewCardKitClientWithTokenSource(tokens)
			client.http = httpClient
			client.limiter = nil
			if _, err := client.ReplyCard(context.Background(), CardKitReplyRequest{ReplyToMessageID: "source", CardID: "card", UUID: "uuid", ReplyInThread: want}); err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(httpClient.body, &body); err != nil {
				t.Fatal(err)
			}
			if body["reply_in_thread"] != want {
				t.Fatalf("request body = %#v, want reply_in_thread=%t", body, want)
			}
			if tokens.calls != 1 || httpClient.calls != 1 {
				t.Fatalf("calls token=%d http=%d", tokens.calls, httpClient.calls)
			}
		})
	}
}

func TestCardKitClientCapacityRejectsRawMessageAndStructBeforeLocalSideEffects(t *testing.T) {
	elements := make([]any, card.LarkCardMaxComponents+1)
	for i := range elements {
		elements[i] = map[string]any{"tag": "markdown", "content": "x"}
	}
	raw, err := json.Marshal(map[string]any{"elements": elements})
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]map[string]any{
		"raw": {"body": json.RawMessage(raw)},
		"struct": {"body": struct {
			Elements []any `json:"elements"`
		}{Elements: elements}},
	} {
		for _, update := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				tokens := &countingTokenSource{}
				httpClient := &recordingCardKitHTTP{}
				client := NewCardKitClientWithTokenSource(tokens)
				client.http = httpClient
				client.limiter = newSerialRateLimiter(time.Hour)
				var err error
				if update {
					err = client.UpdateCard(context.Background(), CardKitUpdateCardRequest{CardID: "card", Card: payload})
				} else {
					_, err = client.CreateCard(context.Background(), CardKitCreateRequest{Card: payload})
				}
				if !errors.Is(err, card.ErrCardPayloadOversize) || tokens.calls != 0 || !client.limiter.next.IsZero() || httpClient.calls != 0 {
					t.Fatalf("local rejection err=%v tokens=%d limiter=%v http=%d", err, tokens.calls, client.limiter.next, httpClient.calls)
				}
			})
		}
	}
}

func TestCardKitClientPreparedCreateUsesExactPreparedJSON(t *testing.T) {
	prepared, err := card.PrepareLarkCard(card.Event{Type: "result", Segments: []card.Segment{{Kind: card.SegmentText, Text: "你好"}}})
	if err != nil {
		t.Fatal(err)
	}
	tokens := &countingTokenSource{}
	httpClient := &recordingCardKitHTTP{}
	client := NewCardKitClientWithTokenSource(tokens)
	client.http = httpClient
	client.limiter = nil
	if _, err := client.CreateCard(context.Background(), CardKitCreateRequest{Prepared: &prepared}); err != nil {
		t.Fatalf("CreateCard() error: %v", err)
	}
	var body struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(httpClient.body)).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data != string(prepared.CardJSON()) {
		t.Fatalf("card.data = %q, want exact prepared JSON %q", body.Data, prepared.CardJSON())
	}
}

func TestCardKitClientPreparedUpdateUsesExactPreparedJSON(t *testing.T) {
	prepared, err := card.PrepareLarkCard(card.Event{Type: "result", Segments: []card.Segment{{Kind: card.SegmentText, Text: "你好"}}})
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &recordingCardKitHTTP{}
	client := NewCardKitClientWithTokenSource(&countingTokenSource{})
	client.http = httpClient
	client.limiter = nil
	if err := client.UpdateCard(context.Background(), CardKitUpdateCardRequest{CardID: "card-1", Prepared: &prepared}); err != nil {
		t.Fatalf("UpdateCard() error: %v", err)
	}
	var body struct {
		Card struct {
			Data string `json:"data"`
		} `json:"card"`
	}
	if err := json.NewDecoder(bytes.NewReader(httpClient.body)).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Card.Data != string(prepared.CardJSON()) {
		t.Fatalf("card.data = %q, want exact prepared JSON %q", body.Card.Data, prepared.CardJSON())
	}
}

func TestCardKitClientRejectsInvalidPreparedOrAmbiguousInputBeforeTokenLookup(t *testing.T) {
	prepared, err := card.PrepareLarkCard(card.Event{Type: "result"})
	if err != nil {
		t.Fatal(err)
	}
	// A caller cannot mutate the opaque value; zero and ambiguous values are still rejected here.
	cases := []CardKitCreateRequest{
		{},
		{Prepared: &card.PreparedLarkCard{}},
		{Card: map[string]any{"tag": "markdown"}, Prepared: &prepared},
	}
	for _, req := range cases {
		tokens := &countingTokenSource{}
		client := NewCardKitClientWithTokenSource(tokens)
		if _, err := client.CreateCard(context.Background(), req); err == nil {
			t.Fatalf("CreateCard(%#v) error = nil", req)
		}
		if tokens.calls != 0 {
			t.Fatalf("token lookups = %d, want 0", tokens.calls)
		}
	}
}
