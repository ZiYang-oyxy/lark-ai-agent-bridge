package card

import (
	"fmt"
	"strings"
)

const (
	referenceReasoningMax     = 1500
	referenceToolSummaryMax   = 80
	referenceToolBodyFieldMax = 600
	referenceToolOutputMax    = 1200
	referenceToolBodyTotalMax = 2500
	referenceToolCollapseAt   = 3
)

type referenceBlock struct {
	kind string
	text string
	tool *referenceTool
}

type referenceTool struct {
	id         string
	name       string
	summary    string
	input      map[string]any
	output     string
	status     string
	legacyBody string
}

func buildReferenceCardElements(e Event) []any {
	elements := make([]any, 0, len(e.Segments)+3)
	if thought := referenceThought(e.Segments); thought != "" {
		elements = append(elements, referenceReasoningPanel(thought, e.Streaming && e.Activity == "reasoning"))
	}

	visibleSegments := e.Segments
	if e.Type == "error" {
		visibleSegments = withoutReferenceErrors(e.Segments)
	}
	for _, group := range groupReferenceBlocks(referenceBlocks(visibleSegments)) {
		if group.text != "" {
			elements = append(elements, map[string]any{"tag": "markdown", "content": group.text})
			continue
		}
		elements = append(elements, renderReferenceToolGroup(group.tools, !e.Streaming)...)
	}

	switch e.Type {
	case "stopped", "interrupted":
		elements = append(elements, referenceNote("_⏹ 已被中断_"))
	case "error":
		if message := referenceError(e); message != "" {
			elements = append(elements, referenceNote("⚠️ agent 失败："+message))
		}
	case "result":
		if len(elements) == 0 {
			elements = append(elements, referenceNote("_（未返回内容）_"))
		}
	}

	if e.Streaming {
		elements = append(elements, referenceNote(referenceFooter(e.Activity)))
		if e.StopButton.Visible && !e.StopButton.Disabled {
			elements = append(elements, referenceStopButton(e))
		}
	}
	return elements
}

func withoutReferenceErrors(segments []Segment) []Segment {
	filtered := make([]Segment, 0, len(segments))
	for _, segment := range segments {
		if segment.Kind != SegmentError {
			filtered = append(filtered, segment)
		}
	}
	return filtered
}

type referenceGroup struct {
	text  string
	tools []*referenceTool
}

func groupReferenceBlocks(blocks []referenceBlock) []referenceGroup {
	groups := make([]referenceGroup, 0, len(blocks))
	var tools []*referenceTool
	flushTools := func() {
		if len(tools) == 0 {
			return
		}
		groups = append(groups, referenceGroup{tools: tools})
		tools = nil
	}
	for _, block := range blocks {
		if block.tool != nil {
			tools = append(tools, block.tool)
			continue
		}
		flushTools()
		if strings.TrimSpace(block.text) != "" {
			groups = append(groups, referenceGroup{text: block.text})
		}
	}
	flushTools()
	return groups
}

func referenceBlocks(segments []Segment) []referenceBlock {
	blocks := make([]referenceBlock, 0, len(segments))
	byID := map[string]*referenceTool{}
	lastText := -1
	legacyIndex := 0
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		switch segment.Kind {
		case SegmentThought:
			continue
		case SegmentTool:
			meta := segment.Tool
			if meta == nil {
				legacyIndex++
				blocks = append(blocks, referenceBlock{kind: "tool", tool: &referenceTool{
					id: fmt.Sprintf("legacy-%d", legacyIndex), name: "工具调用", status: "done", legacyBody: text,
				}})
				lastText = -1
				continue
			}
			id := strings.TrimSpace(meta.ID)
			tool := byID[id]
			if id == "" || tool == nil {
				legacyIndex++
				tool = &referenceTool{id: id, status: "running"}
				if id == "" {
					tool.id = fmt.Sprintf("anonymous-%d", legacyIndex)
				} else {
					byID[id] = tool
				}
				blocks = append(blocks, referenceBlock{kind: "tool", tool: tool})
			}
			applyReferenceToolSegment(tool, segment)
			lastText = -1
		case SegmentError:
			if text != "" {
				blocks = append(blocks, referenceBlock{kind: "text", text: "**Error**\n" + text})
			}
			lastText = -1
		default:
			if text == "" {
				continue
			}
			if lastText == len(blocks)-1 && lastText >= 0 {
				blocks[lastText].text += segment.Text
			} else {
				blocks = append(blocks, referenceBlock{kind: "text", text: segment.Text})
				lastText = len(blocks) - 1
			}
		}
	}
	return blocks
}

func applyReferenceToolSegment(tool *referenceTool, segment Segment) {
	meta := segment.Tool
	if tool == nil || meta == nil {
		return
	}
	if name := strings.TrimSpace(meta.Name); name != "" {
		tool.name = name
	}
	if summary := strings.TrimSpace(meta.Summary); summary != "" {
		tool.summary = summary
	}
	if len(meta.Input) > 0 {
		tool.input = meta.Input
	}
	if meta.Phase == "result" {
		tool.status = "done"
		if meta.IsError {
			tool.status = "error"
		}
		tool.output = meta.Output
		if tool.output == "" {
			tool.output = segment.Text
		}
	} else if tool.status == "" {
		tool.status = "running"
	}
	if tool.name == "" {
		tool.name = "工具调用"
	}
}

func renderReferenceToolGroup(tools []*referenceTool, finalized bool) []any {
	if len(tools) == 0 {
		return nil
	}
	if len(tools) < referenceToolCollapseAt {
		out := make([]any, 0, len(tools))
		for _, tool := range tools {
			out = append(out, referenceToolPanel(tool, false))
		}
		return out
	}
	if finalized {
		return []any{referenceToolSummaryPanel(tools, true)}
	}
	prior := tools[:len(tools)-1]
	latest := tools[len(tools)-1]
	return []any{referenceToolSummaryPanel(prior, false), referenceToolPanel(latest, true)}
}

func referenceReasoningPanel(content string, active bool) map[string]any {
	title := "🧠 **思考完成，点击查看**"
	if active {
		title = "🧠 **思考中**"
	}
	return referencePanel(title, active, "grey", truncateReference(content, referenceReasoningMax))
}

func referenceToolPanel(tool *referenceTool, expanded bool) map[string]any {
	color := "grey"
	if tool.status == "error" {
		color = "red"
	}
	return referencePanel(referenceToolHeader(tool), expanded, color, referenceToolBody(tool))
}

func referenceToolSummaryPanel(tools []*referenceTool, finalized bool) map[string]any {
	suffix := ""
	if finalized {
		suffix = "（已结束）"
	}
	lines := make([]string, 0, len(tools))
	for _, tool := range tools {
		lines = append(lines, "- "+referenceToolHeader(tool))
	}
	return referencePanel(fmt.Sprintf("☕ **%d 个工具调用%s**", len(tools), suffix), false, "blue", strings.Join(lines, "\n"))
}

func referenceToolHeader(tool *referenceTool) string {
	icon := "⏳"
	if tool.status == "done" {
		icon = "✅"
	} else if tool.status == "error" {
		icon = "❌"
	}
	name := strings.TrimSpace(tool.name)
	if name == "" {
		name = "工具调用"
	}
	summary := referenceToolSummary(tool)
	if summary == "" {
		return fmt.Sprintf("%s **%s**", icon, name)
	}
	return fmt.Sprintf("%s **%s** — %s", icon, name, summary)
}

func referenceToolSummary(tool *referenceTool) string {
	if summary := compactReference(strings.TrimSpace(tool.summary)); summary != "" {
		return truncateReference(summary, referenceToolSummaryMax)
	}
	return truncateReference(referenceInputSummary(tool.name, tool.input), referenceToolSummaryMax)
}

func referenceToolBody(tool *referenceTool) string {
	if tool.legacyBody != "" {
		return truncateReference(tool.legacyBody, referenceToolBodyTotalMax)
	}
	parts := make([]string, 0, 2)
	if input := referenceInputBody(tool.name, tool.input); input != "" {
		parts = append(parts, input)
	}
	if tool.output != "" {
		label := "Output"
		if tool.status == "error" {
			label = "Error"
		}
		parts = append(parts, fmt.Sprintf("**%s**\n```\n%s\n```", label, truncateReference(tool.output, referenceToolOutputMax)))
	} else if tool.status == "running" {
		parts = append(parts, "_运行中…_")
	}
	body := strings.Join(parts, "\n\n")
	if body == "" {
		return "_无输出_"
	}
	if len([]rune(body)) > referenceToolBodyTotalMax {
		return truncateReference(body, referenceToolBodyTotalMax) + "\n\n_（body 已截断,完整内容查 `/doctor` 或日志）_"
	}
	return body
}

func referenceInputSummary(name string, input map[string]any) string {
	pick := func(key string) string {
		value, _ := input[key].(string)
		return compactReference(value)
	}
	switch name {
	case "Bash":
		return pick("command")
	case "Read", "Edit", "Write", "NotebookEdit":
		return pick("file_path")
	case "Grep":
		pattern := truncateReference(pick("pattern"), 40)
		path := truncateReference(pick("path"), 30)
		if path != "" {
			return pattern + " in " + path
		}
		return pattern
	case "Glob":
		return pick("pattern")
	case "WebFetch":
		return pick("url")
	case "WebSearch":
		return truncateReference(pick("query"), 60)
	case "Agent", "Task":
		if value := pick("description"); value != "" {
			return value
		}
		return pick("subagent_type")
	default:
		for _, key := range []string{"command", "file_path", "path", "query"} {
			if value := pick(key); value != "" {
				return value
			}
		}
	}
	return ""
}

func referenceInputBody(name string, input map[string]any) string {
	value := func(key string) string {
		text, _ := input[key].(string)
		return text
	}
	switch name {
	case "Bash":
		if command := value("command"); command != "" {
			return "**Command**\n```bash\n" + truncateReference(command, referenceToolBodyFieldMax) + "\n```"
		}
	case "Read", "Edit", "Write", "NotebookEdit":
		if path := value("file_path"); path != "" {
			return "**File** `" + path + "`"
		}
	case "Grep":
		var lines []string
		if pattern := value("pattern"); pattern != "" {
			lines = append(lines, "**Pattern** `"+pattern+"`")
		}
		if path := value("path"); path != "" {
			lines = append(lines, "**Path** `"+path+"`")
		}
		return strings.Join(lines, "\n")
	case "WebFetch":
		if url := value("url"); url != "" {
			return "**URL** " + url
		}
	case "WebSearch":
		if query := value("query"); query != "" {
			return "**Query** `" + truncateReference(query, referenceToolBodyFieldMax) + "`"
		}
	}
	return ""
}

func referencePanel(title string, expanded bool, color, body string) map[string]any {
	return map[string]any{
		"tag": "collapsible_panel", "expanded": expanded,
		"header": map[string]any{
			"title":          map[string]any{"tag": "markdown", "content": title},
			"vertical_align": "center",
			"icon":           map[string]any{"tag": "standard_icon", "token": "down-small-ccm_outlined", "size": "16px 16px"},
			"icon_position":  "follow_text", "icon_expanded_angle": -180,
		},
		"border":           map[string]any{"color": color, "corner_radius": "5px"},
		"vertical_spacing": "8px", "padding": "8px 8px 8px 8px",
		"elements": []any{map[string]any{"tag": "markdown", "content": body, "text_size": "notation"}},
	}
}

func referenceStopButton(e Event) map[string]any {
	return map[string]any{
		"tag":       "button",
		"text":      map[string]any{"tag": "plain_text", "content": "⏹ 终止"},
		"type":      "danger",
		"behaviors": callbackBehaviorWithGrant(e.SessionID, "stop", "", e.StopButton.GrantID),
	}
}

func referenceNote(content string) map[string]any {
	return map[string]any{"tag": "markdown", "content": content, "text_size": "notation"}
}

func referenceThought(segments []Segment) string {
	var thoughts []string
	for _, segment := range segments {
		if segment.Kind == SegmentThought && strings.TrimSpace(segment.Text) != "" {
			thoughts = append(thoughts, strings.TrimSpace(segment.Text))
		}
	}
	return strings.Join(thoughts, "\n\n")
}

func referenceError(e Event) string {
	for index := len(e.Segments) - 1; index >= 0; index-- {
		if e.Segments[index].Kind == SegmentError && strings.TrimSpace(e.Segments[index].Text) != "" {
			return strings.TrimSpace(e.Segments[index].Text)
		}
	}
	return strings.TrimSpace(e.Message)
}

func referenceFooter(activity string) string {
	switch activity {
	case "tool":
		return "🧰 正在调用工具"
	case "answering":
		return "✍️ 正在输出"
	default:
		return "🧠 正在思考"
	}
}

func referenceSummary(e Event) string {
	switch e.Type {
	case "stopped", "interrupted":
		return "已中断"
	case "error":
		return "出错"
	case "result":
		return "已完成"
	}
	switch e.Activity {
	case "tool":
		return "正在调用工具"
	case "answering":
		return "正在输出"
	default:
		return "思考中"
	}
}

func truncateReference(value string, maxRunes int) string {
	runes := []rune(value)
	if maxRunes > 0 && len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return value
}

func compactReference(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
