package update

import "strings"

var releaseNoteSectionAliases = map[string]struct{}{
	"Breaking Changes": {},
	"Features":         {},
	"主要更新":             {},
	"Bug Fixes":        {},
	"Fixes":            {},
	"修复":               {},
	"Upgrade Notes":    {},
	"升级说明":             {},
	"Miscellaneous":    {},
	"杂项":               {},
}

var emptyReleaseNoteItems = map[string]struct{}{
	"无":  {},
	"无。": {},
	"无独立的用户可感知问题修复":  {},
	"无独立的用户可感知问题修复。": {},
}

// HideEmptyReleaseNoteSections removes canonical release-note sections whose
// body is blank or contains only an empty placeholder. Unknown headings and
// sections with any real content are preserved verbatim.
func HideEmptyReleaseNoteSections(markdown string) string {
	lines := strings.Split(markdown, "\n")
	filtered := make([]string, 0, len(lines))
	changed := false
	for i := 0; i < len(lines); {
		if !isReleaseNoteSectionHeading(lines[i]) {
			filtered = append(filtered, lines[i])
			i++
			continue
		}

		end := i + 1
		for end < len(lines) && !isReleaseNoteBoundary(lines[end]) {
			end++
		}
		if releaseNoteSectionIsEmpty(lines[i+1 : end]) {
			changed = true
			i = end
			continue
		}
		filtered = append(filtered, lines[i:end]...)
		i = end
	}
	if !changed {
		return markdown
	}
	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

func isReleaseNoteSectionHeading(line string) bool {
	title, ok := markdownHeading(line, 2)
	if !ok {
		return false
	}
	_, ok = releaseNoteSectionAliases[title]
	return ok
}

func isReleaseNoteBoundary(line string) bool {
	_, topLevel := markdownHeading(line, 1)
	_, section := markdownHeading(line, 2)
	return topLevel || section || strings.TrimSpace(line) == "---"
}

func markdownHeading(line string, level int) (string, bool) {
	trimmed := strings.TrimSpace(line)
	prefix := strings.Repeat("#", level) + " "
	if !strings.HasPrefix(trimmed, prefix) || strings.HasPrefix(trimmed, "#"+prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), "#")), true
}

func releaseNoteSectionIsEmpty(lines []string) bool {
	for _, line := range lines {
		item := strings.TrimSpace(line)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "- ") || strings.HasPrefix(item, "* ") {
			item = strings.TrimSpace(item[2:])
		}
		if _, ok := emptyReleaseNoteItems[item]; !ok {
			return false
		}
	}
	return true
}
