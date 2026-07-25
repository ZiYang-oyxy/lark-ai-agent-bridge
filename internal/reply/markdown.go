package reply

import (
	"strings"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/security"
)

const (
	toolHeaderSummaryMaxRunes = 80
	inlineTimelineMaxRunes    = 9000
)

func RenderMarkdown(event card.Event) string {
	parts := make([]string, 0, len(event.Segments)+2)
	toolParts := make(map[string]int)
	hasError := false
	for _, segment := range event.Segments {
		text := strings.TrimSpace(segment.Text)
		switch segment.Kind {
		case card.SegmentThought:
			continue
		case card.SegmentTool:
			meta := segment.Tool
			if meta == nil || strings.TrimSpace(meta.ID) == "" {
				continue
			}
			if meta.Phase == "result" {
				if index, ok := toolParts[meta.ID]; ok {
					parts[index] = renderToolLine(parts[index], meta)
				}
				continue
			}
			line := renderToolUse(meta)
			if line != "" {
				toolParts[meta.ID] = len(parts)
				parts = append(parts, line)
			}
		case card.SegmentError:
			if text != "" {
				hasError = true
				parts = append(parts, "⚠️ Agent 执行失败："+text)
			}
		default:
			if text != "" {
				parts = append(parts, text)
			}
		}
	}

	if event.Streaming {
		parts = append(parts, runningStatusLine(event.Activity))
	} else if status := terminalStatusLine(event.Type, hasError); status != "" {
		parts = append(parts, status)
	}
	if !event.Streaming {
		if meta := markdownMetaFooter(event.Meta); meta != "" {
			parts = append(parts, meta)
		}
	}
	if len(parts) == 0 {
		return "_（未返回内容）_"
	}
	return strings.Join(parts, "\n\n")
}

// RenderInlineTimeline projects ordered assistant and tool events into the
// single Markdown element used by append cards. Card status, actions, and meta
// remain in the surrounding CardKit shell and are intentionally omitted here.
func RenderInlineTimeline(event card.Event) string {
	parts := make([]string, 0, len(event.Segments))
	toolParts := make(map[string]int)
	pendingTools := make(map[string]bool)
	hasError := false
	for _, segment := range event.Segments {
		text := strings.TrimSpace(segment.Text)
		switch segment.Kind {
		case card.SegmentThought:
			continue
		case card.SegmentTool:
			meta := segment.Tool
			if meta == nil || strings.TrimSpace(meta.ID) == "" {
				continue
			}
			if meta.Phase == "result" {
				if index, ok := toolParts[meta.ID]; ok && pendingTools[meta.ID] {
					parts[index] = renderToolLine(parts[index], meta)
					pendingTools[meta.ID] = false
				}
				continue
			}
			if _, exists := toolParts[meta.ID]; exists {
				continue
			}
			line := renderToolUse(meta)
			if line != "" {
				toolParts[meta.ID] = len(parts)
				pendingTools[meta.ID] = true
				parts = append(parts, line)
			}
		case card.SegmentError:
			if text != "" {
				hasError = true
				parts = append(parts, "⚠️ agent 失败："+text)
			}
		default:
			if text != "" {
				parts = append(parts, text)
			}
		}
	}

	if event.Streaming {
		parts = append(parts, runningStatusLine(event.Activity))
	} else {
		switch event.Type {
		case "stopped", "interrupted":
			parts = append(parts, "_⏹ 已被中断_")
		case "error":
			if !hasError {
				parts = append(parts, "_⚠️ 执行失败_")
			}
		}
	}

	if len(parts) == 0 {
		return "_（未返回内容）_"
	}
	return fitTimelineParts(parts, inlineTimelineMaxRunes)
}

func renderToolUse(meta *card.ToolMeta) string {
	name := safeInline(meta.Name)
	if name == "" {
		name = "Tool"
	}
	line := "> ⏳ **" + name + "**"
	if summary := safeInlineSummary(meta.Summary, toolHeaderSummaryMaxRunes); summary != "" {
		line += " — " + summary
	}
	return line
}

func renderToolLine(existing string, result *card.ToolMeta) string {
	status := "✅"
	if result.IsError {
		status = "❌"
	}
	return strings.Replace(existing, "⏳", status, 1)
}

func safeInline(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	return escapeMarkdownInline(value)
}

func safeInlineSummary(value string, maxRunes int) string {
	value = security.Redact(value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if maxRunes > 0 && len(runes) > maxRunes {
		value = string(runes[:maxRunes]) + "…"
	}
	return escapeMarkdownInline(value)
}

func escapeMarkdownInline(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]")
	return replacer.Replace(value)
}

func fitTimelineParts(parts []string, maxRunes int) string {
	if len(parts) == 0 {
		return ""
	}
	joined := strings.Join(parts, "\n\n")
	if maxRunes <= 0 || len([]rune(joined)) <= maxRunes {
		return joined
	}
	const notice = "_较早过程已省略_"
	for len(parts) > 1 {
		parts = parts[1:]
		candidate := notice + "\n\n" + strings.Join(parts, "\n\n")
		if len([]rune(candidate)) <= maxRunes {
			return candidate
		}
	}
	return notice + "\n\n" + limitHeadWithSuffix(parts[0], maxRunes-len([]rune(notice))-2)
}

func limitHeadWithSuffix(value string, maxRunes int) string {
	const suffix = "\n\n[truncated]"
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	suffixRunes := []rune(suffix)
	if maxRunes <= len(suffixRunes) {
		return string(suffixRunes[:maxRunes])
	}
	return string(runes[:maxRunes-len(suffixRunes)]) + suffix
}

func runningStatusLine(activity string) string {
	switch activity {
	case "tool":
		return "_🧰 正在调用工具…_"
	case "answering":
		return "_✍️ 正在输出…_"
	default:
		return "_🧠 正在思考…_"
	}
}

func terminalStatusLine(eventType string, hasError bool) string {
	switch eventType {
	case "stopped", "interrupted":
		return "_⏹ 已停止_"
	case "error":
		if !hasError {
			return "_⚠️ 执行失败_"
		}
	}
	return ""
}

func markdownMetaFooter(meta card.Meta) string {
	metaRows := card.MetaRows(meta)
	if len(metaRows) == 0 {
		return ""
	}
	rows := make([]string, 0, len(metaRows))
	for _, r := range metaRows {
		if r.Text != "" {
			rows = append(rows, r.Text)
		}
	}
	return strings.Join(rows, "\n")
}
