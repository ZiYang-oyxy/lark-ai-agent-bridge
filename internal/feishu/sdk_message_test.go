package feishu

import "testing"

func TestParseMessageTextFromTextPayload(t *testing.T) {
	got := parseMessageText(`{"text":"hello"}`)
	if got != "hello" {
		t.Fatalf("text = %q, want hello", got)
	}
}

func TestParseMessageTextFromPostPayload(t *testing.T) {
	raw := `{"zh_cn":{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"bot"},{"tag":"text","text":" /new status"}]]}}`
	got := parseMessageText(raw)
	if got != "/new status" {
		t.Fatalf("text = %q, want command text", got)
	}
}
