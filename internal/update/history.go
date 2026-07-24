package update

import (
	"net/url"
	"strings"
)

const (
	releaseHistoryPage   = "lark-ai-agent-bridge-releases.html"
	releaseHistoryRCPage = "lark-ai-agent-bridge-rc-releases.html"
)

// ReleaseHistoryURL derives the public "发布历史" page URL from a same-origin
// manifest or release-notes URL. It keeps only the scheme://host of the source
// URL as the base and appends the stable or rc history page depending on the
// prerelease flag. It returns ("", false) when the source URL cannot be parsed
// into an absolute scheme+host so the caller can skip the link safely.
func ReleaseHistoryURL(sourceURL string, prerelease bool) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	page := releaseHistoryPage
	if prerelease {
		page = releaseHistoryRCPage
	}
	base := parsed.Scheme + "://" + parsed.Host
	return base + "/" + page, true
}
