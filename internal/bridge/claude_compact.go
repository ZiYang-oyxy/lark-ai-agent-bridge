package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"lark-agent-bridge/internal/card"
)

func parseClaudeCompactStream(input io.Reader, copyTo *bytes.Buffer) (AgentRunResult, error) {
	var result AgentRunResult
	sawResult := false
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
			return result, fmt.Errorf("decode claude compact event: %w", err)
		}
		if id, _ := event["session_id"].(string); result.AgentSessionID == "" {
			result.AgentSessionID = strings.TrimSpace(id)
		}
		if compactResult, _ := event["compact_result"].(string); compactResult == "failed" {
			detail, _ := event["compact_error"].(string)
			if strings.TrimSpace(detail) == "" {
				detail = "Claude refused to compact the session"
			}
			return result, errors.New(strings.TrimSpace(detail))
		}
		if eventType, _ := event["type"].(string); eventType == "result" {
			if isError, _ := event["is_error"].(bool); isError {
				detail, _ := event["result"].(string)
				return result, errors.New(strings.TrimSpace(detail))
			}
			sawResult = true
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if !sawResult {
		return result, errors.New("claude compact stream ended without a result")
	}
	result.Segments = []card.Segment{{Kind: card.SegmentText, Text: "Claude 上下文压缩完成。"}}
	return result, nil
}
