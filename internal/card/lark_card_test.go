package card

import "testing"

func TestBuildLarkCardIncludesActionsAndMeta(t *testing.T) {
	card := BuildLarkCard(Event{
		Type:      "workdir_confirm",
		SessionID: "claude:chat",
		Segments:  []Segment{{Kind: SegmentText, Text: "Workdir does not exist: /tmp/work"}},
		Actions:   WorkDirCreateActions("/tmp/work"),
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
	if foundButtons != 2 {
		t.Fatalf("button count = %d, want 2", foundButtons)
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

func TestBuildLarkCardFormatsRichSegments(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type: "result",
		Segments: []Segment{
			{Kind: SegmentText, Text: "plain output"},
			{Kind: SegmentThought, Text: "inspect plan"},
			{Kind: SegmentTool, Text: "Bash(ls)"},
			{Kind: SegmentError, Text: "failed"},
		},
	})
	elements := payload["body"].(map[string]any)["elements"].([]any)
	var markdownContents []string
	var panels []map[string]any
	for _, element := range elements {
		m := element.(map[string]any)
		if m["tag"] == "markdown" {
			markdownContents = append(markdownContents, m["content"].(string))
		}
		if m["tag"] == "collapsible_panel" {
			panels = append(panels, m)
		}
	}
	if len(markdownContents) == 0 || markdownContents[0] != "plain output\n\n**Error**\nfailed" {
		t.Fatalf("answer markdown = %#v", markdownContents)
	}
	if len(panels) != 2 {
		t.Fatalf("panels = %#v, want thought and tool panels", panels)
	}
	thoughtElements := panels[0]["elements"].([]map[string]any)
	if thoughtElements[0]["content"] != "inspect plan" {
		t.Fatalf("thought panel = %#v", thoughtElements)
	}
	toolElements := panels[1]["elements"].([]map[string]any)
	if toolElements[0]["REDACTED"] != "REDACTED" {
		t.Fatalf("tool panel = %#v", toolElements)
	}
}
