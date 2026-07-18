package card

import (
	"fmt"
	"strings"
)

func BuildLarkCard(e Event) map[string]any {
	var elements []any
	if e.ConfigForm != nil {
		e.Streaming = false
		elements = buildConfigFormElements(e.SessionID, *e.ConfigForm)
	} else {
		elements = make([]any, 0, len(e.Segments)+5)
		answer, thought, tools := splitCardSections(e.Segments)
		if strings.TrimSpace(answer) != "" {
			elements = append(elements, markdownElement("answer", answer))
		}
		if e.Message != "" {
			elements = append(elements, markdownElement("message", e.Message))
		}
		if shouldShowAgentPanels(e, thought, tools) {
			elements = append(elements,
				collapsiblePanelElement("panel_thought", thoughtPanelTitle(e, thought), e.ThoughtExpanded, []map[string]any{
					markdownElement("thought", defaultPanelText(thought, "等待模型输出思考或推理内容。")),
				}),
				collapsiblePanelElement("panel_tools", toolsPanelTitle(e, tools), e.ToolsExpanded, []map[string]any{
					markdownElement("tools", defaultPanelText(tools, "暂无工具调用。")),
				}),
			)
		}
		for _, action := range buildButtonActions(e) {
			elements = append(elements, action)
		}
		elements = append(elements, buildMetaElements(e.Meta)...)
	}
	title := headerTitle(e)
	payload := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":   true,
			"streaming_mode": e.Streaming,
			"summary":        map[string]string{"content": title},
		},
		"header": map[string]any{
			"template": headerTemplate(e),
			"title":    map[string]any{"tag": "plain_text", "content": title},
		},
		"body": map[string]any{
			"direction":        "vertical",
			"vertical_spacing": "8px",
			"padding":          "12px 12px 12px 12px",
			"elements":         elements,
		},
	}
	if e.Streaming {
		config := payload["config"].(map[string]any)
		config["streaming_config"] = map[string]any{
			"print_frequency_ms": map[string]int{"default": 80, "android": 80, "ios": 80, "pc": 80},
			"print_step":         map[string]int{"default": 1, "android": 1, "ios": 1, "pc": 1},
			"print_strategy":     "fast",
		}
	}
	return payload
}

func buildConfigFormElements(sessionID string, form ConfigForm) []any {
	return []any{
		markdownElement("config_intro", "⚙️ **个人运行偏好**\n\n修改后只影响新进入队列的消息。"),
		map[string]any{
			"tag":  "form",
			"name": "runtime_config",
			"elements": []any{
				markdownElement("config_model_label", "**Model**\n`default` 表示由 Claude CLI / wrapper 决定。"),
				configSelect("model", form.Model, form.Models),
				markdownElement("config_effort_label", "**Effort**\n`default` 表示不传 `--effort`。"),
				configSelect("effort", form.Effort, form.Efforts),
				map[string]any{
					"tag":              "button",
					"name":             "submit_runtime_config",
					"text":             map[string]any{"tag": "plain_text", "content": "保存"},
					"type":             "primary",
					"form_action_type": "submit",
					"behaviors":        callbackBehavior(sessionID, "config.save", ""),
				},
			},
		},
	}
}

func configSelect(name, initial string, values []string) map[string]any {
	options := make([]any, 0, len(values))
	for _, value := range values {
		options = append(options, map[string]any{
			"text":  map[string]any{"tag": "plain_text", "content": value},
			"value": value,
		})
	}
	return map[string]any{
		"tag":            "select_static",
		"name":           name,
		"initial_option": initial,
		"options":        options,
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
	return e.Streaming || e.Type == "result" || e.Type == "stopped" || e.Type == "error"
}

func defaultPanelText(text, fallback string) string {
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return text
}

func thoughtPanelTitle(e Event, content string) string {
	if e.ThoughtExpanded {
		return "思考推理（生成中，点击收起）"
	}
	if strings.TrimSpace(content) != "" {
		return "思考推理（已完成，点击展开）"
	}
	return "思考推理（暂无，点击展开）"
}

func toolsPanelTitle(e Event, content string) string {
	if e.ToolsExpanded {
		return "工具调用（执行中，点击收起）"
	}
	if strings.TrimSpace(content) == "" {
		return "工具调用（暂无，点击展开）"
	}
	return "工具调用（点击展开）"
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
		button := map[string]any{
			"tag":        "button",
			"element_id": fmt.Sprintf("btn_%d", i+1),
			"text":       map[string]any{"tag": "plain_text", "content": action.Label},
			"type":       buttonType,
			"width":      "fill",
			"size":       "medium",
			"disabled":   action.Disabled,
		}
		if !action.Disabled {
			button["behaviors"] = callbackBehavior(e.SessionID, action.ID, action.Value)
		}
		buttons = append(buttons, button)
	}
	if e.StopButton.Visible {
		buttonType := "danger"
		label := stopButtonLabel(e)
		if e.StopButton.Disabled {
			buttonType = "default"
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

func stopButtonLabel(e Event) string {
	if !e.StopButton.Disabled {
		return "停止"
	}
	switch e.Type {
	case "result":
		return "已完成"
	case "error":
		return "已结束"
	case "stopped":
		return "已停止"
	default:
		return "已停止"
	}
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

type weightedMetaCell struct {
	Weight  int
	Content string
	ID      string
}

func buildMetaElements(meta Meta) []any {
	firstRow, secondRow := metaRows(meta)
	if len(firstRow) == 0 && len(secondRow) == 0 {
		return nil
	}
	elements := []any{map[string]any{"tag": "hr"}}
	if len(firstRow) > 0 {
		elements = append(elements, columnSetElement("meta_primary", firstRow))
	}
	if len(secondRow) > 0 {
		elements = append(elements, columnSetElement("meta_runtime", secondRow))
	}
	return elements
}

func metaRows(meta Meta) ([]weightedMetaCell, []weightedMetaCell) {
	var first []weightedMetaCell
	if meta.Agent != "" {
		first = append(first, weightedMetaCell{Weight: 10, Content: "🤖 " + displayAgent(meta.Agent), ID: "meta_agent"})
	}
	if meta.ModelInfo != (ModelInfo{}) {
		requested := meta.ModelInfo.Requested
		if requested == "" {
			requested = "unknown"
		}
		actual := meta.ModelInfo.Actual
		if actual == "" {
			actual = "unknown"
		}
		effort := meta.ModelInfo.Effort
		if effort == "" {
			effort = "unknown"
		}
		first = append(first, weightedMetaCell{Weight: 30, Content: fmt.Sprintf("🧠 requested: %s · actual: %s · effort: %s", requested, actual, effort), ID: "meta_model"})
	} else if meta.Model != "" {
		first = append(first, weightedMetaCell{Weight: 14, Content: "🧠 " + meta.Model, ID: "meta_model"})
	}
	runTokens := meta.RunTokens
	totalTokens := meta.TotalTokens
	if runTokens == 0 && meta.Tokens > 0 {
		runTokens = meta.Tokens
	}
	if totalTokens == 0 && meta.Tokens > 0 {
		totalTokens = meta.Tokens
	}
	if runTokens > 0 || totalTokens > 0 {
		if totalTokens > 0 {
			first = append(first, weightedMetaCell{Weight: 18, Content: fmt.Sprintf("🔢 tokens: ▶ %s / ∑ %s", compactInt(runTokens), compactInt(totalTokens)), ID: "meta_tokens"})
		} else {
			first = append(first, weightedMetaCell{Weight: 18, Content: fmt.Sprintf("🔢 tokens: ▶ %s", compactInt(runTokens)), ID: "meta_tokens"})
		}
	}
	var second []weightedMetaCell
	if meta.User != "" {
		second = append(second, weightedMetaCell{Weight: 10, Content: "👤 " + meta.User, ID: "meta_user"})
	}
	if meta.IP != "" {
		second = append(second, weightedMetaCell{Weight: 12, Content: "🖥️ " + meta.IP, ID: "meta_ip"})
	}
	if meta.WorkDir != "" {
		second = append(second, weightedMetaCell{Weight: 30, Content: "📁 `" + meta.WorkDir + "`", ID: "meta_workdir"})
	}
	return first, second
}

func columnSetElement(id string, cells []weightedMetaCell) map[string]any {
	columns := make([]any, 0, len(cells))
	for _, cell := range cells {
		columns = append(columns, map[string]any{
			"tag":            "column",
			"width":          "weighted",
			"weight":         cell.Weight,
			"vertical_align": "top",
			"elements": []any{
				markdownElement(cell.ID, cell.Content),
			},
		})
	}
	return map[string]any{
		"tag":                "column_set",
		"element_id":         id,
		"flex_mode":          "none",
		"horizontal_spacing": "8px",
		"columns":            columns,
	}
}

func displayAgent(agent string) string {
	switch strings.ToLower(agent) {
	case "claude":
		return "Claude"
	default:
		return agent
	}
}

func compactInt(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	value := float64(n) / 1000
	text := fmt.Sprintf("%.1fk", value)
	if strings.HasSuffix(text, ".0k") {
		return strings.TrimSuffix(text, ".0k") + "k"
	}
	return text
}

func titleForEvent(eventType string) string {
	switch eventType {
	case "workdir_confirm":
		return "工作目录确认"
	case "workdir_created":
		return "✅ 工作目录已创建"
	case "workdir_cancelled":
		return "⏹ 已取消"
	case "error":
		return "Agent 错误"
	case "config":
		return "个人运行偏好"
	default:
		return "AI Agent"
	}
}

func templateForEvent(eventType string) string {
	switch eventType {
	case "workdir_confirm":
		return "orange"
	case "workdir_created":
		return "green"
	case "workdir_cancelled":
		return "grey"
	case "error":
		return "red"
	case "config":
		return "blue"
	default:
		return "green"
	}
}

func headerTitle(e Event) string {
	if e.HeaderTitle != "" {
		return e.HeaderTitle
	}
	return titleForEvent(e.Type)
}

func headerTemplate(e Event) string {
	if e.HeaderTemplate != "" {
		return e.HeaderTemplate
	}
	return templateForEvent(e.Type)
}
