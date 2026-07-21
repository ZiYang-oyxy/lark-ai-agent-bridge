package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bridgeupdate "lark-agent-bridge/internal/update"
)

func TestCanonicalReleaseVersion(t *testing.T) {
	for _, tc := range []struct {
		input, canonical string
		ok               bool
	}{
		{input: "v1.2.3", canonical: "1.2.3", ok: true},
		{input: "1.2.3"},
		{input: "v1.2.3-rc.1"},
		{input: "v01.2.3"},
	} {
		got, err := canonicalReleaseVersion(tc.input)
		if tc.ok && (err != nil || got != tc.canonical) {
			t.Fatalf("canonicalReleaseVersion(%q) = %q,%v", tc.input, got, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("canonicalReleaseVersion(%q) accepted", tc.input)
		}
	}
}

func TestParseBundleArgsAcceptsDocumentedTagFirstForm(t *testing.T) {
	tag, baseURL, err := parseBundleArgs([]string{"v1.2.3", "--base-url", "https://updates.example/bridge"})
	if err != nil || tag != "v1.2.3" || baseURL != "https://updates.example/bridge" {
		t.Fatalf("parseBundleArgs() = %q,%q,%v", tag, baseURL, err)
	}
}

func TestRenderReleaseNotesGroupsConventionalCommits(t *testing.T) {
	got := renderReleaseNotes("v1.2.3", []string{
		"feat(update): add cards",
		"fix(update): verify hash",
		"refactor: simplify client",
		"feat!: change manifest schema",
	})
	for _, want := range []string{"# v1.2.3", "## Breaking", "change manifest schema", "## Features", "add cards", "## Fixes", "verify hash", "## Other", "simplify client"} {
		if !strings.Contains(got, want) {
			t.Fatalf("release notes missing %q:\n%s", want, got)
		}
	}
}

func TestWriteBundleMetadataUsesExactFilesAndBaseURL(t *testing.T) {
	dir := t.TempDir()
	linux := filepath.Join(dir, "lark-agent-bridge-linux-amd64")
	darwin := filepath.Join(dir, "lark-agent-bridge-darwin-arm64")
	if err := os.WriteFile(linux, []byte("linux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(darwin, []byte("darwin"), 0o755); err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	manifest, err := writeBundleMetadata(dir, "v1.2.3", "https://updates.example/bridge/", publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.2.3" || manifest.ReleaseNotesURL != "https://updates.example/bridge/v1.2.3/release-notes.md" {
		t.Fatalf("manifest = %#v", manifest)
	}
	for _, platform := range []string{"linux/amd64", "darwin/arm64"} {
		asset := manifest.Assets[platform]
		if asset.Size == 0 || len(asset.SHA256) != 64 || !strings.HasPrefix(asset.URL, "https://updates.example/bridge/v1.2.3/") {
			t.Fatalf("asset %s = %#v", platform, asset)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded bridgeupdate.Manifest
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Version != "1.2.3" {
		t.Fatalf("decode manifest = %#v, %v", decoded, err)
	}
	checksums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil || !strings.Contains(string(checksums), "lark-agent-bridge-linux-amd64") || !strings.Contains(string(checksums), "lark-agent-bridge-darwin-arm64") {
		t.Fatalf("checksums = %q, %v", checksums, err)
	}
}
