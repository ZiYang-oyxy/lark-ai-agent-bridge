package update

import "testing"

func TestReleaseHistoryURL(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		prerelease bool
		wantURL    string
		wantOK     bool
	}{
		{
			name:       "stable channel",
			source:     "https://tos.example/lark-ai-agent-bridge-stable-manifest.json",
			prerelease: false,
			wantURL:    "https://tos.example/lark-ai-agent-bridge-releases.html",
			wantOK:     true,
		},
		{
			name:       "prerelease channel",
			source:     "https://tos.example/lark-ai-agent-bridge-prerelease-manifest.json",
			prerelease: true,
			wantURL:    "https://tos.example/lark-ai-agent-bridge-rc-releases.html",
			wantOK:     true,
		},
		{
			name:       "derives from release notes url same origin",
			source:     "https://tos.example/some/deep/path/notes-v0.1.4.md",
			prerelease: false,
			wantURL:    "https://tos.example/lark-ai-agent-bridge-releases.html",
			wantOK:     true,
		},
		{
			name:       "unparseable falls back",
			source:     "not-a-url",
			prerelease: false,
			wantURL:    "",
			wantOK:     false,
		},
		{
			name:       "empty falls back",
			source:     "",
			prerelease: true,
			wantURL:    "",
			wantOK:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ReleaseHistoryURL(tc.source, tc.prerelease)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.wantURL {
				t.Fatalf("url = %q, want %q", got, tc.wantURL)
			}
		})
	}
}
