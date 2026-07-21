package buildinfo

import (
	"runtime"
	"testing"
)

func TestCurrentUsesInjectedValuesAndRuntimePlatform(t *testing.T) {
	oldVersion, oldCommit, oldBuildTime := Version, Commit, BuildTime
	t.Cleanup(func() {
		Version, Commit, BuildTime = oldVersion, oldCommit, oldBuildTime
	})
	Version, Commit, BuildTime = "1.2.3", "abc123", "2026-07-21T12:00:00Z"

	got := Current()
	if got.Version != Version || got.Commit != Commit || got.BuildTime != BuildTime || got.GOOS != runtime.GOOS || got.GOARCH != runtime.GOARCH {
		t.Fatalf("Current() = %#v", got)
	}
}

func TestIsReleaseRequiresCanonicalStableSemver(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{version: "dev"},
		{version: "v1.2.3"},
		{version: "1.2.3-rc.1"},
		{version: "1.2"},
		{version: "01.2.3"},
		{version: "1.2.3", want: true},
		{version: "0.0.0", want: true},
	} {
		Version = tc.version
		if got := IsRelease(); got != tc.want {
			t.Fatalf("IsRelease() for %q = %t, want %t", tc.version, got, tc.want)
		}
	}
}
