package card

import (
	"fmt"
	"strings"
)

func BuildLarkCard(e Event) map[string]any {
	elements := make([]any, 0, len(e.Segments)+5)
	answer, thought, tools := splitCardSections(e.Segments)
	if strings.TrimSpace(answer) != "" {
		elements = append(elements, markdownElement("answer", answer))
	}
	if e.Message != "" {
		elements = append(elements, markdownElement("message", e.Message))
	}
	if shouldShowAgentPanels(e, thought, tools) {
		elements = append(elements,
			collapsiblePanelElement("panel_thought", panelTitle("思考推理", thought), false, []map[string]any{
				markdownElement("thought", defaultPanelText(thought, "暂无思考推理内容。")),
			}),
			collapsiblePanelElement("panel_tools", panelTitle("工具调用", tools), false, []map[string]any{
				markdownElement("tools", defaultPanelText(tools, "暂无工具调用。")),
			}),
		)
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

func splitCardSections(segments []Segment) (string, string, string) {
	var answer strings.Builder
	var thought strings.Builder
	var tools strings.Builder
	write := func(b *strings.Builder, text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
	}
	for _, seg := range segments {
		switch seg.Kind {
		case SegmentThought:
			write(&thought, seg.Text)
		case SegmentTool:
			write(&tools, seg.Text)
		case SegmentError:
			write(&answer, "**Error**\n"+seg.Text)
		default:
			write(&answer, seg.Text)
		}
	}
	return answer.String(), thought.String(), tools.String()
}

func shouldShowAgentPanels(e Event, thought, tools string) bool {
	if strings.TrimSpace(thought) != "" || strings.TrimSpace(tools) != "" {
		return true
	}
	return e.Type == "result"
}

func defaultPanelText(text, fallback string) string {
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return text
}

func panelTitle(title, content string) string {
	if strings.TrimSpace(content) == "" {
		return title + "（暂无，点击展开）"
	}
	return title + "（点击展开）"
}

func markdownElement(id, content string) map[string]any {
	return map[string]any{
		"tag":        "markdown",
		"element_id": id,
		"content":    content,
	}
}

func collapsiblePanelElement(id, title string, expanded bool, elements []map[string]any) map[string]any {
	return map[string]any{
		"tag":              "collapsible_panel",
		"element_id":       id,
		"expanded":         expanded,
		"vertical_spacing": "8px",
		"padding":          "8px 8px 8px 8px",
		"header": map[string]any{
			"title": map[string]string{
				"tag":     "plain_text",
				"content": title,
			},
			"vertical_align": "center",
			"padding":        "4px 0px 4px 8px",
			"width":          "auto_when_fold",
			"icon": map[string]string{
				"tag":   "standard_icon",
				"token": "down-small-ccm_outlined",
				"color": "",
				"size":  "16px 16px",
			},
			"icon_position":       "right",
			"icon_expanded_angle": -180,
		},
		"border": map[string]string{
			"color":         "grey",
			"corner_radius": "5px",
		},
		"elements": elements,
	}
}

func buildButtonActions(e Event) []any {
	var buttons []any
	for i, action := range e.Actions {
		buttonType := "default"
		if action.ID == "cancel_workdir" {
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
	case "workdir_confirm":
		return "工作目录确认"
	case "error":
		return "Agent 错误"
	default:
		return "AI Agent"
	}
}

func templateForEvent(eventType string) string {
	switch eventType {
	case "workdir_confirm":
		return "orange"
	case "error":
		return "red"
	default:
		return "green"
	}
}
