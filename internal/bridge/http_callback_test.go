package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/tmux"
)

func TestCallbackHTTPHandlerDispatchesAction(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	renderer := card.NewFakeRenderer()
	runner := tmux.NewRecordingRunner()
	service := NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, runner), audit.NewRecorder())
	if err := service.HandleMessage(t.Context(), Message{ChatID: "chat", Sender: "u1", Text: "/claude hello"}); err != nil {
		t.Fatal(err)
	}
	body := `{"operator":{"open_id":"u1"},"action":{"value":{"session":"claude:chat","action_id":"stop"}}}`
	req := httptest.NewRequest(http.MethodPost, "/card/callback", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewCallbackHTTPHandler(service).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	foundInterrupt := false
	for _, cmd := range runner.Snapshot() {
		for _, arg := range cmd.Args {
			if arg == "C-c" {
				foundInterrupt = true
			}
		}
	}
	if !foundInterrupt {
		t.Fatalf("C-c not found in %#v", runner.Snapshot())
	}
}

func TestCallbackHTTPHandlerRespondsToChallenge(t *testing.T) {
	cfg := config.Config{TmuxSession: config.DefaultTmuxSession, DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000}
	service := NewService(cfg, card.NewFakeRenderer(), tmux.NewManager(cfg.TmuxSession, tmux.NewRecordingRunner()), audit.NewRecorder())
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
