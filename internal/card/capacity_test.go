package card

import (
	"errors"
	"strings"
	"testing"
)

func TestMarshalLarkCardMeasuresUTF8BytesAndNestedComponents(t *testing.T) {
	payload := map[string]any{
		"body": map[string]any{
			"elements": []any{
				map[string]any{"tag": "markdown", "content": "你好"},
				map[string]any{
					"tag": "select_static",
					"options": []any{
						map[string]any{"text": map[string]any{"tag": "plain_text", "content": "选项"}},
					},
				},
			},
		},
	}

	encoded, capacity, err := MarshalLarkCard(payload)
	if err != nil {
		t.Fatalf("MarshalLarkCard() error: %v", err)
	}
	if capacity.JSONBytes != len(encoded) {
		t.Fatalf("JSONBytes = %d, want %d", capacity.JSONBytes, len(encoded))
	}
	if capacity.Components != 3 {
		t.Fatalf("Components = %d, want 3", capacity.Components)
	}
	if capacity.MaxJSONBytes != LarkCardSoftMaxJSONBytes || capacity.MaxComponents != LarkCardMaxComponents {
		t.Fatalf("limits = %#v", capacity)
	}
}

func TestMarshalLarkCardRejects201stComponentButReturnsEncodedPayload(t *testing.T) {
	elements := make([]any, LarkCardMaxComponents+1)
	for i := range elements {
		elements[i] = map[string]any{"tag": "markdown", "content": "x"}
	}

	encoded, capacity, err := MarshalLarkCard(map[string]any{"body": map[string]any{"elements": elements}})
	if !errors.Is(err, ErrCardPayloadOversize) {
		t.Fatalf("MarshalLarkCard() error = %v, want ErrCardPayloadOversize", err)
	}
	if len(encoded) == 0 || capacity.Components != LarkCardMaxComponents+1 {
		t.Fatalf("encoded/capacity = %d/%#v", len(encoded), capacity)
	}
}

func TestMarshalLarkCardRejectsOversizedUTF8Payload(t *testing.T) {
	encoded, _, err := MarshalLarkCard(map[string]any{"tag": "markdown", "content": strings.Repeat("界", LarkCardSoftMaxJSONBytes)})
	if !errors.Is(err, ErrCardPayloadOversize) {
		t.Fatalf("MarshalLarkCard() error = %v, want ErrCardPayloadOversize", err)
	}
	if len(encoded) <= LarkCardSoftMaxJSONBytes {
		t.Fatalf("len(encoded) = %d, want > %d", len(encoded), LarkCardSoftMaxJSONBytes)
	}
}

func TestPreparedLarkCardRejectsZeroAndIntegrityFailure(t *testing.T) {
	var zero PreparedLarkCard
	if err := ValidatePreparedLarkCard(zero); !errors.Is(err, ErrInvalidPreparedLarkCard) {
		t.Fatalf("ValidatePreparedLarkCard(zero) = %v", err)
	}

	prepared, err := PrepareLarkCard(Event{Type: "result", Segments: []Segment{{Kind: SegmentText, Text: "answer"}}})
	if err != nil {
		t.Fatalf("PrepareLarkCard() error: %v", err)
	}
	prepared.json[0] ^= 1
	if err := ValidatePreparedLarkCard(prepared); !errors.Is(err, ErrInvalidPreparedLarkCard) {
		t.Fatalf("ValidatePreparedLarkCard(tampered) = %v", err)
	}
}

func TestPreparedLarkCardAccessorsReturnDefensiveCopies(t *testing.T) {
	prepared, err := PrepareLarkCard(Event{
		Type:       "result",
		SessionID:  "session-a",
		Segments:   []Segment{{Kind: SegmentText, Text: "answer"}},
		ConfigForm: &ConfigForm{Models: []string{"a", "b"}},
	})
	if err != nil {
		t.Fatalf("PrepareLarkCard() error: %v", err)
	}

	event := prepared.EventCopy()
	event.SessionID = "changed"
	event.Segments[0].Text = "changed"
	event.ConfigForm.Models[0] = "changed"
	payload := prepared.PayloadCopy()
	payload["schema"] = "changed"
	encoded := prepared.CardJSON()
	encoded[0] ^= 1

	if got := prepared.EventCopy(); got.SessionID != "session-a" || got.Segments[0].Text != "answer" || got.ConfigForm.Models[0] != "a" {
		t.Fatalf("EventCopy mutated prepared event: %#v", got)
	}
	if got := prepared.PayloadCopy()["schema"]; got != "2.0" {
		t.Fatalf("PayloadCopy mutated prepared payload: %#v", got)
	}
	if err := ValidatePreparedLarkCard(prepared); err != nil {
		t.Fatalf("ValidatePreparedLarkCard() after accessors: %v", err)
	}
}

func TestPrepareLarkCardDropsOldestToolOutputBeforeThoughtOrAnswer(t *testing.T) {
	event := Event{Type: "result", Segments: []Segment{
		{Kind: SegmentText, Text: "answer remains"},
		{Kind: SegmentThought, Text: "thought remains"},
		{Kind: SegmentTool, Text: strings.Repeat("old-tool ", LarkCardSoftMaxJSONBytes)},
		{Kind: SegmentTool, Text: "latest tool summary"},
	}}

	prepared, err := PrepareLarkCard(event)
	if err != nil {
		t.Fatalf("PrepareLarkCard() error: %v", err)
	}
	if got := prepared.Capacity(); got.JSONBytes > LarkCardSoftMaxJSONBytes || got.Components > LarkCardMaxComponents {
		t.Fatalf("prepared capacity = %#v", got)
	}
	got := prepared.EventCopy()
	if len(got.Segments) != 3 || got.Segments[1].Text != "thought remains" || got.Segments[2].Text != "latest tool summary" {
		t.Fatalf("fitted segments = %#v", got.Segments)
	}
	if event.Segments[2].Text == "" {
		t.Fatal("PrepareLarkCard mutated input event")
	}
}

func TestPrepareLarkCardPreservesLatestActivityForToolOnlyAndThoughtOnlyEvents(t *testing.T) {
	for _, kind := range []SegmentKind{SegmentTool, SegmentThought} {
		t.Run(string(kind), func(t *testing.T) {
			prepared, err := PrepareLarkCard(Event{Type: "stream", Segments: []Segment{{Kind: kind, Text: strings.Repeat("activity ", LarkCardSoftMaxJSONBytes)}}})
			if err != nil {
				t.Fatalf("PrepareLarkCard() error: %v", err)
			}
			var retained string
			for _, segment := range prepared.EventCopy().Segments {
				if segment.Kind == kind {
					retained = segment.Text
				}
			}
			if strings.TrimSpace(retained) == "" {
				t.Fatalf("latest %s summary was lost: %#v", kind, prepared.EventCopy().Segments)
			}
		})
	}
}

func TestPrepareLarkCardTruncatesAnswerTailBeforeEmergencyFallback(t *testing.T) {
	answer := "answer prefix " + strings.Repeat("tail ", LarkCardSoftMaxJSONBytes)
	prepared, err := PrepareLarkCard(Event{Type: "result", Segments: []Segment{{Kind: SegmentText, Text: answer}}})
	if err != nil {
		t.Fatalf("PrepareLarkCard() error: %v", err)
	}
	if !prepared.NativeReady() || prepared.Answer() == "" || !strings.HasPrefix(prepared.Answer(), "answer prefix") {
		t.Fatalf("prepared answer = %q native=%t", prepared.Answer(), prepared.NativeReady())
	}
}

func TestPrepareLarkCardUsesStaticEmergencyFallbackForUnfitCallerControlledStructure(t *testing.T) {
	prepared, err := PrepareLarkCard(Event{
		Type:      "result",
		SessionID: strings.Repeat("session", LarkCardSoftMaxJSONBytes),
		Actions:   []Action{{ID: "action", Label: strings.Repeat("label", LarkCardSoftMaxJSONBytes), Value: strings.Repeat("value", LarkCardSoftMaxJSONBytes)}},
	})
	if err != nil {
		t.Fatalf("PrepareLarkCard() error: %v", err)
	}
	if prepared.NativeReady() || prepared.Answer() != "" {
		t.Fatalf("emergency prepared = native=%t answer=%q", prepared.NativeReady(), prepared.Answer())
	}
	encoded := string(prepared.CardJSON())
	if strings.Contains(encoded, "session") || strings.Contains(encoded, "label") || strings.Contains(encoded, "value") {
		t.Fatalf("emergency payload includes caller content: %q", encoded)
	}
}
