package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"lark-agent-bridge/internal/feishueventlog"
)

func NewCallbackHTTPHandler(gateway ActionGateway) http.Handler {
	return NewCallbackHTTPHandlerWithRawEvents(gateway, nil)
}

func NewCallbackHTTPHandlerWithRawEvents(gateway ActionGateway, observer func(context.Context, feishueventlog.Event)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/card/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read callback body", http.StatusBadRequest)
			return
		}
		if observer != nil {
			observer(r.Context(), feishueventlog.Event{
				Transport: "http_callback",
				EventType: callbackEventType(body),
				Payload:   append([]byte(nil), body...),
			})
		}
		if challenge, ok := callbackChallenge(body); ok {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"challenge": challenge})
			return
		}
		req, err := ActionRequestFromCardCallback(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := gateway.Handle(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"ok": true}
		if gateway.Service == nil {
			http.Error(w, "action service unavailable", http.StatusInternalServerError)
			return
		}
		prepared, err := result.PrepareCard(gateway.Service.Config.CardMaxChars)
		if err != nil {
			result.CancelDeferred()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if prepared.CardJSON() != nil {
			resp["card"] = prepared.PayloadCopy()
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			result.CancelDeferred()
			return
		}
		result.StartDeferred()
	})
	return mux
}

func callbackEventType(body []byte) string {
	var envelope struct {
		Type   string `json:"type"`
		Header struct {
			EventType string `json:"event_type"`
		} `json:"header"`
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		if envelope.Header.EventType != "" {
			return envelope.Header.EventType
		}
		if envelope.Type != "" {
			return envelope.Type
		}
		if envelope.Challenge != "" {
			return "url_verification"
		}
	}
	return "card.callback"
}

func callbackChallenge(body []byte) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", false
	}
	challenge, _ := raw["challenge"].(string)
	if challenge == "" {
		return "", false
	}
	return challenge, true
}
