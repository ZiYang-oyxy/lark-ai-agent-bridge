package bridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
)

type scopeInspectorStub struct {
	mu     sync.Mutex
	states []feishu.ScopeState
	errs   []error
	calls  int
}

func (s *scopeInspectorStub) InspectTenantScope(context.Context, string, string) (feishu.ScopeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	state := s.states[len(s.states)-1]
	if i < len(s.states) {
		state = s.states[i]
	}
	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return state, err
}

func (s *scopeInspectorStub) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type scopeGrantProviderStub struct {
	grant feishu.ScopeGrant
	err   error
	calls int
}

func (s *scopeGrantProviderStub) Begin(context.Context, string, []string) (feishu.ScopeGrant, error) {
	s.calls++
	return s.grant, s.err
}

func TestEnsureGroupMessageScopeSkipsMentionOnlyAndPresent(t *testing.T) {
	for _, mode := range []config.GroupMessageMode{config.GroupMessageModeMentionOnly, config.GroupMessageModeAll} {
		inspector := &scopeInspectorStub{states: []feishu.ScopeState{feishu.ScopePresent}}
		renderer := card.NewFakeRenderer()
		svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
		svc.ScopeInspector = inspector
		svc.AccessAppID = "cli_app"
		svc.ensureGroupMessageScope("config", mode)
		time.Sleep(20 * time.Millisecond)
		if mode == config.GroupMessageModeMentionOnly && inspector.Calls() != 0 {
			t.Fatalf("mention_only scope calls = %d", inspector.Calls())
		}
		if mode == config.GroupMessageModeAll && inspector.Calls() != 1 {
			t.Fatalf("all mode scope calls = %d", inspector.Calls())
		}
		if len(renderer.Events()) != 0 {
			t.Fatalf("events = %#v", renderer.Events())
		}
		_ = svc.Shutdown(context.Background())
	}
}

func TestEnsureGroupMessageScopeGrantsAndConfirmsPresent(t *testing.T) {
	done := make(chan error, 1)
	inspector := &scopeInspectorStub{states: []feishu.ScopeState{feishu.ScopeMissing, feishu.ScopePresent}}
	provider := &scopeGrantProviderStub{grant: feishu.ScopeGrant{URL: "https://accounts.example/grant-secret", ExpiresAt: time.Now().Add(time.Minute), Done: done}}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), recorder)
	svc.ScopeInspector, svc.ScopeGrants, svc.AccessAppID = inspector, provider, "cli_app"
	svc.ensureGroupMessageScope("config", config.GroupMessageModeAll)
	waitForEvents(t, renderer, 1)
	pending := renderer.Events()[0]
	if pending.Type != "scope_grant_pending" || len(pending.Actions) != 1 || pending.Actions[0].URL == "" {
		t.Fatalf("pending event = %#v", pending)
	}
	done <- nil
	close(done)
	waitForEvents(t, renderer, 2)
	if renderer.Events()[1].Type != "scope_granted" || inspector.Calls() != 2 || provider.calls != 1 {
		t.Fatalf("events=%#v inspect=%d grants=%d", renderer.Events(), inspector.Calls(), provider.calls)
	}
	for _, event := range recorder.Events() {
		if event.Detail == provider.grant.URL {
			t.Fatal("audit leaked grant URL")
		}
	}
	_ = svc.Shutdown(context.Background())
}

func TestEnsureGroupMessageScopeReportsUnknownWithoutGrant(t *testing.T) {
	inspector := &scopeInspectorStub{states: []feishu.ScopeState{feishu.ScopeUnknown}, errs: []error{errors.New("temporary")}}
	provider := &scopeGrantProviderStub{}
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.ScopeInspector, svc.ScopeGrants, svc.AccessAppID = inspector, provider, "cli_app"
	svc.ensureGroupMessageScope("config", config.GroupMessageModeParticipatedTopics)
	waitForEvents(t, renderer, 1)
	if renderer.Events()[0].Type != "scope_unknown" || provider.calls != 0 {
		t.Fatalf("events=%#v grants=%d", renderer.Events(), provider.calls)
	}
	_ = svc.Shutdown(context.Background())
}
