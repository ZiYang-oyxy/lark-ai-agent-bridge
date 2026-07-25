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
	pendingMessage string
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
		flushPendingCodexMessage(&result, state, onEvent, card.SegmentThought)
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
		flushPendingCodexMessage(result, state, onEvent, card.SegmentThought)
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
		segment := card.Segment{Kind: card.SegmentTool, Text: command, Tool: &card.ToolMeta{
			ID: id, Name: "Bash", Summary: command, Phase: "use",
			Input: map[string]any{"command": command},
		}}
		appendCodexSegment(result, segment)
		emitCodexSegment(onEvent, segment, streamActivityTool, false)
	case "item.completed":
		return consumeCodexCompletedItem(codexRecord(event["item"]), result, state, onEvent)
	case "agent_message":
		text := codexString(event["message"])
		if text == "" {
			text = codexString(event["text"])
		}
		bufferCodexAgentMessage(result, state, onEvent, text)
	case "turn.completed":
		flushPendingCodexMessage(result, state, onEvent, card.SegmentText)
		state.terminal = true
		usage := codexRecord(event["usage"])
		tokens := codexInt(usage["input_tokens"]) + codexInt(usage["output_tokens"])
		result.Tokens = tokens
		if tokens > 0 {
			emitStreamUpdate(onEvent, AgentStreamUpdate{Tokens: tokens})
		}
	case "turn.failed":
		flushPendingCodexMessage(result, state, onEvent, card.SegmentThought)
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
	itemType := codexString(item["type"])
	if itemType == "agent_message" {
		text := codexString(item["text"])
		if text == "" {
			text = codexString(item["message"])
		}
		bufferCodexAgentMessage(result, state, onEvent, text)
		return nil
	}
	flushPendingCodexMessage(result, state, onEvent, card.SegmentThought)
	switch itemType {
	case "reasoning", "reasoning_message":
		text := codexString(item["text"])
		if text == "" {
			text = codexString(item["message"])
		}
		appendCodexSegment(result, card.Segment{Kind: card.SegmentThought, Text: text})
		emitCodexSegment(onEvent, card.Segment{Kind: card.SegmentThought, Text: text}, streamActivityReasoning, false)
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
		isError := codexInt(item["exit_code"]) != 0
		segment := card.Segment{Kind: card.SegmentTool, Text: output, Tool: &card.ToolMeta{
			ID: id, Name: "Bash", Phase: "result", IsError: isError, Output: output,
		}}
		appendCodexSegment(result, segment)
		emitCodexSegment(onEvent, segment, streamActivityTool, false)
	}
	return nil
}

// Codex records commentary and the final answer as agent_message items, while
// exec --json currently omits their phase. Keep the newest message pending:
// later activity proves it was commentary, and turn.completed identifies the
// final message without synthesizing a reasoning summary.
func bufferCodexAgentMessage(result *AgentRunResult, state *codexParseState, onEvent func(AgentStreamUpdate), text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	flushPendingCodexMessage(result, state, onEvent, card.SegmentThought)
	state.pendingMessage = text
}

func flushPendingCodexMessage(result *AgentRunResult, state *codexParseState, onEvent func(AgentStreamUpdate), kind card.SegmentKind) {
	text := state.pendingMessage
	state.pendingMessage = ""
	if text == "" {
		return
	}
	if kind == card.SegmentText {
		appendCodexAnswer(result, state, text)
		emitCodexSegment(onEvent, card.Segment{Kind: card.SegmentText, Text: text}, streamActivityAnswering, true)
		return
	}
	segment := card.Segment{Kind: card.SegmentThought, Text: text}
	appendCodexSegment(result, segment)
	emitCodexSegment(onEvent, segment, streamActivityReasoning, false)
}

func appendCodexAnswer(result *AgentRunResult, state *codexParseState, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	result.Segments = append(result.Segments, card.Segment{Kind: card.SegmentText, Text: text})
	result.OrderedSegments = append(result.OrderedSegments, card.Segment{Kind: card.SegmentText, Text: text})
	state.answerSegments = append(state.answerSegments, text)
}

func appendCodexSegment(result *AgentRunResult, segment card.Segment) {
	text := strings.TrimSpace(segment.Text)
	if text != "" {
		segment.Text = text
		result.Segments = append(result.Segments, segment)
		result.OrderedSegments = append(result.OrderedSegments, segment)
	}
}

func emitCodexSegment(onEvent func(AgentStreamUpdate), segment card.Segment, activity string, answerSnapshot bool) {
	text := strings.TrimSpace(segment.Text)
	if text == "" {
		return
	}
	segment.Text = text
	emitStreamUpdate(onEvent, AgentStreamUpdate{Segments: []card.Segment{segment}, Activity: activity, AnswerSnapshot: answerSnapshot})
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
