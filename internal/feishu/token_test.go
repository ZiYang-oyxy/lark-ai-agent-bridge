package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingTokenDoer struct {
	started chan struct{}
	release chan struct{}
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func (d *blockingTokenDoer) Do(*http.Request) (*http.Response, error) {
	select {
	case <-d.started:
	default:
		close(d.started)
	}
	<-d.release
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"ok","tenant_access_token":"tenant-token","expire":7200}`)),
		Header:     make(http.Header),
	}, nil
}

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

func TestTenantTokenSourceWaiterHonorsContextWhileRefreshIsInFlight(t *testing.T) {
	doer := &blockingTokenDoer{started: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseLeader := func() { releaseOnce.Do(func() { close(doer.release) }) }
	source := NewTenantTokenSource("app", "secret")
	source.http = doer
	leaderDone := make(chan error, 1)
	go func() {
		_, err := source.Token(context.Background())
		leaderDone <- err
	}()
	defer func() {
		releaseLeader()
		select {
		case err := <-leaderDone:
			if err != nil {
				t.Errorf("leader refresh: %v", err)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("leader did not exit after release")
		}
	}()
	select {
	case <-doer.started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("leader refresh did not start")
	}

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedDoneContext{Context: baseCtx, observed: make(chan struct{})}
	waiterDone := make(chan error, 1)
	go func() {
		_, err := source.Token(ctx)
		waiterDone <- err
	}()
	select {
	case <-ctx.observed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("waiter did not enter context-aware refresh wait")
	}
	select {
	case err := <-waiterDone:
		t.Fatalf("waiter returned before cancellation: %v", err)
	default:
	}
	cancel()

	select {
	case err := <-waiterDone:
		if err != context.Canceled {
			t.Fatalf("waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("waiter did not honor canceled context while refresh was in flight")
	}
	releaseLeader()
}
