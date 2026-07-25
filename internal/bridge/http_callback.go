package bridge

import (
	"encoding/json"
	"io"
	"net/http"
)

func NewCallbackHTTPHandler(gateway ActionGateway) http.Handler {
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
