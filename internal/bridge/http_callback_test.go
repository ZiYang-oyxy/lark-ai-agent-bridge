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
	NewCallbackHTTPHandler(ActionGateway{Service: service}).ServeHTTP(rec, req)
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
	if !strings.Contains(rec.Body.String(), stopRequestedNotice) {
		t.Fatalf("response body = %s, want in-place stop notice", rec.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		events := renderer.Events()
		got := events[len(events)-1]
		if got.Type == "stopped" && got.StopButton.Disabled && got.HeaderTemplate == "grey" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("last event = %#v, want disabled stop", got)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCallbackHTTPHandlerRespondsToChallenge(t *testing.T) {
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	service := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/card/callback", strings.NewReader(`{"challenge":"abc123"}`))
	rec := httptest.NewRecorder()
	NewCallbackHTTPHandler(ActionGateway{Service: service}).ServeHTTP(rec, req)
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

func TestCallbackHTTPHandlerAcknowledgesUpdateBeforeRefresh(t *testing.T) {
	setUpdateTestVersion(t)
	started := make(chan struct{})
	release := make(chan struct{})
	prepared := &fakePreparedUpdate{restarted: make(chan struct{})}
	manager := &fakeUpdateManager{
		checkResult: updateAvailableResult(), prepared: prepared,
		refreshStarted: started, refreshRelease: release,
	}
	cfg := updateTestConfig(t)
	renderer := card.NewFakeRenderer()
	service := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	service.Updates = manager
	body := `{"operator":{"open_id":"admin"},"context":{"open_chat_id":"oc_test","open_message_id":"om_card"},"action":{"value":{"session":"help:test","action_id":"update.install","value":"1.2.0"}}}`
	req := httptest.NewRequest(http.MethodPost, "/card/callback", strings.NewReader(body))
	rec := httptest.NewRecorder()

	begin := time.Now()
	NewCallbackHTTPHandler(ActionGateway{Service: service}).ServeHTTP(rec, req)
	if elapsed := time.Since(begin); elapsed > 100*time.Millisecond {
		t.Fatalf("HTTP callback took %s while Refresh was blocked", elapsed)
	}
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"card"`) {
		t.Fatalf("status/body = %d / %s", rec.Code, rec.Body.String())
	}
	waitForEvents(t, renderer, 1)
	if got := renderer.Events()[0]; got.HeaderTitle != "Bridge 正在准备升级" {
		t.Fatalf("preparing event = %#v", got)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("deferred Refresh did not start after the response was written")
	}
	close(release)
	select {
	case <-prepared.restarted:
	case <-time.After(time.Second):
		t.Fatal("deferred update did not finish")
	}
}
