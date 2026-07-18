package bridge

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/card"
)

func TestActionResultPrepareCardFitsLongCallbackContent(t *testing.T) {
	result := ActionResult{Event: &card.Event{Type: "result", Segments: []card.Segment{{Kind: card.SegmentText, Text: strings.Repeat("界", card.LarkCardSoftMaxJSONBytes)}}}}
	prepared, err := result.PrepareCard(0)
	if err != nil {
		t.Fatalf("PrepareCard() error: %v", err)
	}
	if got := prepared.Capacity(); got.JSONBytes > card.LarkCardSoftMaxJSONBytes || got.Components > card.LarkCardMaxComponents {
		t.Fatalf("prepared capacity = %#v", got)
	}
}
