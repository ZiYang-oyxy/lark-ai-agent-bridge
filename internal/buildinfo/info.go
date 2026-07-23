package buildinfo

import (
	"regexp"
	"runtime"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var stableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// releaseVersionPattern additionally accepts an -rc.N prerelease suffix. A
// released rc build (e.g. 0.1.4-rc.2) is still a real, non-dev release and must
// be able to self-upgrade to the next rc; IsRelease therefore accepts it.
var releaseVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$`)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

func Current() Info {
	return Info{
		Version: Version, Commit: Commit, BuildTime: BuildTime,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}
}

func IsRelease() bool {
	return releaseVersionPattern.MatchString(Version)
}

// IsStableRelease reports whether the build is a final (non-rc) release. Kept
// separate from IsRelease for callers that must distinguish stable from rc.
func IsStableRelease() bool {
	return stableVersionPattern.MatchString(Version)
}
