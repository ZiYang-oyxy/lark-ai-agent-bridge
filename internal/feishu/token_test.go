package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTenantTokenSourceSerializesRefreshAndCachesToken(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/open-apis/auth/v3/tenant_access_token/internal" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var credentials map[string]string
		if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
			t.Error(err)
		}
		if credentials["app_id"] != "app" || credentials["app_secret"] != "secret" {
			t.Errorf("credentials = %v", credentials)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"tenant-token","expire":7200}`))
	}))
	defer server.Close()

	source := NewTenantTokenSource("app", "secret")
	source.baseURL = server.URL
	source.http = server.Client()

	const callers = 12
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := source.Token(context.Background())
			if err == nil && token != "tenant-token" {
				t.Errorf("Token = %q", token)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("token refresh requests = %d, want 1", got)
	}
}

func TestTenantTokenSourceRejectsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":10003,"msg":"invalid app"}`))
	}))
	defer server.Close()
	source := NewTenantTokenSource("app", "secret")
	source.baseURL = server.URL
	source.http = server.Client()

	if _, err := source.Token(context.Background()); err == nil {
		t.Fatal("Token error = nil")
	}
}
