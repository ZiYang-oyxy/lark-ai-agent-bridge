package reply

import (
	"fmt"
	"strings"

	"lark-agent-bridge/internal/card"
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
		if meta := markdownMetaLine(event.Meta); meta != "" {
			parts = append(parts, meta)
		}
	}
	if len(parts) == 0 {
		return "_（未返回内容）_"
	}
	return strings.Join(parts, "\n\n")
}

func renderToolUse(meta *card.ToolMeta) string {
	name := safeInline(meta.Name)
	if name == "" {
		name = "Tool"
	}
	line := "> ⏳ **" + name + "**"
	if summary := safeInline(meta.Summary); summary != "" {
		line += " · " + summary
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
	replacer := strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]")
	return replacer.Replace(value)
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

func markdownMetaLine(meta card.Meta) string {
	parts := make([]string, 0, 2)
	if agent := strings.TrimSpace(meta.Agent); agent != "" {
		if strings.EqualFold(agent, "claude") {
			agent = "Claude"
		}
		parts = append(parts, "🤖 "+agent)
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
		tokens := "🔢 tokens: ▶ " + compactMarkdownInt(runTokens)
		if totalTokens > 0 {
			tokens += " / ∑ " + compactMarkdownInt(totalTokens)
		}
		parts = append(parts, tokens)
	}
	return strings.Join(parts, " · ")
}

func compactMarkdownInt(value int) string {
	if value < 1000 {
		return fmt.Sprintf("%d", value)
	}
	text := fmt.Sprintf("%.1fk", float64(value)/1000)
	return strings.Replace(text, ".0k", "k", 1)
}
