package card

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type encodedComponentContainer struct {
	Elements []any `json:"elements"`
}

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

func TestMarshalLarkCardCountsComponentsFromEncodedRawMessageAndStruct(t *testing.T) {
	elements := componentElements(LarkCardMaxComponents + 1)
	raw, err := json.Marshal(map[string]any{"elements": elements})
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]map[string]any{
		"raw message": {"body": json.RawMessage(raw)},
		"json struct": {"body": encodedComponentContainer{Elements: elements}},
	} {
		t.Run(name, func(t *testing.T) {
			_, capacity, err := MarshalLarkCard(payload)
			if !errors.Is(err, ErrCardPayloadOversize) {
				t.Fatalf("MarshalLarkCard() error = %v, want ErrCardPayloadOversize", err)
			}
			if capacity.Components != LarkCardMaxComponents+1 {
				t.Fatalf("Components = %d, want %d", capacity.Components, LarkCardMaxComponents+1)
			}
		})
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

func TestPrepareLarkCardNativeReadyRequiresStreamingAnswerTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event Event
		want  bool
	}{
		{
			name:  "streaming answer",
			event: Event{Type: "stream", Streaming: true, Segments: []Segment{{Kind: SegmentText, Text: "answer"}}},
			want:  true,
		},
		{
			name:  "streaming without answer target",
			event: Event{Type: "stream", Streaming: true, ConfigForm: &ConfigForm{}},
			want:  false,
		},
		{
			name: "ordered streaming uses full card updates",
			event: Event{Type: "stream", Streaming: true, OrderedLayout: true, Segments: []Segment{
				{Kind: SegmentText, Text: "first"},
				{Kind: SegmentTool, Text: "Bash(ls)"},
				{Kind: SegmentText, Text: "final"},
			}},
			want: false,
		},
		{
			name:  "result",
			event: Event{Type: "result", Streaming: true, Segments: []Segment{{Kind: SegmentText, Text: "answer"}}},
			want:  false,
		},
		{
			name:  "error",
			event: Event{Type: "error", Streaming: true, Segments: []Segment{{Kind: SegmentError, Text: "failure"}}},
			want:  false,
		},
		{
			name:  "stopped",
			event: Event{Type: "stopped", Streaming: true, Segments: []Segment{{Kind: SegmentText, Text: "stopped"}}},
			want:  false,
		},
		{
			name:  "interrupted",
			event: Event{Type: "interrupted", Streaming: true, Segments: []Segment{{Kind: SegmentText, Text: "interrupted"}}},
			want:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepared, err := PrepareLarkCard(tc.event)
			if err != nil {
				t.Fatalf("PrepareLarkCard() error: %v", err)
			}
			if got := prepared.NativeReady(); got != tc.want {
				t.Fatalf("NativeReady() = %t, want %t; event=%#v", got, tc.want, prepared.EventCopy())
			}
		})
	}
}

func TestPrepareLarkCardMarkdownLayoutUsesMarkdownAsNativeAnswer(t *testing.T) {
	prepared, err := PrepareLarkCard(Event{
		Type:           "stream",
		Streaming:      true,
		MarkdownLayout: true,
		Markdown:       "answer\n\n> ✅ **Bash** · pwd",
	})
	if err != nil {
		t.Fatalf("PrepareLarkCard() error: %v", err)
	}
	if !prepared.NativeReady() {
		t.Fatal("markdown layout is not native-ready")
	}
	if got, want := prepared.Answer(), "answer\n\n> ✅ **Bash** · pwd"; got != want {
		t.Fatalf("Answer() = %q, want %q", got, want)
	}
}

func TestPrepareLarkCardInlineTimelineIsNativeReady(t *testing.T) {
	prepared, err := PrepareLarkCard(Event{
		Type:                 "stream",
		Streaming:            true,
		InlineTimelineLayout: true,
		Markdown:             "answer\n\n> ⏳ **Bash** · git status",
		Segments:             []Segment{{Kind: SegmentThought, Text: "reasoning"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.NativeReady() {
		t.Fatal("inline timeline is not native-ready")
	}
	if got := prepared.Answer(); got != "answer\n\n> ⏳ **Bash** · git status" {
		t.Fatalf("answer = %q", got)
	}
}

func TestPrepareLarkCardFitsOversizedInlineTimelineWithoutEmergency(t *testing.T) {
	prepared, err := PrepareLarkCard(Event{
		Type:                 "stream",
		Streaming:            true,
		InlineTimelineLayout: true,
		Markdown:             strings.Repeat("旧过程\n\n", 5000) + "FINAL_REPLY",
		Segments:             []Segment{{Kind: SegmentThought, Text: strings.Repeat("思考", 2000)}},
		Meta:                 Meta{Agent: "claude", Model: "model", User: "user", IP: "host", WorkDir: "/work"},
		StopButton:           StopButton{Visible: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Capacity().JSONBytes > LarkCardSoftMaxJSONBytes {
		t.Fatalf("capacity = %#v", prepared.Capacity())
	}
	if !strings.Contains(prepared.Answer(), "FINAL_REPLY") {
		t.Fatalf("fitted answer lost final reply: %q", prepared.Answer())
	}
	if strings.Contains(string(prepared.CardJSON()), "内容过长，已省略") {
		t.Fatalf("inline timeline fell back to emergency: %s", prepared.CardJSON())
	}
	answers := answerElements(prepared.PayloadCopy())
	if len(answers) != 1 || prepared.Answer() != answers[0]["content"] {
		t.Fatalf("answer mismatch: prepared=%q payload=%#v", prepared.Answer(), answers)
	}
}

func TestNativeReadyRequiresExactlyOneMarkdownAnswerTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{
			name:    "no answer target",
			payload: map[string]any{"body": map[string]any{"elements": []any{map[string]any{"tag": "markdown", "element_id": "message", "content": "message"}}}},
		},
		{
			name:    "duplicate answer targets",
			payload: map[string]any{"body": map[string]any{"elements": []any{map[string]any{"tag": "markdown", "element_id": "answer", "content": "one"}, map[string]any{"tag": "markdown", "element_id": "answer", "content": "two"}}}},
		},
		{
			name:    "wrong answer tag",
			payload: map[string]any{"body": map[string]any{"elements": []any{map[string]any{"tag": "plain_text", "element_id": "answer", "content": "answer"}}}},
		},
		{
			name:    "single markdown answer",
			payload: map[string]any{"body": map[string]any{"elements": []any{map[string]any{"tag": "markdown", "element_id": "answer", "content": "answer"}}}},
			want:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeReadyForPayload(Event{Type: "stream", Streaming: true}, tc.payload); got != tc.want {
				t.Fatalf("nativeReadyForPayload() = %t, want %t", got, tc.want)
			}
		})
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
	prepared, err := PrepareLarkCard(Event{Type: "stream", Streaming: true, Segments: []Segment{{Kind: SegmentText, Text: answer}}})
	if err != nil {
		t.Fatalf("PrepareLarkCard() error: %v", err)
	}
	if !prepared.NativeReady() || prepared.Answer() == "" || !strings.HasPrefix(prepared.Answer(), "answer prefix") {
		t.Fatalf("prepared answer = %q native=%t", prepared.Answer(), prepared.NativeReady())
	}
}

func TestPrepareLarkCardFitsMultipleAnswerSegmentsBeforeEmergencyFallback(t *testing.T) {
	large := "answer prefix " + strings.Repeat("tail ", LarkCardSoftMaxJSONBytes)
	for name, segments := range map[string][]Segment{
		"short error after large answer": {
			{Kind: SegmentText, Text: large},
			{Kind: SegmentError, Text: "short error remains"},
		},
		"two text segments": {
			{Kind: SegmentText, Text: large},
			{Kind: SegmentText, Text: "latest answer remains"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			prepared, err := PrepareLarkCard(Event{Type: "stream", Streaming: true, Segments: segments})
			if err != nil {
				t.Fatalf("PrepareLarkCard() error: %v", err)
			}
			if !prepared.NativeReady() {
				t.Fatalf("prepared an emergency card instead of fitting answer segments: %#v", prepared.EventCopy())
			}
			if got := prepared.Answer(); !strings.Contains(got, "latest answer remains") && !strings.Contains(got, "short error remains") {
				t.Fatalf("answer tail was not retained: %q", got)
			}
		})
	}
}

func TestPrepareLarkCardPreservesOrderedTimeline(t *testing.T) {
	event := Event{
		Type:          "stream",
		Streaming:     true,
		OrderedLayout: true,
		Segments: []Segment{
			{Kind: SegmentText, Text: "leading " + strings.Repeat("text ", LarkCardSoftMaxJSONBytes)},
			{Kind: SegmentTool, Text: "latest tool"},
			{Kind: SegmentText, Text: "final answer"},
			{Kind: SegmentError, Text: "terminal error"},
		},
	}
	prepared, err := PrepareLarkCard(event)
	if err != nil {
		t.Fatalf("PrepareLarkCard() error: %v", err)
	}
	got := prepared.EventCopy().Segments
	positions := map[string]int{}
	for i, segment := range got {
		for _, marker := range []string{"latest tool", "final answer", "terminal error"} {
			if strings.Contains(segment.Text, marker) {
				positions[marker] = i
			}
		}
	}
	if len(positions) != 3 {
		t.Fatalf("prepared ordered segments lost tail markers: %#v", got)
	}
	if !(positions["latest tool"] < positions["final answer"] && positions["final answer"] < positions["terminal error"]) {
		t.Fatalf("prepared ordered segments = %#v", got)
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

func componentElements(n int) []any {
	elements := make([]any, n)
	for i := range elements {
		elements[i] = map[string]any{"tag": "markdown", "content": "x"}
	}
	return elements
}
