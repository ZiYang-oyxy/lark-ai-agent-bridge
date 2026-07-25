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
		{input: "v1.2.3-rc.1", canonical: "1.2.3-rc.1", ok: true},
		{input: "v1.2.3-rc.0", canonical: "1.2.3-rc.0", ok: true},
		{input: "v1.2.3-beta.1"},
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

func TestCompareReleaseVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{a: "1.2.3-rc.1", b: "1.2.3-rc.0", want: 1},
		{a: "1.2.3", b: "1.2.3-rc.9", want: 1},
		{a: "1.2.4-rc.0", b: "1.2.3", want: 1},
	} {
		got, err := compareReleaseVersions(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Fatalf("compareReleaseVersions(%q,%q) = %d,%v", tc.a, tc.b, got, err)
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
		"featish: do not misclassify",
	})
	for _, want := range []string{
		"# v1.2.3",
		"## Features", "add cards",
		"## Bug Fixes", "verify hash",
		"## Breaking Changes", "change manifest schema",
		"## Miscellaneous", "simplify client", "do not misclassify",
		"## Upgrade Notes", "- 无。",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("release notes missing %q:\n%s", want, got)
		}
	}
}

func TestRenderReleaseNotesDeduplicatesIntoHighestPrioritySection(t *testing.T) {
	got := renderReleaseNotes("v1.2.3", []string{
		"merge: preserve diagnostics",
		"feat(bridge): preserve diagnostics",
	})
	if strings.Count(got, "- preserve diagnostics\n") != 1 {
		t.Fatalf("release note duplicate count != 1:\n%s", got)
	}
	features := strings.SplitN(got, "## Bug Fixes", 2)[0]
	if !strings.Contains(features, "## Features\n\n- preserve diagnostics") {
		t.Fatalf("duplicate was not retained in Features:\n%s", got)
	}
	if err := validateReleaseNote("v1.2.3", []byte(got)); err != nil {
		t.Fatalf("generated release note is invalid: %v\n%s", err, got)
	}
}

func TestReleaseNoteTemplateMatchesGeneratedSections(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))
	if err := requireReleaseNoteTemplate(); err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(filepath.Join("docs", "releases", "TEMPLATE.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range releaseNoteSections {
		if strings.Count(string(template), "## "+section) != 1 {
			t.Fatalf("template must contain exactly one %q section", section)
		}
	}
}

func TestValidateReleaseNoteEnforcesStructuredTemplate(t *testing.T) {
	valid := renderReleaseNotes("v1.2.3", []string{
		"feat: add cards",
		"fix: verify hash",
		"docs: explain deployment",
	})
	if err := validateReleaseNote("v1.2.3", []byte(valid)); err != nil {
		t.Fatalf("valid note rejected: %v\n%s", err, valid)
	}

	tests := map[string]string{
		"wrong title": strings.Replace(valid, "# v1.2.3", "# v1.2.4", 1),
		"wrong order": strings.Replace(valid,
			"## Breaking Changes\n\n- 无。\n\n## Features\n\n- add cards",
			"## Features\n\n- add cards\n\n## Breaking Changes\n\n- 无。", 1),
		"prose outside bullet":     strings.Replace(valid, "- add cards", "add cards", 1),
		"empty mixed with content": strings.Replace(valid, "## Upgrade Notes\n\n- 无。", "## Upgrade Notes\n\n- 无。\n- restart", 1),
		"duplicate bullet":         strings.Replace(valid, "## Upgrade Notes\n\n- 无。", "## Upgrade Notes\n\n- add cards", 1),
		"all sections empty":       "# v1.2.3\n",
	}
	for name, note := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateReleaseNote("v1.2.3", []byte(note)); err == nil {
				t.Fatalf("invalid note accepted:\n%s", note)
			}
		})
	}
}

// TestValidateReleaseNoteAllowsMissingSections 校验放宽后的核心语义:缺失的 section 允许
// 直接省略,不再强制"5 段全在 + 无内容用『无。』占位"。已出现的段仍按 releaseNoteSections
// 顺序推进(顺序错误由 TestValidateReleaseNoteEnforcesStructuredTemplate 的 wrong-order 覆盖)。
func TestValidateReleaseNoteAllowsMissingSections(t *testing.T) {
	// 只有 Features 一段,其他 4 段都省略——旧版会报 "missing section",新版应通过。
	onlyFeatures := "# v1.2.3\n\n## Features\n\n- add cards\n"
	if err := validateReleaseNote("v1.2.3", []byte(onlyFeatures)); err != nil {
		t.Fatalf("notes with only Features should be valid, got %v", err)
	}
	// Features + Bug Fixes,跳过 Breaking Changes / Upgrade Notes / Miscellaneous:仍合法。
	partial := "# v1.2.3\n\n## Features\n\n- add cards\n\n## Bug Fixes\n\n- verify hash\n"
	if err := validateReleaseNote("v1.2.3", []byte(partial)); err != nil {
		t.Fatalf("notes skipping some sections should be valid, got %v", err)
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

func TestWriteAIInstallGuidesUsesFixedAndStableManifestURLs(t *testing.T) {
	dir := t.TempDir()
	versionDir := filepath.Join(dir, "v1.2.3")
	stableDir := filepath.Join(dir, "stable")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stableDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(versionDir, "AI_INSTALL.md"),
		filepath.Join(stableDir, "AI_INSTALL.md"),
	} {
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := []byte("# Install\n\nManifest: {{MANIFEST_URL}}\n")
	if err := writeAIInstallGuides(versionDir, stableDir, "v1.2.3", "https://updates.example/bridge/", source); err != nil {
		t.Fatal(err)
	}

	versioned, err := os.ReadFile(filepath.Join(versionDir, "AI_INSTALL.md"))
	if err != nil {
		t.Fatal(err)
	}
	stable, err := os.ReadFile(filepath.Join(stableDir, "AI_INSTALL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(versioned); !strings.Contains(got, "https://updates.example/bridge/v1.2.3/manifest.json") || strings.Contains(got, "{{MANIFEST_URL}}") {
		t.Fatalf("versioned guide = %q", got)
	}
	if got := string(stable); !strings.Contains(got, "https://updates.example/bridge/stable/manifest.json") || strings.Contains(got, "{{MANIFEST_URL}}") {
		t.Fatalf("stable guide = %q", got)
	}
	if strings.Contains(string(versioned), dir) || strings.Contains(string(stable), dir) {
		t.Fatalf("guide leaked build path %q", dir)
	}
	for _, path := range []string{
		filepath.Join(versionDir, "AI_INSTALL.md"),
		filepath.Join(stableDir, "AI_INSTALL.md"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Fatalf("%s mode = %o, want 644", path, got)
		}
	}
}

func TestWriteAIInstallGuidesRequiresExactlyOneManifestMarker(t *testing.T) {
	for _, source := range []string{
		"# missing\n",
		"{{MANIFEST_URL}}\n{{MANIFEST_URL}}\n",
	} {
		err := writeAIInstallGuides(t.TempDir(), t.TempDir(), "v1.2.3", "https://updates.example/bridge", []byte(source))
		if err == nil || !strings.Contains(err.Error(), "exactly one {{MANIFEST_URL}} marker") {
			t.Fatalf("source %q error = %v", source, err)
		}
	}
}
