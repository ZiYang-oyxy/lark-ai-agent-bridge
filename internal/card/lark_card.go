package card

import (
	"fmt"
	"strings"
)

func BuildLarkCard(e Event) map[string]any {
	elements := make([]any, 0, len(e.Segments)+3)
	elementSeq := 1
	for _, seg := range e.Segments {
		if strings.TrimSpace(seg.Text) == "" {
			continue
		}
		elements = append(elements, map[string]any{
			"tag":        "markdown",
			"element_id": fmt.Sprintf("md_%d", elementSeq),
			"content":    formatSegment(seg),
		})
		elementSeq++
	}
	if e.Message != "" {
		elements = append(elements, map[string]any{
			"tag":        "markdown",
			"element_id": "message",
			"content":    e.Message,
		})
	}
	actions := buildButtonActions(e)
	for _, action := range actions {
		elements = append(elements, action)
	}
	meta := formatMeta(e.Meta)
	if meta != "" {
		elements = append(elements, map[string]any{
			"tag":        "markdown",
			"element_id": "meta",
			"content":    "`" + meta + "`",
		})
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi": true,
			"summary":      map[string]string{"content": titleForEvent(e.Type)},
		},
		"header": map[string]any{
			"template": templateForEvent(e.Type),
			"title":    map[string]any{"tag": "plain_text", "content": titleForEvent(e.Type)},
		},
		"body": map[string]any{
			"direction":        "vertical",
			"vertical_spacing": "8px",
			"padding":          "12px 12px 12px 12px",
			"elements":         elements,
		},
	}
}

func formatSegment(seg Segment) string {
	switch seg.Kind {
	case SegmentThought:
		return "**Thinking**\n" + seg.Text
	case SegmentTool:
		return "**Tool**\n" + seg.Text
	case SegmentError:
		return "**Error**\n" + seg.Text
	default:
		return seg.Text
	}
}

func buildButtonActions(e Event) []any {
	var buttons []any
	for i, action := range e.Actions {
		buttonType := "default"
		if action.ID == "reject" || action.ID == "cancel_workdir" || action.ID == "resume_cancel" || action.ID == "terminate_session" {
			buttonType = "danger"
		}
		buttons = append(buttons, map[string]any{
			"tag":        "button",
			"element_id": fmt.Sprintf("btn_%d", i+1),
			"text":       map[string]any{"tag": "plain_text", "content": action.Label},
			"type":       buttonType,
			"width":      "fill",
			"size":       "medium",
			"disabled":   action.Disabled,
			"behaviors":  callbackBehavior(e.SessionID, action.ID, action.Value),
		})
	}
	if e.StopButton.Visible {
		buttonType := "danger"
		label := "停止"
		if e.StopButton.Disabled {
			buttonType = "default"
			label = "已停止"
		}
		button := map[string]any{
			"tag":        "button",
			"element_id": "btn_stop",
			"text":       map[string]any{"tag": "plain_text", "content": label},
			"type":       buttonType,
			"width":      "fill",
			"size":       "medium",
			"disabled":   e.StopButton.Disabled,
		}
		if !e.StopButton.Disabled {
			button["behaviors"] = callbackBehavior(e.SessionID, "stop", "")
		}
		buttons = append(buttons, button)
	}
	return buttons
}

func callbackBehavior(sessionID, actionID, value string) []any {
	return []any{
		map[string]any{
			"type": "callback",
			"value": map[string]any{
				"action_id": actionID,
				"value":     value,
				"session":   sessionID,
			},
		},
	}
}

func formatMeta(meta Meta) string {
	var parts []string
	if meta.Agent != "" {
		parts = append(parts, "agent="+meta.Agent)
	}
	if meta.Model != "" {
		parts = append(parts, "model="+meta.Model)
	}
	if meta.Tokens > 0 {
		parts = append(parts, fmt.Sprintf("tokens=%d", meta.Tokens))
	}
	if meta.WorkDir != "" {
		parts = append(parts, "workdir="+meta.WorkDir)
	}
	if meta.Status != "" {
		parts = append(parts, "status="+meta.Status)
	}
	return strings.Join(parts, " | ")
}

func titleForEvent(eventType string) string {
	switch eventType {
	case "authorization":
		return "工具授权请求"
	case "choice":
		return "需要你的回答"
	case "resume":
		return "恢复会话"
	case "workdir_confirm":
		return "工作目录确认"
	case "idle_reminder":
		return "会话闲置提醒"
	case "error":
		return "Agent 错误"
	default:
		return "AI Agent"
	}
}

func templateForEvent(eventType string) string {
	switch eventType {
	case "authorization", "workdir_confirm", "idle_reminder":
		return "orange"
	case "error":
		return "red"
	case "choice", "resume":
		return "blue"
	default:
		return "green"
	}
}
