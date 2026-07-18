package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"lark-agent-bridge/internal/card"
)

func TestRealCardKitElementContentRequestShape(t *testing.T) {
	if os.Getenv("E2E_REAL_CARDKIT") != "1" {
		t.Skip("set E2E_REAL_CARDKIT=1 with LARK_APP_ID/LARK_APP_SECRET to run the raw CardKit element-content probe")
	}
	appID := os.Getenv("LARK_APP_ID")
	appSecret := os.Getenv("LARK_APP_SECRET")
	if appID == "" || appSecret == "" {
		t.Skip("LARK_APP_ID and LARK_APP_SECRET are required for the raw CardKit element-content probe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tokens := NewTenantTokenSource(appID, appSecret)
	client := NewCardKitClientWithTokenSource(tokens)
	prepared, err := card.PrepareLarkCard(card.Event{Type: "stream", Streaming: true, SessionID: "cardkit-element-content-probe"})
	if err != nil {
		t.Fatalf("prepare probe card: %v", err)
	}
	created, err := client.CreateCard(ctx, CardKitCreateRequest{Prepared: &prepared})
	if err != nil {
		t.Fatalf("create probe card: %v", err)
	}

	body, err := json.Marshal(map[string]any{"content": "native probe", "sequence": 1})
	if err != nil {
		t.Fatalf("marshal raw element-content request: %v", err)
	}
	token, err := tokens.Token(ctx)
	if err != nil {
		t.Fatalf("get tenant token for raw element-content probe: %v", err)
	}
	endpoint := defaultFeishuOpenAPIBaseURL + "/open-apis/cardkit/v1/cards/" + url.PathEscape(created.CardID) + "/elements/answer/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build raw element-content request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send raw element-content request: %v", err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&envelope); err != nil {
		t.Fatalf("decode raw element-content response metadata: http=%d err=%v", resp.StatusCode, err)
	}

	// 2026-07-18: no endpoint-specific encoded-body ceiling or proven-unapplied
	// response code is frozen. This probe must be run with deliberate credentials,
	// then its non-sensitive status/code/byte evidence must be reviewed before
	// adding production constants or enabling UpdateElementContent.
	t.Fatalf("raw element-content probe observed http=%d code=%d encoded_bytes=%d; evidence is not frozen, native streaming remains disabled", resp.StatusCode, envelope.Code, len(body))
}

func TestRealCardKitCreatesBridgeCard(t *testing.T) {
	if os.Getenv("E2E_REAL_CARDKIT") != "1" {
		t.Skip("set E2E_REAL_CARDKIT=1 with LARK_APP_ID/LARK_APP_SECRET to run real CardKit schema smoke")
	}
	appID := os.Getenv("LARK_APP_ID")
	appSecret := os.Getenv("LARK_APP_SECRET")
	if appID == "" || appSecret == "" {
		t.Fatal("LARK_APP_ID and LARK_APP_SECRET are required")
	}
	tokens := NewTenantTokenSource(appID, appSecret)
	client := NewCardKitClientWithTokenSource(tokens)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	events := []card.Event{
		{
			Type:      "workdir_confirm",
			SessionID: "claude:real-cardkit-smoke:confirm",
			Segments:  []card.Segment{{Kind: card.SegmentText, Text: "Workdir does not exist: /tmp/lark-agent-bridge-cardkit-smoke"}},
			Actions:   card.WorkDirCreateActions("/tmp/lark-agent-bridge-cardkit-smoke"),
			Meta:      card.Meta{Agent: "claude", Model: "smoke", RunTokens: 1, TotalTokens: 1, User: "smoke-user", IP: "192.0.2.1", WorkDir: "/tmp/lark-agent-bridge-cardkit-smoke", Status: "running"},
		},
		{
			Type:      "workdir_created",
			SessionID: "claude:real-cardkit-smoke:created",
			Segments:  []card.Segment{{Kind: card.SegmentText, Text: "Workdir created: /tmp/lark-agent-bridge-cardkit-smoke"}},
			Actions:   card.WorkDirActions("/tmp/lark-agent-bridge-cardkit-smoke", true),
			Meta:      card.Meta{WorkDir: "/tmp/lark-agent-bridge-cardkit-smoke"},
		},
		{
			Type:      "workdir_cancelled",
			SessionID: "claude:real-cardkit-smoke:cancelled",
			Segments:  []card.Segment{{Kind: card.SegmentText, Text: "Workdir creation cancelled: /tmp/lark-agent-bridge-cardkit-smoke"}},
			Actions:   card.WorkDirActions("/tmp/lark-agent-bridge-cardkit-smoke", true),
			Meta:      card.Meta{WorkDir: "/tmp/lark-agent-bridge-cardkit-smoke"},
		},
	}
	for _, event := range events {
		payload := card.BuildLarkCard(event)
		if _, err := client.CreateCard(ctx, CardKitCreateRequest{Card: payload}); err != nil {
			t.Fatalf("CreateCard %s bridge payload error: %v", event.Type, err)
		}
	}
}
