package security

import "regexp"

var redactors = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[A-Za-z0-9._\-]+`),
	regexp.MustCompile(`(?i)(["']?authorization["']?\s*:\s*["']?bearer\s+)[A-Za-z0-9._\-]+`),
	regexp.MustCompile(`(?i)((token|secret|password|api[_-]?key)\s*[:=]\s*)[^\s,;]+`),
	regexp.MustCompile(`(?i)((access|refresh)[_-]?token\s*[:=]\s*)[^\s,;]+`),
}

func Redact(s string) string {
	out := s
	for _, re := range redactors {
		out = re.ReplaceAllString(out, "${1}[REDACTED]")
	}
	return out
}
