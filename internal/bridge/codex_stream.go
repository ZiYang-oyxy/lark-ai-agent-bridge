package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"lark-agent-bridge/internal/card"
)

type codexParseState struct {
	threadID       string
	terminal       bool
	lastError      string
	startedItems   map[string]struct{}
	answerSegments []string
	unknownEvents  int
	anomalies      int
}

func parseCodexStream(input io.Reader, copyTo *bytes.Buffer, onEvent func(AgentStreamUpdate)) (AgentRunResult, error) {
	state := &codexParseState{startedItems: map[string]struct{}{}}
	var result AgentRunResult
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if copyTo != nil {
			copyTo.WriteString(line)
			copyTo.WriteByte('\n')
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			state.anomalies++
			continue
		}
		if err := consumeCodexEvent(event, &result, state, onEvent); err != nil {
			result.ProtocolUnknown = state.unknownEvents
			result.ProtocolAnomalies = state.anomalies
			return result, err
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	result.AgentSessionID = state.threadID
	result.AnswerSegments = append([]string(nil), state.answerSegments...)
	result.ToolCallCount = len(state.startedItems)
	result.ProtocolUnknown = state.unknownEvents
	result.ProtocolAnomalies = state.anomalies
	if !state.terminal {
		detail := ""
		if state.lastError != "" {
			detail = ": " + state.lastError
		}
		return result, fmt.Errorf("codex stream ended before a terminal event%s", detail)
	}
	return result, nil
}

func consumeCodexEvent(event map[string]any, result *AgentRunResult, state *codexParseState, onEvent func(AgentStreamUpdate)) error {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "thread.started":
		threadID := codexString(event["thread_id"])
		if threadID == "" {
			threadID = codexString(event["threadId"])
		}
		if threadID == "" {
			state.anomalies++
			return nil
		}
		state.threadID = threadID
		result.AgentSessionID = threadID
		emitStreamUpdate(onEvent, AgentStreamUpdate{AgentSessionID: threadID})
	case "turn.started":
		return nil
	case "item.started":
		item := codexRecord(event["item"])
		if codexString(item["type"]) != "command_execution" {
			return nil
		}
		id := codexString(item["id"])
		if id == "" {
			state.anomalies++
			return nil
		}
		state.startedItems[id] = struct{}{}
		command := codexString(item["command"])
		appendCodexSegment(result, card.SegmentTool, command)
		emitCodexSegment(onEvent, card.SegmentTool, command, streamActivityTool, false)
	case "item.completed":
		return consumeCodexCompletedItem(codexRecord(event["item"]), result, state, onEvent)
	case "agent_message":
		text := codexString(event["message"])
		if text == "" {
			text = codexString(event["text"])
		}
		appendCodexAnswer(result, state, text)
		emitCodexSegment(onEvent, card.SegmentText, text, streamActivityAnswering, true)
	case "turn.completed":
		state.terminal = true
		usage := codexRecord(event["usage"])
		tokens := codexInt(usage["input_tokens"]) + codexInt(usage["output_tokens"])
		result.Tokens = tokens
		if tokens > 0 {
			emitStreamUpdate(onEvent, AgentStreamUpdate{Tokens: tokens})
		}
	case "turn.failed":
		state.terminal = true
		return fmt.Errorf("%s", codexErrorMessage(event, "codex turn failed"))
	case "error":
		state.lastError = codexErrorMessage(event, "codex error")
	case "":
		state.anomalies++
	default:
		state.unknownEvents++
	}
	return nil
}

func consumeCodexCompletedItem(item map[string]any, result *AgentRunResult, state *codexParseState, onEvent func(AgentStreamUpdate)) error {
	switch codexString(item["type"]) {
	case "agent_message":
		text := codexString(item["text"])
		if text == "" {
			text = codexString(item["message"])
		}
		appendCodexAnswer(result, state, text)
		emitCodexSegment(onEvent, card.SegmentText, text, streamActivityAnswering, true)
	case "reasoning", "reasoning_message":
		text := codexString(item["text"])
		if text == "" {
			text = codexString(item["message"])
		}
		appendCodexSegment(result, card.SegmentThought, text)
		emitCodexSegment(onEvent, card.SegmentThought, text, streamActivityReasoning, false)
	case "command_execution":
		id := codexString(item["id"])
		if id == "" {
			state.anomalies++
			return nil
		}
		if _, ok := state.startedItems[id]; !ok {
			state.anomalies++
			state.startedItems[id] = struct{}{}
		}
		output := codexString(item["output"])
		if output == "" {
			output = codexString(item["aggregated_output"])
		}
		if output == "" {
			output = codexString(item["stdout"])
		}
		appendCodexSegment(result, card.SegmentTool, output)
		emitCodexSegment(onEvent, card.SegmentTool, output, streamActivityTool, false)
	}
	return nil
}

func appendCodexAnswer(result *AgentRunResult, state *codexParseState, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	result.Segments = append(result.Segments, card.Segment{Kind: card.SegmentText, Text: text})
	state.answerSegments = append(state.answerSegments, text)
}

func appendCodexSegment(result *AgentRunResult, kind card.SegmentKind, text string) {
	text = strings.TrimSpace(text)
	if text != "" {
		result.Segments = append(result.Segments, card.Segment{Kind: kind, Text: text})
	}
}

func emitCodexSegment(onEvent func(AgentStreamUpdate), kind card.SegmentKind, text, activity string, answerSnapshot bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	emitStreamUpdate(onEvent, AgentStreamUpdate{Segments: []card.Segment{{Kind: kind, Text: text}}, Activity: activity, AnswerSnapshot: answerSnapshot})
}

func codexRecord(value any) map[string]any {
	record, _ := value.(map[string]any)
	return record
}

func codexString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func codexInt(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case json.Number:
		value, _ := number.Int64()
		return int(value)
	default:
		return 0
	}
}

func codexErrorMessage(event map[string]any, fallback string) string {
	if message := codexString(event["message"]); message != "" {
		return message
	}
	errorValue := event["error"]
	if nested := codexRecord(errorValue); nested != nil {
		if message := codexString(nested["message"]); message != "" {
			return message
		}
	}
	if message := codexString(errorValue); message != "" {
		return message
	}
	return fallback
}
