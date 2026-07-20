package card

import (
	"fmt"
	"strings"
	"testing"
)

func TestBuildLarkCardRendersGrantURLAsOpenURLBehavior(t *testing.T) {
	secretURL := "https://accounts.example/grant-secret"
	payload := BuildLarkCard(Event{Type: "scope_grant_pending", SessionID: "config", Actions: []Action{{ID: "grant", Label: "打开授权页面", URL: secretURL}}})
	text := fmt.Sprint(payload)
	if !strings.Contains(text, "open_url") || !strings.Contains(text, secretURL) || strings.Contains(text, "action_id:grant") {
		t.Fatalf("payload = %#v", payload)
	}
}
