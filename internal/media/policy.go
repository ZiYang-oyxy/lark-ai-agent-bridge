package media

import (
	"fmt"
	"mime"
	"path/filepath"
	"strings"
)

type mediaPolicy struct {
	canonical string
	accepted  map[string]struct{}
}

func policy(canonical string, accepted ...string) mediaPolicy {
	values := make(map[string]struct{}, len(accepted))
	for _, value := range accepted {
		values[value] = struct{}{}
	}
	return mediaPolicy{canonical: canonical, accepted: values}
}

var policiesByExtension = map[string]mediaPolicy{
	".jpg":      policy("image/jpeg", "image/jpeg"),
	".jpeg":     policy("image/jpeg", "image/jpeg"),
	".png":      policy("image/png", "image/png"),
	".webp":     policy("image/webp", "image/webp"),
	".gif":      policy("image/gif", "image/gif"),
	".txt":      policy("text/plain", "text/plain"),
	".log":      policy("text/plain", "text/plain"),
	".md":       policy("text/markdown", "text/markdown", "text/plain"),
	".markdown": policy("text/markdown", "text/markdown", "text/plain"),
	".json":     policy("application/json", "application/json"),
	".csv":      policy("text/csv", "text/csv", "text/plain"),
}

// Validate performs a preliminary allowlist check against the declared MIME
// type and filename extension. The cache performs content-byte validation
// before accepting the resource.
func Validate(name, contentType string) (string, error) {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	policy, ok := policiesByExtension[extension]
	if !ok {
		return "", fmt.Errorf("media extension %q is not allowed", extension)
	}

	declared, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("parse media type: %w", err)
	}
	declared = strings.ToLower(declared)
	if _, ok := policy.accepted[declared]; !ok {
		return "", fmt.Errorf("media type %q does not match extension %q", declared, extension)
	}
	return policy.canonical, nil
}
