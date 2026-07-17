package agent

import (
	"regexp"
	"strings"
)

type InteractionKind string

const (
	InteractionNone          InteractionKind = "none"
	InteractionAuthorization InteractionKind = "authorization"
	InteractionChoice        InteractionKind = "choice"
	InteractionResume        InteractionKind = "resume"
)

type Interaction struct {
	Kind    InteractionKind
	Title   string
	Options []string
	Raw     string
}

var numberedOption = regexp.MustCompile(`(?m)^\s*(?:[❯›>]\s*)?(\d+)(?:[\).、]\s+|\s+)(.+)$`)
var shellPrompt = regexp.MustCompile(`(?m)^\s*([>$#❯›➜])\s*(?:[^\n]*)$`)

func DetectInteraction(output string) Interaction {
	lower := strings.ToLower(output)
	options := extractOptions(output)
	switch {
	case looksLikeAuthorizationRequest(output, lower):
		return Interaction{
			Kind:    InteractionAuthorization,
			Title:   "tool authorization request",
			Options: []string{"Allow once", "Allow tool for this session", "Allow all tools for this session", "Reject"},
			Raw:     output,
		}
	case len(options) > 0 && strings.Contains(lower, "resume") && (strings.Contains(lower, "session") || strings.Contains(lower, "会话")):
		return Interaction{Kind: InteractionResume, Title: "resume session", Options: options, Raw: output}
	case len(options) > 0 && (strings.Contains(lower, "choose") ||
		strings.Contains(lower, "请选择") ||
		strings.Contains(lower, "select") ||
		strings.Contains(lower, "quick safety check") ||
		strings.Contains(lower, "trust this folder") ||
		strings.Contains(lower, "trust the contents of this directory") ||
		strings.Contains(lower, "do you trust") ||
		strings.Contains(lower, "update available") ||
		strings.Contains(lower, "press enter to continue")):
		return Interaction{Kind: InteractionChoice, Title: "choice required", Options: options, Raw: output}
	default:
		return Interaction{Kind: InteractionNone, Raw: output}
	}
}

func looksLikeAuthorizationRequest(output, lower string) bool {
	if strings.Contains(output, "授权") || strings.Contains(output, "请求执行权限") {
		return true
	}
	if strings.Contains(lower, "allow once") ||
		strings.Contains(lower, "tool permission") ||
		strings.Contains(lower, "permission required") ||
		strings.Contains(lower, "permission request") ||
		strings.Contains(lower, "approval required") {
		return true
	}
	if strings.Contains(lower, "permission") {
		return strings.Contains(lower, "allow ") ||
			strings.Contains(lower, "reject") ||
			strings.Contains(lower, "approve")
	}
	return false
}

func extractOptions(output string) []string {
	matches := numberedOption.FindAllStringSubmatch(output, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 3 {
			out = append(out, strings.TrimSpace(match[2]))
		}
	}
	return out
}

func DetectReady(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}
	if DetectInteraction(trimmed).Kind != InteractionNone {
		return false
	}
	lower := strings.ToLower(trimmed)
	readyMarkers := []string{
		"ready for input",
		"waiting for input",
		"what would you like to do",
		"how can i help",
		"如何帮助",
	}
	for _, marker := range readyMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return shellPrompt.MatchString(output)
}
