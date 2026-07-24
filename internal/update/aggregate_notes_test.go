package update

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntermediateVersionsExpandsSameCoreRCRange(t *testing.T) {
	got := intermediateVersions("0.1.4-rc.14", "0.1.4-rc.17")
	want := []string{"0.1.4-rc.17", "0.1.4-rc.16", "0.1.4-rc.15"}
	if len(got) != len(want) {
		t.Fatalf("intermediateVersions() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("intermediateVersions()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIntermediateVersionsDegradesToTargetOnly(t *testing.T) {
	cases := []struct {
		name    string
		current string
		target  string
	}{
		{"cross core", "0.1.3-rc.2", "0.1.4-rc.3"},
		{"target is final release", "0.1.4-rc.2", "0.1.4"},
		{"current is final release", "0.1.4", "0.1.5-rc.1"},
		{"target not newer", "0.1.4-rc.5", "0.1.4-rc.5"},
		{"unparseable current", "garbage", "0.1.4-rc.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := intermediateVersions(tc.current, tc.target)
			if len(got) != 1 || got[0] != tc.target {
				t.Fatalf("intermediateVersions(%q,%q) = %v, want [%q]", tc.current, tc.target, got, tc.target)
			}
		})
	}
}

func TestIntermediateVersionsBoundsRange(t *testing.T) {
	got := intermediateVersions("0.1.4-rc.1", "0.1.4-rc.100")
	if len(got) != maxAggregatedReleaseNotes {
		t.Fatalf("intermediateVersions() len = %d, want %d", len(got), maxAggregatedReleaseNotes)
	}
	if got[0] != "0.1.4-rc.100" {
		t.Fatalf("intermediateVersions()[0] = %q, want newest-first target", got[0])
	}
}

func TestReleaseNotesURLForVersionSubstitutesToken(t *testing.T) {
	tmpl := "https://tos.example/lark-ai-agent-bridge-v0.1.4-rc.17-release-notes.md"
	got, ok := releaseNotesURLForVersion(tmpl, "0.1.4-rc.17", "0.1.4-rc.15")
	if !ok || got != "https://tos.example/lark-ai-agent-bridge-v0.1.4-rc.15-release-notes.md" {
		t.Fatalf("releaseNotesURLForVersion() = %q, %v", got, ok)
	}
	if _, ok := releaseNotesURLForVersion("https://tos.example/no-version-token.md", "0.1.4-rc.17", "0.1.4-rc.15"); ok {
		t.Fatal("expected ok=false when version token absent")
	}
}

// TestAggregatedReleaseNotesConcatenatesNewestFirst verifies the full path: an
// rc.14 → rc.17 upgrade produces one section per intermediate version, ordered
// newest-first, each under its own heading.
func TestAggregatedReleaseNotesConcatenatesNewestFirst(t *testing.T) {
	body := map[string]string{
		"/lark-ai-agent-bridge-v0.1.4-rc.17-release-notes.md": "rc17 notes",
		"/lark-ai-agent-bridge-v0.1.4-rc.16-release-notes.md": "rc16 notes",
		"/lark-ai-agent-bridge-v0.1.4-rc.15-release-notes.md": "rc15 notes",
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if text, ok := body[r.URL.Path]; ok {
			_, _ = w.Write([]byte(text))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	manifest := Manifest{Version: "0.1.4-rc.17", ReleaseNotesURL: server.URL + "/lark-ai-agent-bridge-v0.1.4-rc.17-release-notes.md"}
	notes, err := client.AggregatedReleaseNotes(t.Context(), "0.1.4-rc.14", manifest, nil)
	if err != nil {
		t.Fatalf("AggregatedReleaseNotes() error = %v", err)
	}
	for _, want := range []string{"## v0.1.4-rc.17", "rc17 notes", "## v0.1.4-rc.16", "rc16 notes", "## v0.1.4-rc.15", "rc15 notes"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("aggregated notes missing %q:\n%s", want, notes)
		}
	}
	// newest-first ordering: rc.17 heading precedes rc.15 heading.
	if strings.Index(notes, "## v0.1.4-rc.17") > strings.Index(notes, "## v0.1.4-rc.15") {
		t.Fatalf("expected newest-first ordering:\n%s", notes)
	}
}

// TestAggregatedReleaseNotesSkipsMissingIntermediate confirms a 404 on one
// intermediate version is skipped (reported via onSkip) without failing the
// whole card, and the surviving sections still render.
func TestAggregatedReleaseNotesSkipsMissingIntermediate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lark-ai-agent-bridge-v0.1.4-rc.17-release-notes.md":
			_, _ = w.Write([]byte("rc17 notes"))
		case "/lark-ai-agent-bridge-v0.1.4-rc.15-release-notes.md":
			_, _ = w.Write([]byte("rc15 notes"))
		default: // rc.16 is missing
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	var skipped []string
	client := NewClient(server.URL, server.Client())
	manifest := Manifest{Version: "0.1.4-rc.17", ReleaseNotesURL: server.URL + "/lark-ai-agent-bridge-v0.1.4-rc.17-release-notes.md"}
	notes, err := client.AggregatedReleaseNotes(t.Context(), "0.1.4-rc.14", manifest, func(version string, _ error) {
		skipped = append(skipped, version)
	})
	if err != nil {
		t.Fatalf("AggregatedReleaseNotes() error = %v", err)
	}
	if !strings.Contains(notes, "rc17 notes") || !strings.Contains(notes, "rc15 notes") {
		t.Fatalf("expected surviving sections, got:\n%s", notes)
	}
	if strings.Contains(notes, "0.1.4-rc.16") {
		t.Fatalf("missing rc.16 should not appear:\n%s", notes)
	}
	if len(skipped) != 1 || skipped[0] != "0.1.4-rc.16" {
		t.Fatalf("onSkip = %v, want [0.1.4-rc.16]", skipped)
	}
}

// TestAggregatedReleaseNotesSingleVersionMatchesReleaseNotes confirms the
// single-version path (no expandable range) returns the target note verbatim,
// without an added heading — preserving the prior behaviour.
func TestAggregatedReleaseNotesSingleVersionMatchesReleaseNotes(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("only notes"))
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client())
	manifest := Manifest{Version: "0.1.4", ReleaseNotesURL: server.URL + "/notes"}
	notes, err := client.AggregatedReleaseNotes(t.Context(), "0.1.3", manifest, nil)
	if err != nil {
		t.Fatalf("AggregatedReleaseNotes() error = %v", err)
	}
	if notes != "only notes" {
		t.Fatalf("single-version notes = %q, want verbatim %q", notes, "only notes")
	}
}

func TestAggregatedReleaseNotesFallsBackWhenAllFail(t *testing.T) {
	// All intermediate fetches 404 except the target's own URL, which the fetch
	// path also 404s → AggregatedReleaseNotes falls back to ReleaseNotes, whose
	// error surfaces. Confirms we do not silently return empty notes.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client())
	manifest := Manifest{Version: "0.1.4-rc.17", ReleaseNotesURL: server.URL + "/lark-ai-agent-bridge-v0.1.4-rc.17-release-notes.md"}
	_, err := client.AggregatedReleaseNotes(t.Context(), "0.1.4-rc.14", manifest, nil)
	if err == nil {
		t.Fatal("expected error when all fetches fail")
	}
}
