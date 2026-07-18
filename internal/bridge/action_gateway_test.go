package bridge

import (
	"context"
	"sync"
	"testing"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

type countingInteractionFencer struct {
	mu                sync.Mutex
	entered, released int
}

func (f *countingInteractionFencer) BeginCardInteraction(string) func() {
	f.mu.Lock()
	f.entered++
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.released++
			f.mu.Unlock()
		})
	}
}

func TestActionGatewayAlwaysReleasesFence(t *testing.T) {
	fencer := &countingInteractionFencer{}
	service := NewService(config.Config{DefaultAgent: "claude", DefaultWorkDir: t.TempDir()}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	_, _ = (ActionGateway{Service: service, Fencer: fencer}).Handle(context.Background(), ActionRequest{SessionID: "missing", ActionID: "stop"})
	fencer.mu.Lock()
	defer fencer.mu.Unlock()
	if fencer.entered != 1 || fencer.released != 1 {
		t.Fatalf("fence entered=%d released=%d", fencer.entered, fencer.released)
	}
}

func TestActionGatewayNilServiceStillReleasesFence(t *testing.T) {
	fencer := &countingInteractionFencer{}
	if _, err := (ActionGateway{Fencer: fencer}).Handle(context.Background(), ActionRequest{SessionID: "session"}); err == nil {
		t.Fatal("Handle() error=nil")
	}
	if fencer.entered != 1 || fencer.released != 1 {
		t.Fatalf("fence entered=%d released=%d", fencer.entered, fencer.released)
	}
}
