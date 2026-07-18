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

	"lark-agent-bridge/internal/card"
)

type countingTokenSource struct{ calls int }

func (s *countingTokenSource) Token(context.Context) (string, error) {
	s.calls++
	return "token", nil
}

type recordingCardKitHTTP struct{ body []byte }

func (h *recordingCardKitHTTP) Do(req *http.Request) (*http.Response, error) {
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
			client := NewCardKitClientWithTokenSource(tokens)
			err := method.call(client, map[string]any{"tag": "markdown", "content": strings.Repeat("界", card.LarkCardSoftMaxJSONBytes)})
			if !errors.Is(err, card.ErrCardPayloadOversize) {
				t.Fatalf("error = %v, want ErrCardPayloadOversize", err)
			}
			if tokens.calls != 0 {
				t.Fatalf("token lookups = %d, want 0", tokens.calls)
			}
		})
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
