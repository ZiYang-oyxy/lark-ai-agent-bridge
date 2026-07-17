package feishu

import (
	"context"
	"os"
	"testing"
	"time"

	"lark-agent-bridge/internal/card"
)

func TestRealCardKitCreatesBridgeCard(t *testing.T) {
	if os.Getenv("E2E_REAL_CARDKIT") != "1" {
		t.Skip("set E2E_REAL_CARDKIT=1 with LARK_APP_ID/LARK_APP_SECRET to run real CardKit schema smoke")
	}
	appID := os.Getenv("LARK_APP_ID")
	appSecret := os.Getenv("LARK_APP_SECRET")
	if appID == "" || appSecret == "" {
		t.Fatal("LARK_APP_ID and LARK_APP_SECRET are required")
	}
	client := NewCardKitClient(appID, appSecret)
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
