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
	if event.MarkdownLayout || event.InlineTimelineLayout {
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
	if event.MarkdownLayout {
		if prepared, ok := fitMarkdownCardTail(event); ok {
			return prepared, true
		}
		return PreparedLarkCard{}, false
	}
	if event.InlineTimelineLayout {
		if prepared, ok := fitInlineTimelineCard(event); ok {
			return prepared, true
		}
		return PreparedLarkCard{}, false
	}

	// 三段布局下思考/工具块内部已经用分隔线区分最近2条,dropOldestSegment 会删除整个块导致
	// 历史全部丢失。当独立段数超过1个时才删(三段布局下各1个块,不会触发此删除;其它布局下
	// 每个工具/思考是独立段,超过1个就删最旧的,保持原有行为)。
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
	// 如果是三段布局(单个思考/工具块内含多条历史,用 --- 分隔),优先收缩块内最旧的历史内容,
	// 保留最新部分,而不是直接截断整个块的尾部导致最新内容丢失。
	if event.ThreeSectionLayout {
		// 收缩思考块:优先保留分隔线后的最新部分
		if idx := findLatestSegmentIndex(event.Segments, SegmentThought); idx >= 0 {
			if _, prepared, ok := shrinkSegmentKeepTail(event, idx, 1500, true); ok {
				return prepared, true
			}
		}
		// 收缩工具块:优先保留分隔线后的最新部分
		if idx := findLatestSegmentIndex(event.Segments, SegmentTool); idx >= 0 {
			if _, prepared, ok := shrinkSegmentKeepTail(event, idx, 4000, true); ok {
				return prepared, true
			}
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

func fitMarkdownCardTail(event Event) (PreparedLarkCard, bool) {
	original := []rune(event.Markdown)
	if len(original) == 0 {
		return PreparedLarkCard{}, false
	}
	const notice = "_较早过程已省略_\n\n"
	var best PreparedLarkCard
	low, high := 0, len(original)
	for low <= high {
		mid := (low + high) / 2
		candidate := cloneEvent(event)
		candidate.Markdown = notice + string(original[len(original)-mid:])
		prepared, err := prepareLarkCard(candidate, true)
		if err == nil {
			best = prepared
			low = mid + 1
			continue
		}
		high = mid - 1
	}
	if len(best.json) == 0 {
		return PreparedLarkCard{}, false
	}
	return best, true
}

func fitInlineTimelineCard(event Event) (PreparedLarkCard, bool) {
	candidate := cloneEvent(event)
	latestThought := -1
	for index := range candidate.Segments {
		if candidate.Segments[index].Kind == SegmentThought && strings.TrimSpace(candidate.Segments[index].Text) != "" {
			candidate.Segments[index].Text = ""
			latestThought = index
		}
	}
	if latestThought >= 0 {
		candidate.Segments[latestThought].Text = "…"
	}

	fittedEvent, prepared, ok := shrinkInlineMarkdown(candidate)
	if !ok {
		return PreparedLarkCard{}, false
	}
	if latestThought >= 0 {
		_, prepared = maximizeSegment(fittedEvent, latestThought, []rune(event.Segments[latestThought].Text), true, prepared)
	}
	return prepared, true
}

func shrinkInlineMarkdown(event Event) (Event, PreparedLarkCard, bool) {
	if prepared, err := prepareLarkCard(event, true); err == nil {
		return event, prepared, true
	}
	parts := strings.Split(event.Markdown, "\n\n")
	if len(parts) == 0 {
		return event, PreparedLarkCard{}, false
	}
	const notice = "_较早过程已省略_"
	var bestEvent Event
	var best PreparedLarkCard
	low, high := 1, len(parts)-1
	for low <= high {
		mid := (low + high) / 2
		candidate := cloneEvent(event)
		candidate.Markdown = notice + "\n\n" + strings.Join(parts[mid:], "\n\n")
		prepared, err := prepareLarkCard(candidate, true)
		if err == nil {
			bestEvent, best = candidate, prepared
			high = mid - 1
			continue
		}
		low = mid + 1
	}
	if len(best.json) != 0 {
		return bestEvent, best, true
	}

	finalPart := []rune(parts[len(parts)-1])
	low, high = 0, len(finalPart)
	for low <= high {
		mid := (low + high) / 2
		candidate := cloneEvent(event)
		candidate.Markdown = notice + "\n\n" + truncatedRunes(finalPart, mid, false)
		prepared, err := prepareLarkCard(candidate, true)
		if err == nil {
			bestEvent, best = candidate, prepared
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

// findLatestSegmentIndex 返回最后一个匹配 kind 的非空 segment 索引,找不到返回 -1
func findLatestSegmentIndex(segments []Segment, kind SegmentKind) int {
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i].Kind == kind && strings.TrimSpace(segments[i].Text) != "" {
			return i
		}
	}
	return -1
}

// shrinkSegmentKeepTail 收缩指定索引的 segment,优先保留尾部内容(keepTail=true),
// 内容中包含 --- 分隔线时优先保留最后一段分隔线后的最新内容,而不是简单从尾截断。
func shrinkSegmentKeepTail(event Event, index int, maxRunes int, keepTail bool) (Event, PreparedLarkCard, bool) {
	if index < 0 || index >= len(event.Segments) {
		return event, PreparedLarkCard{}, false
	}
	text := event.Segments[index].Text
	runes := []rune(text)
	if len(runes) <= maxRunes {
		// 已经够短,直接尝试prepare
		if prepared, err := prepareLarkCard(event, true); err == nil {
			return event, prepared, true
		}
		return event, PreparedLarkCard{}, false
	}

	// 尝试从最后一个 --- 分隔线截断,优先保留最新的部分(最后一个分隔线后的内容)
	const separator = "\n\n---\n\n"
	sepIdx := strings.LastIndex(text, separator)
	candidate := cloneEvent(event)
	if sepIdx >= 0 {
		// 有分隔线:只保留分隔线后的最新内容,加上省略提示
		latestPart := text[sepIdx+len(separator):]
		if len([]rune(latestPart)) <= maxRunes {
			candidate.Segments[index].Text = "…\n\n" + latestPart
		} else {
			// 最新部分本身就超长,按尾部截断
			candidate.Segments[index].Text = truncatedRunes(runes, maxRunes, keepTail)
		}
	} else {
		// 无分隔线:按普通尾部截断
		candidate.Segments[index].Text = truncatedRunes(runes, maxRunes, keepTail)
	}

	if prepared, err := prepareLarkCard(candidate, true); err == nil {
		return candidate, prepared, true
	}
	// 收缩失败,退回到普通shrink逻辑
	return shrinkSegment(event, index, keepTail)
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
		form.TopicSeedModes = append([]string(nil), event.ConfigForm.TopicSeedModes...)
		cloned.ConfigForm = &form
	}
	return cloned
}
