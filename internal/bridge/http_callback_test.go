package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

func TestCallbackHTTPHandlerDispatchesAction(t *testing.T) {
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: 120}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	service := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := service.HandleMessage(t.Context(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new hello"}); err != nil {
		t.Fatal(err)
	}
	if err := service.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	body := `{"operator":{"open_id":"u1"},"action":{"value":{"session":"claude:chat:message:msg-1","action_id":"stop"}}}`
	req := httptest.NewRequest(http.MethodPost, "/card/callback", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewCallbackHTTPHandler(service).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var bodyResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &bodyResp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if bodyResp["card"] == nil {
		t.Fatalf("response body = %#v, want card payload", bodyResp)
	}
	events := renderer.Events()
	if got := events[len(events)-1]; got.Type != "stopped" || !got.StopButton.Disabled || got.HeaderTemplate != "grey" {
		t.Fatalf("last event = %#v, want disabled stop", got)
	}
}

func TestCallbackHTTPHandlerRespondsToChallenge(t *testing.T) {
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	service := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/card/callback", strings.NewReader(`{"challenge":"abc123"}`))
	rec := httptest.NewRecorder()
	NewCallbackHTTPHandler(service).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body["challenge"] != "abc123" {
		t.Fatalf("challenge = %q, want abc123", body["challenge"])
	}
}
