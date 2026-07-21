package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientCheckCachesSuccessfulManifest(t *testing.T) {
	binary := []byte("new")
	hash := fmt.Sprintf("%x", sha256.Sum256(binary))
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(validManifest(server.URL+"/notes", server.URL+"/binary", len(binary), hash)))
	}))
	defer server.Close()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	client := NewClient(server.URL, server.Client())
	client.Now = func() time.Time { return now }

	for range 2 {
		result, err := client.Check(t.Context(), "1.0.0")
		if err != nil || !result.UpdateAvailable || result.Asset.Size != int64(len(binary)) {
			t.Fatalf("Check() = %#v, %v", result, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("manifest calls = %d, want 1", got)
	}
	now = now.Add(11 * time.Minute)
	if _, err := client.Check(t.Context(), "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("manifest calls after expiry = %d, want 2", got)
	}
}

func TestClientRejectsUnsupportedRuntime(t *testing.T) {
	hash := strings.Repeat("a", 64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(validManifest("https://updates.example/notes", "https://updates.example/binary", 3, hash)))
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client())
	client.GOOS, client.GOARCH = "windows", "amd64"
	result, err := client.Check(t.Context(), "1.0.0")
	if err != nil || !result.UnsupportedPlatform || result.UpdateAvailable {
		t.Fatalf("Check() = %#v, %v", result, err)
	}
}

func TestClientReleaseNotesAndStageEnforceContent(t *testing.T) {
	binary := []byte("new-binary")
	hash := fmt.Sprintf("%x", sha256.Sum256(binary))
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notes":
			_, _ = w.Write([]byte("# Changes\n\n- safer upgrade"))
		case "/binary":
			_, _ = w.Write(binary)
		default:
			_, _ = w.Write([]byte(validManifest(server.URL+"/notes", server.URL+"/binary", len(binary), hash)))
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client())
	client.GOOS, client.GOARCH = runtime.GOOS, runtime.GOARCH
	result, err := client.Check(t.Context(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	notes, err := client.ReleaseNotes(t.Context(), result.Manifest)
	if err != nil || !strings.Contains(notes, "safer upgrade") {
		t.Fatalf("ReleaseNotes() = %q, %v", notes, err)
	}
	staged, err := client.Stage(t.Context(), result.Asset, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(staged.Path) })
	got, err := os.ReadFile(staged.Path)
	if err != nil || string(got) != string(binary) {
		t.Fatalf("staged = %q, %v", got, err)
	}
}

func TestClientStageRemovesHashMismatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("bad"))
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client())
	dir := t.TempDir()
	_, err := client.Stage(context.Background(), Asset{URL: server.URL, SHA256: strings.Repeat("a", 64), Size: 3}, dir)
	if err == nil {
		t.Fatal("Stage() error = nil")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("staging dir entries = %v, err=%v", entries, readErr)
	}
}

func TestClientRejectsHTTPSRedirectDowngrade(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("unsafe"))
	}))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer tls.Close()
	client := NewClient(tls.URL, tls.Client())
	manifest := Manifest{ReleaseNotesURL: tls.URL}
	if _, err := client.ReleaseNotes(t.Context(), manifest); err == nil {
		t.Fatal("ReleaseNotes followed an HTTPS-to-HTTP redirect")
	}
}
