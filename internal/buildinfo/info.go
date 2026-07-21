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
	return stableVersionPattern.MatchString(Version)
}
