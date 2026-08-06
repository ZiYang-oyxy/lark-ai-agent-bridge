package reply

import (
	"strings"
	"unicode/utf8"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/security"
)

const (
	toolHeaderSummaryMaxRunes = 80
	inlineTimelineMaxRunes    = 9000
	continuationPageMaxRunes  = 6000
	maxContinuationCards      = 9
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

func renderThoughtLine(text string) string {
	text = security.Redact(strings.TrimSpace(text))
	text = strings.ReplaceAll(text, "\n", "\n> ")
	return "> 💭 **思考** — " + text
}

// RenderInlineTimeline projects ordered assistant and tool events into the
// single Markdown element used by append cards. Card status, actions, and meta
// remain in the surrounding CardKit shell and are intentionally omitted here.
func RenderInlineTimeline(event card.Event) string {
	return RenderInlineTimelineWithLimit(event, inlineTimelineMaxRunes)
}

func RenderInlineTimelineWithLimit(event card.Event, maxRunes int) string {
	parts := inlineTimelineParts(event)
	if len(parts) == 0 {
		return "_（未返回内容）_"
	}
	return fitTimelineParts(parts, normalizedTimelineLimit(maxRunes, inlineTimelineMaxRunes))
}

// RenderInlineTimelinePages projects the same append timeline into at most
// nine cards. Once the combined window is full, it keeps a continuous tail so
// the newest answer and terminal state remain on the last card.
func RenderInlineTimelinePages(event card.Event) []string {
	return RenderInlineTimelinePagesWithLimit(event, continuationPageMaxRunes)
}

func RenderInlineTimelinePagesWithLimit(event card.Event, maxRunes int) []string {
	parts := inlineTimelineParts(event)
	if len(parts) == 0 {
		parts = []string{"_（未返回内容）_"}
	}
	return paginateTimelineParts(event, parts, normalizedTimelineLimit(maxRunes, continuationPageMaxRunes), maxContinuationCards)
}

func inlineTimelineParts(event card.Event) []string {
	parts := make([]string, 0, len(event.Segments))
	toolParts := make(map[string]int)
	pendingTools := make(map[string]bool)
	hasError := false
	for _, segment := range event.Segments {
		text := strings.TrimSpace(segment.Text)
		switch segment.Kind {
		case card.SegmentThought:
			if text != "" {
				parts = append(parts, renderThoughtLine(text))
			}
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
				parts = append(parts, "⚠️ agent 失败："+security.Redact(text))
			}
		default:
			if text != "" {
				parts = append(parts, security.Redact(text))
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

	return parts
}

func normalizedTimelineLimit(configured, fallback int) int {
	if configured <= 0 || configured > fallback {
		return fallback
	}
	return configured
}

func AppendPreviewMaxRunes(cardMaxRunes int) int {
	return normalizedTimelineLimit(cardMaxRunes, inlineTimelineMaxRunes)
}

func ContinuationPreviewMaxRunes(cardMaxRunes int) int {
	return normalizedTimelineLimit(cardMaxRunes, continuationPageMaxRunes) * maxContinuationCards
}

func paginateTimelineParts(event card.Event, parts []string, maxRunes, maxPages int) []string {
	if len(parts) == 0 || maxRunes <= 0 || maxPages <= 0 {
		return nil
	}
	runes, truncated := boundedTimelineTail(parts, maxRunes*maxPages)
	pages := make([]string, 0, minInt(maxPages+1, (len(runes)+maxRunes-1)/maxRunes))
	for len(runes) > 0 {
		count := largestFittingPrefix(event, runes, maxRunes)
		pages = append(pages, string(runes[:count]))
		runes = runes[count:]
	}
	if len(pages) == 0 {
		return []string{"_（未返回内容）_"}
	}
	if len(pages) > maxPages {
		pages = append([]string(nil), pages[len(pages)-maxPages:]...)
		truncated = true
	}
	if truncated {
		pages[0] = fitContinuationNotice(event, pages[0], maxRunes)
	}
	return pages
}

func boundedTimelineTail(parts []string, capacity int) ([]rune, bool) {
	if capacity <= 0 {
		return nil, len(parts) > 0
	}
	reversed := make([]rune, 0, capacity)
	truncated := false
outer:
	for i := len(parts) - 1; i >= 0; i-- {
		for remaining := parts[i]; remaining != ""; {
			if len(reversed) == capacity {
				truncated = true
				break outer
			}
			r, size := utf8.DecodeLastRuneInString(remaining)
			reversed = append(reversed, r)
			remaining = remaining[:len(remaining)-size]
		}
		if i == 0 {
			continue
		}
		for range 2 {
			if len(reversed) == capacity {
				truncated = true
				break outer
			}
			reversed = append(reversed, '\n')
		}
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed, truncated
}

func largestFittingPrefix(event card.Event, runes []rune, maxRunes int) int {
	high := minInt(maxRunes, len(runes))
	if continuationMarkdownFits(event, string(runes[:high])) {
		return high
	}
	low := 1
	for low < high {
		mid := low + (high-low+1)/2
		if continuationMarkdownFits(event, string(runes[:mid])) {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low
}

func fitContinuationNotice(event card.Event, page string, maxRunes int) string {
	const notice = "_较早过程已省略_\n\n"
	noticeRunes := []rune(notice)
	if maxRunes <= len(noticeRunes) {
		return string(noticeRunes[:maxRunes])
	}
	runes := []rune(page)
	if len(runes) > maxRunes {
		runes = runes[len(runes)-maxRunes:]
	}
	low, high := 0, len(runes)
	for low < high {
		mid := low + (high-low+1)/2
		candidate := notice + string(runes[len(runes)-mid:])
		if continuationMarkdownFits(event, candidate) && len([]rune(candidate)) <= maxRunes {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return notice + string(runes[len(runes)-low:])
}

func continuationMarkdownFits(event card.Event, markdown string) bool {
	page := event
	page.SessionID += ":page:9"
	page.Markdown = markdown
	page.MarkdownLayout = true
	page.InlineTimelineLayout = false
	page.OrderedLayout = false
	_, _, err := card.MarshalLarkCard(card.BuildLarkCard(page))
	return err == nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
	noticeRunes := []rune(notice)
	if maxRunes <= len(noticeRunes) {
		return string(noticeRunes[:maxRunes])
	}
	remaining := maxRunes - len(noticeRunes) - 2
	keptReverse := make([]string, 0, len(parts))
	for index := len(parts) - 1; index >= 0 && remaining > 0; index-- {
		separatorRunes := 0
		if len(keptReverse) > 0 {
			separatorRunes = 2
		}
		partRunes := []rune(parts[index])
		if len(partRunes)+separatorRunes <= remaining {
			keptReverse = append(keptReverse, parts[index])
			remaining -= len(partRunes) + separatorRunes
			continue
		}
		keep := remaining - separatorRunes
		if keep > 0 {
			keptReverse = append(keptReverse, string(partRunes[len(partRunes)-keep:]))
		}
		break
	}
	kept := make([]string, len(keptReverse))
	for index := range keptReverse {
		kept[len(keptReverse)-1-index] = keptReverse[index]
	}
	if len(kept) == 0 {
		return notice
	}
	return notice + "\n\n" + strings.Join(kept, "\n\n")
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
