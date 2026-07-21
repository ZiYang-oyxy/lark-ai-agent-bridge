package update

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestManagerRefreshBypassesManifestCache(t *testing.T) {
	data := []byte("new")
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	var mu sync.Mutex
	version := "1.1.0"
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := validManifest(server.URL+"/notes", server.URL+"/binary", len(data), hash)
		body = replaceManifestVersion(body, version)
		mu.Unlock()
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	manager := &Manager{Client: NewClient(server.URL, server.Client())}
	first, err := manager.Check(t.Context(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	version = "1.2.0"
	mu.Unlock()
	refreshed, err := manager.Refresh(t.Context(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.Version != "1.1.0" || refreshed.Manifest.Version != "1.2.0" {
		t.Fatalf("versions first=%q refreshed=%q", first.Manifest.Version, refreshed.Manifest.Version)
	}
}

func replaceManifestVersion(body, version string) string {
	return strings.Replace(body, `"version": "1.2.3"`, `"version": "`+version+`"`, 1)
}
