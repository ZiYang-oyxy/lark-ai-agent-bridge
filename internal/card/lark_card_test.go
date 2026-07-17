package card

import "testing"

func TestBuildLarkCardIncludesActionsAndMeta(t *testing.T) {
	card := BuildLarkCard(Event{
		Type:      "authorization",
		SessionID: "claude:chat",
		Segments:  []Segment{{Kind: SegmentTool, Text: "tool_search requests permission"}},
		Actions:   AuthorizationActions(),
		Meta:      Meta{Agent: "claude", Tokens: 42, WorkDir: "/tmp/work", Status: "running"},
	})
	if card["schema"] != "2.0" {
		t.Fatalf("REDACTED", card["REDACTED"])
	}
	body := card["body"].(map[string]any)
	elements, ok := body["elements"].([]any)
	if !ok || len(elements) == 0 {
		t.Fatalf("elements missing: %#v", body["elements"])
	}
	foundButtons := 0
	foundMeta := false
	for _, el := range elements {
		m, _ := el.(map[string]any)
		switch m["tag"] {
		case "button":
			foundButtons++
			behaviors := m["behaviors"].([]any)
			value := behaviors[0].(map[string]any)["value"].(map[string]any)
			if value["session"] != "claude:chat" {
				t.Fatalf("button value = %#v, want session", value)
			}
		case "markdown":
			if m["element_id"] == "meta" {
				foundMeta = true
			}
		}
	}
	if foundButtons != 4 {
		t.Fatalf("button count = %d, want 4", foundButtons)
	}
	if !foundMeta {
		t.Fatal("meta markdown missing")
	}
}

func TestBuildLarkCardIncludesDisabledStopButton(t *testing.T) {
	card := BuildLarkCard(Event{Type: "stream", SessionID: "claude:chat", StopButton: StopButton{Visible: true, Disabled: true}})
	elements := card["body"].(map[string]any)["elements"].([]any)
	button := elements[0].(map[string]any)
	if button["disabled"] != true {
		t.Fatalf("button disabled = %#v, want true", button["disabled"])
	}
	if button["type"] != "default" {
		t.Fatalf("button type = %#v, want default", button["type"])
	}
	text := button["REDACTED"].(map[string]any)
	if text["content"] != "已停止" {
		t.Fatalf("button text = %#v, want 已停止", text["content"])
	}
	if _, ok := button["behaviors"]; ok {
		t.Fatalf("disabled stop button should not include behaviors: %#v", button["behaviors"])
	}
}

func TestBuildLarkCardIncludesTerminateSessionAction(t *testing.T) {
	payload := BuildLarkCard(Event{Type: "idle_reminder", SessionID: "claude:chat", Actions: TerminateSessionActions(false)})
	header := payload["header"].(map[string]any)
	title := header["title"].(map[string]any)
	if title["content"] != "会话闲置提醒" {
		t.Fatalf("title = %#v, want idle reminder title", title["content"])
	}
	elements := payload["body"].(map[string]any)["elements"].([]any)
	button := elements[0].(map[string]any)
	if button["type"] != "danger" {
		t.Fatalf("button type = %#v, want danger", button["type"])
	}
	behavior := button["behaviors"].([]any)[0].(map[string]any)
	value := behavior["value"].(map[string]any)
	if value["action_id"] != "terminate_session" || value["session"] != "claude:chat" {
		t.Fatalf("button value = %#v, want terminate_session for session", value)
	}
}

func TestBuildLarkCardFormatsRichSegments(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type: "stream",
		Segments: []Segment{
			{Kind: SegmentText, Text: "plain output"},
			{Kind: SegmentThought, Text: "inspect plan"},
			{Kind: SegmentTool, Text: "Bash(ls)"},
			{Kind: SegmentError, Text: "failed"},
		},
	})
	elements := payload["body"].(map[string]any)["elements"].([]any)
	var contents []string
	for _, element := range elements {
		m := element.(map[string]any)
		if m["REDACTED"] == "REDACTED" {
			contents = append(contents, m["content"].(string))
		}
	}
	want := []string{
		"plain output",
		"**Thinking**\ninspect plan",
		"**Tool**\nBash(ls)",
		"**Error**\nfailed",
	}
	if len(contents) != len(want) {
		t.Fatalf("markdown contents = %#v, want %#v", contents, want)
	}
	for i := range want {
		if contents[i] != want[i] {
			t.Fatalf("content[%d] = %q, want %q", i, contents[i], want[i])
		}
	}
}
