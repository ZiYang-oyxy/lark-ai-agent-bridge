package feishu

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lark-agent-bridge/internal/media"
)

type staticTenantToken string

func (t staticTenantToken) Token(context.Context) (string, error) { return string(t), nil }

func TestMediaDownloaderOpensMessageResourceAsLiveStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/open-apis/im/v1/messages/<FEISHU_MESSAGE_ID>/resources/file_1" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("type"); got != "file" {
			t.Errorf("type = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tenant-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="notes.md"`)
		_, _ = w.Write([]byte("# streamed"))
	}))
	defer server.Close()

	downloader := &MediaDownloader{BaseURL: server.URL, Client: server.Client(), Tokens: staticTenantToken("tenant-token")}
	download, err := downloader.Open(context.Background(), media.Ref{MessageID: "<FEISHU_MESSAGE_ID>", FileKey: "file_1", Kind: "file"})
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	if download.Name != "notes.md" || download.ContentType != "text/markdown; charset=utf-8" {
		t.Fatalf("download metadata = %+v", download)
	}
	body, err := io.ReadAll(download.Body)
	if err != nil || string(body) != "# streamed" {
		t.Fatalf("live body = %q, err=%v", body, err)
	}
}

func TestMediaDownloaderReturnsBoundedErrorForNonSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("x", 256<<10)))
	}))
	defer server.Close()
	downloader := &MediaDownloader{BaseURL: server.URL, Client: server.Client(), Tokens: staticTenantToken("tenant-token")}

	_, err := downloader.Open(context.Background(), media.Ref{MessageID: "<FEISHU_MESSAGE_ID>", FileKey: "file_1", Kind: "file"})
	if err == nil {
		t.Fatal("Open error = nil")
	}
	if len(err.Error()) > 70<<10 {
		t.Fatalf("error is not bounded: %d bytes", len(err.Error()))
	}
}

func TestMediaDownloaderRejectsUnsupportedResourceKind(t *testing.T) {
	downloader := &MediaDownloader{Tokens: staticTenantToken("tenant-token")}
	_, err := downloader.Open(context.Background(), media.Ref{MessageID: "<FEISHU_MESSAGE_ID>", FileKey: "file_1", Kind: "audio"})
	if err == nil {
		t.Fatal("Open error = nil")
	}
}
