package update

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDerivePrereleaseURL(t *testing.T) {
	got, ok := derivePrereleaseURL("https://tos.example/lark-ai-agent-bridge-stable-manifest.json")
	if !ok || got != "https://tos.example/lark-ai-agent-bridge-prerelease-manifest.json" {
		t.Fatalf("derive = %q ok=%v", got, ok)
	}
	if _, ok := derivePrereleaseURL("https://tos.example/no-channel-segment.json"); ok {
		t.Fatal("URL without -stable- must not derive")
	}
}

// TestClientAutoDerivesPrereleaseURL: in prerelease mode with no explicit
// PrereleaseURL, the client must fetch the derived -prerelease- manifest so
// enabling developer mode needs zero extra configuration.
func TestClientAutoDerivesPrereleaseURL(t *testing.T) {
	c := &Client{
		ManifestURL: "https://tos.example/lark-ai-agent-bridge-stable-manifest.json",
		Prerelease:  func() bool { return true },
	}
	if got := c.activeURL(); got != "https://tos.example/lark-ai-agent-bridge-prerelease-manifest.json" {
		t.Fatalf("activeURL = %q, want derived prerelease URL", got)
	}
	// Explicit PrereleaseURL overrides derivation.
	c.PrereleaseURL = "https://tos.example/custom-rc.json"
	if got := c.activeURL(); got != "https://tos.example/custom-rc.json" {
		t.Fatalf("explicit PrereleaseURL should win, got %q", got)
	}
	// Off => stable regardless.
	c.Prerelease = func() bool { return false }
	if got := c.activeURL(); got != c.ManifestURL {
		t.Fatalf("stable mode must use ManifestURL, got %q", got)
	}
}

// TestClientChannelSelection verifies the developer prerelease channel: with
// the Prerelease callback off, Check follows the stable manifest and ignores
// the rc build; with it on, Check follows the prerelease manifest and offers
// the rc. Also asserts the two channels use independent caches.
func TestClientChannelSelection(t *testing.T) {
	bin := []byte("payload")
	hash := fmt.Sprintf("%x", sha256.Sum256(bin))

	newServer := func(version string) (*httptest.Server, *atomic.Int32) {
		var calls atomic.Int32
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			body := validManifest(srv.URL+"/notes", srv.URL+"/bin", len(bin), hash)
			_, _ = w.Write([]byte(replaceManifestVersion(body, version)))
		}))
		return srv, &calls
	}

	stable, stableCalls := newServer("0.1.3")
	defer stable.Close()
	prerelease, preCalls := newServer("0.1.4-rc.1")
	defer prerelease.Close()

	var devMode atomic.Bool
	// Both channels served over TLS; reuse one client's transport is not possible
	// across two servers, so wire a transport that trusts both test certs.
	client := NewClient(stable.URL, stable.Client())
	client.PrereleaseURL = prerelease.URL
	client.HTTP = prerelease.Client() // prerelease client's pool; stable also needs trust
	client.Prerelease = devMode.Load
	client.GOOS, client.GOARCH = "linux", "amd64"

	// Stable channel: rc build must not be visible; current 0.1.3 => nothing new.
	// (Point HTTP at the stable server's client for this leg.)
	client.HTTP = stable.Client()
	res, err := client.Check(t.Context(), "0.1.3")
	if err != nil {
		t.Fatalf("stable check: %v", err)
	}
	if res.UpdateAvailable {
		t.Fatalf("stable channel should offer nothing over 0.1.3, got %s", res.Manifest.Version)
	}
	if res.Manifest.Version != "0.1.3" {
		t.Fatalf("stable manifest version = %s, want 0.1.3", res.Manifest.Version)
	}

	// Flip developer mode on: prerelease channel offers the rc.
	devMode.Store(true)
	client.HTTP = prerelease.Client()
	res, err = client.Check(t.Context(), "0.1.3")
	if err != nil {
		t.Fatalf("prerelease check: %v", err)
	}
	if !res.UpdateAvailable || res.Manifest.Version != "0.1.4-rc.1" {
		t.Fatalf("prerelease channel should offer rc, got available=%v version=%s", res.UpdateAvailable, res.Manifest.Version)
	}

	if stableCalls.Load() == 0 || preCalls.Load() == 0 {
		t.Fatalf("both channels should have been fetched: stable=%d prerelease=%d", stableCalls.Load(), preCalls.Load())
	}
}

func TestCompareAllowingPrerelease(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.4", "0.1.3", 1},
		{"0.1.3", "0.1.4", -1},
		{"0.1.4", "0.1.4", 0},
		{"0.1.4-rc.1", "0.1.3", 1},  // rc still newer than prior release
		{"0.1.4-rc.1", "0.1.4", -1}, // rc is older than its final release
		{"0.1.4", "0.1.4-rc.1", 1},  // final release outranks its rc
		{"0.1.4-rc.1", "0.1.4-rc.2", -1},
		{"0.1.4-rc.2", "0.1.4-rc.1", 1},
		{"0.1.4-rc.1", "0.1.4-rc.1", 0},
	}
	for _, c := range cases {
		got, err := CompareAllowingPrerelease(c.a, c.b)
		if err != nil {
			t.Fatalf("compare(%q,%q): unexpected error %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Fatalf("compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareAllowingPrereleaseRejectsBadInput(t *testing.T) {
	for _, v := range []string{"0.1", "0.1.4-beta.1", "0.1.4-rc.0", "0.1.4-rc.", "0.1.4-rc.01", "v0.1.4"} {
		if _, err := CompareAllowingPrerelease(v, "0.1.0"); err == nil {
			t.Fatalf("expected error for %q", v)
		}
	}
}

// TestStableCompareStillRejectsRc guards the safety boundary: the stable
// comparator must never accept an rc string, so rc builds can never qualify on
// the stable channel.
func TestStableCompareStillRejectsRc(t *testing.T) {
	if _, err := CompareStable("0.1.4-rc.1", "0.1.3"); err == nil {
		t.Fatal("CompareStable must reject rc versions")
	}
}

// TestStableCompareAcceptsRcCurrentVersion covers a bridge running an rc build
// on the stable channel: the check must not error out (which surfaced as
// "暂时无法检查更新"). Precedence follows SemVer with rc precision preserved —
// a prerelease is strictly lower than its matching release, so 0.1.4-rc.N sees
// the stable 0.1.4 as a genuine upgrade (the "rc → matching release" path).
func TestStableCompareAcceptsRcCurrentVersion(t *testing.T) {
	// Running 0.1.4-rc.3, stable manifest at 0.1.3 → current is newer, no update.
	if got, err := CompareStable("0.1.3", "0.1.4-rc.3"); err != nil || got != -1 {
		t.Fatalf("CompareStable(0.1.3, 0.1.4-rc.3) = %d,%v want -1,nil", got, err)
	}
	// Running 0.1.4-rc.3, stable manifest reaches the matching release 0.1.4 →
	// release outranks its prerelease, so this is an available upgrade.
	if got, err := CompareStable("0.1.4", "0.1.4-rc.3"); err != nil || got != 1 {
		t.Fatalf("CompareStable(0.1.4, 0.1.4-rc.3) = %d,%v want 1,nil", got, err)
	}
	// Running 0.1.4-rc.5 (the reported case), stable manifest at 0.1.4 → upgrade.
	if got, err := CompareStable("0.1.4", "0.1.4-rc.5"); err != nil || got != 1 {
		t.Fatalf("CompareStable(0.1.4, 0.1.4-rc.5) = %d,%v want 1,nil", got, err)
	}
	// Running the final 0.1.4, stable manifest at 0.1.4 → already up to date.
	if got, err := CompareStable("0.1.4", "0.1.4"); err != nil || got != 0 {
		t.Fatalf("CompareStable(0.1.4, 0.1.4) = %d,%v want 0,nil", got, err)
	}
	// Running 0.1.4-rc.3, stable manifest at 0.2.0 → genuine stable upgrade.
	if got, err := CompareStable("0.2.0", "0.1.4-rc.3"); err != nil || got != 1 {
		t.Fatalf("CompareStable(0.2.0, 0.1.4-rc.3) = %d,%v want 1,nil", got, err)
	}
}

// TestStableChannelCheckWithRcCurrentVersion is the end-to-end guard for the
// reported bug: an rc build on the stable channel used to make Client.Check
// return an error, which the /help card renders as "暂时无法检查更新". After the
// fix the check succeeds and reports no update (the rc is already newer than the
// stable manifest), so /help shows "已是最新版本".
func TestStableChannelCheckWithRcCurrentVersion(t *testing.T) {
	bin := []byte("payload")
	hash := fmt.Sprintf("%x", sha256.Sum256(bin))
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := validManifest(srv.URL+"/notes", srv.URL+"/bin", len(bin), hash)
		_, _ = w.Write([]byte(replaceManifestVersion(body, "0.1.3")))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, srv.Client())
	client.GOOS, client.GOARCH = "linux", "amd64"
	// Prerelease is nil → always stable channel (the default user mode).

	res, err := client.Check(t.Context(), "0.1.4-rc.3")
	if err != nil {
		t.Fatalf("stable check with rc current version must not error: %v", err)
	}
	if res.UpdateAvailable {
		t.Fatalf("rc build newer than stable 0.1.3 must report no update, got %s", res.Manifest.Version)
	}
}
