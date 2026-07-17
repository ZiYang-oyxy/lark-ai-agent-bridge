package agent

import (
	"regexp"
	"strconv"
	"strings"
)

type RuntimeMeta struct {
	Model  string
	Tokens int
}

var (
	modelPattern  = regexp.MustCompile(`(?i)\bmodel\s*[:=]\s*([A-Za-z0-9._/\-]+)`)
	tokensPattern = regexp.MustCompile(`(?i)\b(?:tokens?|token usage)\s*[:=]\s*([0-9][0-9,]*)`)
)

func ParseRuntimeMeta(output string) RuntimeMeta {
	meta := RuntimeMeta{}
	if match := modelPattern.FindStringSubmatch(output); len(match) == 2 {
		meta.Model = strings.TrimSpace(match[1])
	}
	if matches := tokensPattern.FindAllStringSubmatch(output, -1); len(matches) > 0 {
		raw := strings.ReplaceAll(matches[len(matches)-1][1], ",", "")
		if n, err := strconv.Atoi(raw); err == nil {
			meta.Tokens = n
		}
	}
	return meta
}
