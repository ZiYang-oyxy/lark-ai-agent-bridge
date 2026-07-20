package card

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	LarkCardSoftMaxJSONBytes = 28 * 1024
	LarkCardMaxComponents    = 200
)

var (
	ErrCardPayloadOversize     = errors.New("lark card payload exceeds capacity")
	ErrInvalidPreparedLarkCard = errors.New("invalid prepared lark card")
)

type CardCapacity struct {
	Scope                       string
	JSONBytes, Components       int
	MaxJSONBytes, MaxComponents int
}

type PreparedLarkCard struct {
	event       Event
	payload     map[string]any
	json        []byte
	capacity    CardCapacity
	answer      string
	nativeReady bool
	integrity   [32]byte
}

type CardPayloadOversizeError struct {
	Capacity CardCapacity
}

func (e *CardPayloadOversizeError) Error() string {
	return fmt.Sprintf("%s: json_bytes=%d/%d components=%d/%d", ErrCardPayloadOversize, e.Capacity.JSONBytes, e.Capacity.MaxJSONBytes, e.Capacity.Components, e.Capacity.MaxComponents)
}

func (e *CardPayloadOversizeError) Is(target error) bool { return target == ErrCardPayloadOversize }

func MarshalLarkCard(payload map[string]any) ([]byte, CardCapacity, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, CardCapacity{}, fmt.Errorf("marshal lark card: %w", err)
	}
	var encodedPayload any
	if err := json.Unmarshal(encoded, &encodedPayload); err != nil {
		return nil, CardCapacity{}, fmt.Errorf("decode encoded lark card: %w", err)
	}
	capacity := CardCapacity{
		Scope:         "lark_card",
		JSONBytes:     len(encoded),
		Components:    countCardComponents(encodedPayload),
		MaxJSONBytes:  LarkCardSoftMaxJSONBytes,
		MaxComponents: LarkCardMaxComponents,
	}
	if capacity.JSONBytes > capacity.MaxJSONBytes || capacity.Components > capacity.MaxComponents {
		return encoded, capacity, &CardPayloadOversizeError{Capacity: capacity}
	}
	return encoded, capacity, nil
}

func PrepareLarkCard(event Event) (PreparedLarkCard, error) {
	event = cloneEvent(event)
	prepared, err := prepareLarkCard(event, true)
	if err == nil {
		return prepared, nil
	}
	if !errors.Is(err, ErrCardPayloadOversize) {
		return PreparedLarkCard{}, err
	}
	if prepared, ok := fitLarkCard(event); ok {
		return prepared, nil
	}
	return PrepareEmergencyLarkCard()
}

func prepareLarkCard(event Event, nativeReady bool) (PreparedLarkCard, error) {
	event = normalizeTerminalEvent(event)
	payload := BuildLarkCard(event)
	encoded, capacity, err := MarshalLarkCard(payload)
	if err != nil {
		return PreparedLarkCard{}, err
	}
	answer, _, _ := splitCardSections(event.Segments)
	if event.MarkdownLayout {
		answer = event.Markdown
	}
	prepared := PreparedLarkCard{
		event:       event,
		payload:     clonePayload(payload),
		json:        append([]byte(nil), encoded...),
		capacity:    capacity,
		answer:      answer,
		nativeReady: nativeReady && nativeReadyForPayload(event, payload),
	}
	prepared.integrity = preparedIntegrity(prepared)
	return prepared, nil
}

func nativeReadyForPayload(event Event, payload map[string]any) bool {
	if !event.Streaming {
		return false
	}
	body, ok := payload["body"].(map[string]any)
	if !ok {
		return false
	}
	elements, ok := body["elements"].([]any)
	if !ok {
		return false
	}
	answerTargets := 0
	for _, raw := range elements {
		element, ok := raw.(map[string]any)
		if !ok || element["element_id"] != "answer" {
			continue
		}
		if element["tag"] != "markdown" {
			return false
		}
		answerTargets++
	}
	return answerTargets == 1
}

func fitLarkCard(event Event) (PreparedLarkCard, bool) {
	if prepared, err := prepareLarkCard(event, true); err == nil {
		return prepared, true
	}

	for countSegments(event.Segments, SegmentTool) > 1 {
		event.Segments = dropOldestSegment(event.Segments, SegmentTool)
		if prepared, err := prepareLarkCard(event, true); err == nil {
			return prepared, true
		}
	}
	for countSegments(event.Segments, SegmentThought) > 1 {
		event.Segments = dropOldestSegment(event.Segments, SegmentThought)
		if prepared, err := prepareLarkCard(event, true); err == nil {
			return prepared, true
		}
	}
	if _, prepared, ok := shrinkLatestSegment(event, SegmentTool, true); ok {
		return prepared, true
	}
	// The final thought is also the latest activity when no tool output is present.
	if _, prepared, ok := shrinkLatestSegment(event, SegmentThought, true); ok {
		return prepared, true
	}
	if _, prepared, ok := shrinkLatestAnswer(event); ok {
		return prepared, true
	}
	if event.Message != "" {
		if _, prepared, ok := shrinkMessage(event); ok {
			return prepared, true
		}
	}
	return PreparedLarkCard{}, false
}

func countSegments(segments []Segment, kind SegmentKind) int {
	count := 0
	for _, segment := range segments {
		if segment.Kind == kind && strings.TrimSpace(segment.Text) != "" {
			count++
		}
	}
	return count
}

func dropOldestSegment(segments []Segment, kind SegmentKind) []Segment {
	for i, segment := range segments {
		if segment.Kind == kind && strings.TrimSpace(segment.Text) != "" {
			out := make([]Segment, 0, len(segments)-1)
			out = append(out, segments[:i]...)
			return append(out, segments[i+1:]...)
		}
	}
	return segments
}

func shrinkLatestSegment(event Event, kind SegmentKind, keepTail bool) (Event, PreparedLarkCard, bool) {
	for i := len(event.Segments) - 1; i >= 0; i-- {
		if event.Segments[i].Kind != kind || strings.TrimSpace(event.Segments[i].Text) == "" {
			continue
		}
		return shrinkSegment(event, i, keepTail)
	}
	return event, PreparedLarkCard{}, false
}

func shrinkLatestAnswer(event Event) (Event, PreparedLarkCard, bool) {
	indices := answerSegmentIndices(event.Segments)
	if len(indices) == 0 {
		return event, PreparedLarkCard{}, false
	}

	// Reserve a readable placeholder for every answer section first. This lets a
	// short final error or answer survive while earlier large sections are reduced.
	candidate := cloneEvent(event)
	for _, index := range indices {
		candidate.Segments[index].Text = "…"
	}
	prepared, err := prepareLarkCard(candidate, true)
	if err != nil {
		return event, PreparedLarkCard{}, false
	}

	// Prefer the answer tail, then use any remaining capacity for earlier text.
	for i := len(indices) - 1; i >= 0; i-- {
		index := indices[i]
		original := []rune(event.Segments[index].Text)
		candidate, prepared = maximizeSegment(candidate, index, original, false, prepared)
	}
	return candidate, prepared, true
}

func answerSegmentIndices(segments []Segment) []int {
	indices := make([]int, 0, len(segments))
	for index, segment := range segments {
		if segment.Kind == SegmentText || segment.Kind == SegmentError {
			indices = append(indices, index)
		}
	}
	return indices
}

func shrinkSegment(event Event, index int, keepTail bool) (Event, PreparedLarkCard, bool) {
	original := []rune(event.Segments[index].Text)
	if len(original) == 0 {
		return event, PreparedLarkCard{}, false
	}
	bestEvent, best := maximizeSegment(event, index, original, keepTail, PreparedLarkCard{})
	if len(best.json) == 0 {
		return event, PreparedLarkCard{}, false
	}
	return bestEvent, best, true
}

func maximizeSegment(event Event, index int, original []rune, keepTail bool, initial PreparedLarkCard) (Event, PreparedLarkCard) {
	bestEvent, best := event, initial
	low, high := 0, len(original)
	for low <= high {
		mid := (low + high) / 2
		candidate := cloneEvent(event)
		candidate.Segments[index].Text = truncatedRunes(original, mid, keepTail)
		prepared, err := prepareLarkCard(candidate, true)
		if err == nil {
			best, bestEvent = prepared, candidate
			low = mid + 1
			continue
		}
		high = mid - 1
	}
	return bestEvent, best
}

func shrinkMessage(event Event) (Event, PreparedLarkCard, bool) {
	original := []rune(event.Message)
	var best PreparedLarkCard
	bestEvent := event
	low, high := 0, len(original)
	for low <= high {
		mid := (low + high) / 2
		candidate := cloneEvent(event)
		candidate.Message = truncatedRunes(original, mid, true)
		prepared, err := prepareLarkCard(candidate, true)
		if err == nil {
			best, bestEvent = prepared, candidate
			low = mid + 1
			continue
		}
		high = mid - 1
	}
	if len(best.json) == 0 {
		return event, PreparedLarkCard{}, false
	}
	return bestEvent, best, true
}

func truncatedRunes(value []rune, keep int, keepTail bool) string {
	if keep >= len(value) {
		return string(value)
	}
	if keep <= 0 {
		return "…"
	}
	if keepTail {
		return "…" + string(value[len(value)-keep:])
	}
	return string(value[:keep]) + "…"
}

func PrepareEmergencyLarkCard() (PreparedLarkCard, error) {
	payload := BuildLarkCard(Event{
		Type:            "error",
		HeaderTitle:     "卡片内容已简化",
		HeaderTemplate:  "orange",
		Segments:        []Segment{{Kind: SegmentText, Text: "内容过长，已省略。请稍后查看下一次更新。"}},
		HideAgentPanels: true,
	})
	encoded, capacity, err := MarshalLarkCard(payload)
	if err != nil {
		return PreparedLarkCard{}, fmt.Errorf("prepare emergency lark card: %w", err)
	}
	prepared := PreparedLarkCard{
		payload:     clonePayload(payload),
		json:        append([]byte(nil), encoded...),
		capacity:    capacity,
		answer:      "",
		nativeReady: false,
	}
	prepared.integrity = preparedIntegrity(prepared)
	return prepared, nil
}

func ValidatePreparedLarkCard(prepared PreparedLarkCard) error {
	if len(prepared.json) == 0 || prepared.capacity.Scope != "lark_card" || prepared.capacity.JSONBytes != len(prepared.json) || prepared.capacity.MaxJSONBytes != LarkCardSoftMaxJSONBytes || prepared.capacity.MaxComponents != LarkCardMaxComponents || prepared.capacity.JSONBytes > LarkCardSoftMaxJSONBytes || prepared.capacity.Components > LarkCardMaxComponents || prepared.integrity != preparedIntegrity(prepared) {
		return ErrInvalidPreparedLarkCard
	}
	return nil
}

func (p PreparedLarkCard) EventCopy() Event { return cloneEvent(p.event) }

func (p PreparedLarkCard) PayloadCopy() map[string]any { return clonePayload(p.payload) }

func (p PreparedLarkCard) CardJSON() []byte { return append([]byte(nil), p.json...) }

func (p PreparedLarkCard) Capacity() CardCapacity { return p.capacity }

func (p PreparedLarkCard) Answer() string { return p.answer }

func (p PreparedLarkCard) NativeReady() bool { return p.nativeReady }

func preparedIntegrity(prepared PreparedLarkCard) [32]byte {
	hash := sha256.New()
	_, _ = hash.Write(prepared.json)
	metadata, _ := json.Marshal(struct {
		Capacity    CardCapacity
		Answer      string
		NativeReady bool
	}{prepared.capacity, prepared.answer, prepared.nativeReady})
	_, _ = hash.Write(metadata)
	var integrity [32]byte
	copy(integrity[:], hash.Sum(nil))
	return integrity
}

func countCardComponents(value any) int {
	switch value := value.(type) {
	case []any:
		count := 0
		for _, item := range value {
			count += countCardComponents(item)
		}
		return count
	case map[string]any:
		count := 0
		if _, ok := value["tag"].(string); ok {
			count++
		}
		for _, item := range value {
			count += countCardComponents(item)
		}
		return count
	default:
		return 0
	}
}

func clonePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("clone lark payload: %v", err))
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		panic(fmt.Sprintf("clone lark payload: %v", err))
	}
	return cloned
}

func cloneEvent(event Event) Event {
	cloned := event
	cloned.Segments = append([]Segment(nil), event.Segments...)
	cloned.Actions = append([]Action(nil), event.Actions...)
	if event.AgentModeForm != nil {
		form := *event.AgentModeForm
		form.Agents = append([]SelectOption(nil), event.AgentModeForm.Agents...)
		cloned.AgentModeForm = &form
	}
	if event.ConfigForm != nil {
		form := *event.ConfigForm
		form.Agents = append([]SelectOption(nil), event.ConfigForm.Agents...)
		form.AgentHomes = append([]SelectOption(nil), event.ConfigForm.AgentHomes...)
		form.AgentBins = append([]SelectOption(nil), event.ConfigForm.AgentBins...)
		form.Models = append([]string(nil), event.ConfigForm.Models...)
		form.Efforts = append([]string(nil), event.ConfigForm.Efforts...)
		form.ReplyModes = append([]string(nil), event.ConfigForm.ReplyModes...)
		form.ConversationModes = append([]string(nil), event.ConfigForm.ConversationModes...)
		cloned.ConfigForm = &form
	}
	return cloned
}
