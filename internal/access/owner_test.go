package access

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRefreshOwnerAndController(t *testing.T) {
	source := &fakeOwnerSource{results: []any{"ou_first", "ou_second"}}
	controls := NewRuntimeControls()
	controller := OwnerRefreshController{Controls: controls, Source: source, AppID: "cli_app", Interval: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()

	deadline := time.Now().Add(time.Second)
	for source.Calls() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := controls.Snapshot(); got.BotOwnerID != "ou_second" || got.OwnerRefreshState != OwnerRefreshOK {
		t.Fatalf("snapshot = %#v", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller did not stop")
	}
}

func TestRefreshOwnerFailureRetainsCachedOwner(t *testing.T) {
	controls := NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_cached")
	source := &fakeOwnerSource{results: []any{errors.New("permission denied")}}
	if err := RefreshOwner(context.Background(), controls, source, "cli_app"); err == nil {
		t.Fatal("RefreshOwner succeeded, want error")
	}
	snapshot := controls.Snapshot()
	if snapshot.BotOwnerID != "ou_cached" || snapshot.OwnerRefreshState != OwnerRefreshFailed {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

type fakeOwnerSource struct {
	mu      sync.Mutex
	results []any
	calls   int
}

func (f *fakeOwnerSource) GetOwner(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.results) == 0 {
		return "", errors.New("no result")
	}
	next := f.results[0]
	f.results = f.results[1:]
	if err, ok := next.(error); ok {
		return "", err
	}
	return next.(string), nil
}

func (f *fakeOwnerSource) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
