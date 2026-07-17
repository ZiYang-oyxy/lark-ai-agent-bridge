package feishu

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fakeBotInfoHTTP struct {
	paths []string
}

func (f *fakeBotInfoHTTP) Do(req *http.Request) (*http.Response, error) {
	f.paths = append(f.paths, req.URL.Path)
	body := `{"code":0,"msg":"ok"}`
	if strings.HasSuffix(req.URL.Path, "/auth/v3/tenant_access_token/internal") {
		body = `{"code":0,"msg":"ok","tenant_access_token":"tenant-token","expire":7200}`
	}
	if strings.HasSuffix(req.URL.Path, "/bot/v3/info") {
		if req.Header.Get("Authorization") != "Bearer tenant-token" {
			body = `{"code":999,"msg":"missing token"}`
		} else {
			body = `{"code":0,"msg":"ok","bot":{"app_name":"打酱油","open_id":"ou_bot"}}`
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func TestBotInfoClientGet(t *testing.T) {
	httpClient := &fakeBotInfoHTTP{}
	client := NewBotInfoClient("cli_test", "secret")
	client.http = httpClient
	client.baseURL = "https://open.feishu.test"

	got, err := client.Get(context.Background())
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.OpenID != "ou_bot" || got.AppName != "打酱油" {
		t.Fatalf("bot info = %#v", got)
	}
	wantPaths := []string{"/open-apis/auth/v3/tenant_access_token/internal", "/open-apis/bot/v3/info"}
	if len(httpClient.paths) != len(wantPaths) {
		t.Fatalf("paths = %#v, want %#v", httpClient.paths, wantPaths)
	}
	for i := range wantPaths {
		if httpClient.paths[i] != wantPaths[i] {
			t.Fatalf("path[%d] = %q, want %q", i, httpClient.paths[i], wantPaths[i])
		}
	}
}
