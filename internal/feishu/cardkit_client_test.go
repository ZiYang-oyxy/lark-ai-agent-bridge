package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	body  []byte
	calls int
}

func (h *recordingCardKitHTTP) Do(req *http.Request) (*http.Response, error) {
	h.calls++
	var err error
	h.body, err = io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"card_id":"card-1"}}`))}, nil
}

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
