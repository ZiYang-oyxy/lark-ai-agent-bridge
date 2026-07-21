package update

import (
	"fmt"
	"strings"
	"testing"
)

func validManifest(notesURL, binaryURL string, size int, hash string) string {
	return fmt.Sprintf(`{
  "schema_version": 1,
  "version": "1.2.3",
  "published_at": "2026-07-21T12:00:00Z",
  "release_notes_url": %q,
  "assets": {
    "linux/amd64": {"url": %q, "sha256": %q, "size": %d},
    "darwin/arm64": {"url": %q, "sha256": %q, "size": %d}
  }
}`, notesURL, binaryURL, hash, size, binaryURL, hash, size)
}

func TestParseManifestSelectsExactPlatform(t *testing.T) {
	hash := strings.Repeat("a", 64)
	manifest, err := ParseManifest(strings.NewReader(validManifest("https://updates.example/notes", "https://updates.example/binary", 3, hash)))
	if err != nil {
		t.Fatal(err)
	}
	asset, ok := manifest.Asset("linux", "amd64")
	if !ok || asset.URL != "https://updates.example/binary" || asset.Size != 3 {
		t.Fatalf("asset = %#v, ok=%t", asset, ok)
	}
}

func TestParseManifestRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	hash := strings.Repeat("a", 64)
	base := validManifest("https://updates.example/notes", "https://updates.example/binary", 3, hash)
	cases := map[string]string{
		"unknown field":  strings.Replace(base, `"schema_version": 1`, `"schema_version": 1, "extra": true`, 1),
		"trailing value": base + `{}`,
		"http notes":     strings.Replace(base, "https://updates.example/notes", "http://updates.example/notes", 1),
		"http binary":    strings.ReplaceAll(base, "https://updates.example/binary", "http://updates.example/binary"),
		"bad version":    strings.Replace(base, `"1.2.3"`, `"v1.2.3"`, 1),
		"prerelease":     strings.Replace(base, `"1.2.3"`, `"1.2.3-rc.1"`, 1),
		"uppercase hash": strings.ReplaceAll(base, hash, strings.ToUpper(hash)),
		"oversized":      strings.ReplaceAll(base, `"size": 3`, fmt.Sprintf(`"size": %d`, MaxBinaryBytes+1)),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest(strings.NewReader(input)); err == nil {
				t.Fatal("ParseManifest() error = nil")
			}
		})
	}
}

func TestCompareStable(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{a: "1.2.3", b: "1.2.3"},
		{a: "1.2.4", b: "1.2.3", want: 1},
		{a: "2.0.0", b: "10.0.0", want: -1},
	} {
		got, err := CompareStable(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Fatalf("CompareStable(%q,%q) = %d,%v want %d", tc.a, tc.b, got, err, tc.want)
		}
	}
	if _, err := CompareStable("dev", "1.0.0"); err == nil {
		t.Fatal("CompareStable accepted dev")
	}
}
