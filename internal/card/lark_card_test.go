package card

import (
	"strings"
	"testing"
)

func TestBuildLarkCardIncludesActionsAndMeta(t *testing.T) {
	card := BuildLarkCard(Event{
		Type:      "workdir_confirm",
		SessionID: "claude:chat",
		Segments:  []Segment{{Kind: SegmentText, Text: "Workdir does not exist: /tmp/work"}},
		Actions:   WorkDirCreateActions("/tmp/work"),
		Meta:      Meta{Agent: "REDACTED", Model: "REDACTED", RunTokens: 42, TotalTokens: 4200, User: "REDACTED", IP: "REDACTED", WorkDir: "REDACTED", Status: "REDACTED"},
	})
	if card["schema"] != "2.0" {
		t.Fatalf("schema = %#v, want 2.0", card["schema"])
	}
	body := card["body"].(map[string]any)
	elements, ok := body["elements"].([]any)
	if !ok || len(elements) == 0 {
		t.Fatalf("elements missing: %#v", body["elements"])
	}
	foundButtons := 0
	foundDivider := false
	var columnSets []map[string]any
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
		case "hr":
			foundDivider = true
		case "column_set":
			columnSets = append(columnSets, m)
		}
	}
	if foundButtons != 2 {
		t.Fatalf("button count = %d, want 2", foundButtons)
	}
	if !foundDivider {
		t.Fatal("meta divider missing")
	}
	if len(columnSets) != 2 {
		t.Fatalf("column sets = %#v, want primary and runtime rows", columnSets)
	}
	metaContent := columnSetText(columnSets[0]) + "\n" + columnSetText(columnSets[1])
	if containsAny(metaContent, "agent=", "model=", "workdir=", "status=") {
		t.Fatalf("meta content contains machine prefixes: %q", metaContent)
	}
	if !containsAll(metaContent, "REDACTED", "REDACTED", "REDACTED", "REDACTED", "REDACTED", "REDACTED") {
		t.Fatalf("REDACTED", metaContent)
	}
	assertColumnWeights(t, columnSets[0], []int{10, 14, 18})
	assertColumnWeights(t, columnSets[1], []int{10, 12, 30})
}

func TestBuildLarkCardLabelsRequestedAndActualModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		info ModelInfo
		want []string
		not  []string
	}{
		{name: "reported actual", info: ModelInfo{Requested: "opus", Actual: "claude-opus-4-1", Effort: "high"}, want: []string{"requested: opus", "actual: claude-opus-4-1", "effort: high"}},
		{name: "missing actual", info: ModelInfo{Requested: "sonnet", Effort: "medium"}, want: []string{"requested: sonnet", "actual: unknown", "effort: medium"}, not: []string{"actual: sonnet"}},
		{name: "default requested", info: ModelInfo{Requested: "default", Effort: "low"}, want: []string{"requested: default", "actual: unknown", "effort: low"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := BuildLarkCard(Event{Type: "result", Meta: Meta{Agent: "claude", Model: "legacy-must-not-win", ModelInfo: tc.info}})
			elements := payload["body"].(map[string]any)["elements"].([]any)
			var text string
			for _, raw := range elements {
				element := raw.(map[string]any)
				if element["tag"] == "column_set" {
					text += columnSetText(element)
				}
			}
			if !containsAll(text, tc.want...) || containsAny(text, tc.not...) || strings.Contains(text, "legacy-must-not-win") {
				t.Fatalf("model provenance text = %q, want %#v without %#v", text, tc.want, tc.not)
			}
		})
	}
}

func TestBuildLarkCardRendersRuntimeConfigForm(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:      "config",
		SessionID: "claude:chat:message:config-1",
		ConfigForm: &ConfigForm{
			Model:      "opus",
			Effort:     "high",
			ReplyMode:  "latest-card",
			Models:     []string{"REDACTED", "REDACTED", "REDACTED", "REDACTED"},
			Efforts:    []string{"default", "low", "medium", "high"},
			ReplyModes: []string{"append", "append-clean-card", "latest-card"},
		},
		Segments:  []Segment{{Kind: SegmentText, Text: "must not appear beside the form"}},
		Streaming: true,
	})
	body := payload["body"].(map[string]any)
	elements := body["elements"].([]any)
	var form map[string]any
	for _, raw := range elements {
		element := raw.(map[string]any)
		if element["tag"] == "form" {
			form = element
		}
		if element["element_id"] == "answer" {
			t.Fatalf("config card mixed streaming answer into form: %#v", element)
		}
	}
	if form == nil || form["name"] != "runtime_config" {
		t.Fatalf("config form = %#v", form)
	}
	controls := form["elements"].([]any)
	selects := map[string]map[string]any{}
	var submit map[string]any
	for _, raw := range controls {
		control := raw.(map[string]any)
		if control["tag"] == "select_static" {
			selects[control["name"].(string)] = control
		}
		if control["tag"] == "button" {
			submit = control
		}
	}
	if len(selects) != 3 || selects["model"]["initial_option"] != "opus" || selects["effort"]["initial_option"] != "high" || selects["reply_mode"]["initial_option"] != "latest-card" {
		t.Fatalf("select controls = %#v", selects)
	}
	if len(selects["model"]["options"].([]any)) != 4 || len(selects["effort"]["options"].([]any)) != 4 || len(selects["reply_mode"]["options"].([]any)) != 3 {
		t.Fatalf("select options = model %#v effort %#v reply %#v", selects["model"]["options"], selects["effort"]["options"], selects["reply_mode"]["options"])
	}
	if submit == nil || submit["form_action_type"] != "submit" {
		t.Fatalf("submit button = %#v", submit)
	}
	behaviors := submit["behaviors"].([]any)
	value := behaviors[0].(map[string]any)["value"].(map[string]any)
	if value["action_id"] != "config.save" || value["session"] != "claude:chat:message:config-1" {
		t.Fatalf("submit callback = %#v", value)
	}
	if payload["config"].(map[string]any)["streaming_mode"] != false {
		t.Fatalf("config form must disable streaming: %#v", payload["config"])
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
	text := button["text"].(map[string]any)
	if text["content"] != "已停止" {
		t.Fatalf("button text = %#v, want 已停止", text["content"])
	}
	if _, ok := button["behaviors"]; ok {
		t.Fatalf("disabled stop button should not include behaviors: %#v", button["behaviors"])
	}
}

func TestBuildLarkCardCleanResultOmitsAgentPanels(t *testing.T) {
	payload := BuildLarkCard(Event{Type: "result", HideAgentPanels: true, Segments: []Segment{{Kind: SegmentText, Text: "answer"}}})
	elements := payload["body"].(map[string]any)["elements"].([]any)
	for _, raw := range elements {
		element := raw.(map[string]any)
		if element["element_id"] == "panel_thought" || element["element_id"] == "panel_tools" {
			t.Fatalf("clean result panel = %#v", element)
		}
	}
}

func TestBuildLarkCardUsesFinalStopButtonLabels(t *testing.T) {
	tests := []struct {
		eventType string
		want      string
	}{
		{eventType: "result", want: "已完成"},
		{eventType: "error", want: "已结束"},
		{eventType: "stopped", want: "已停止"},
	}
	for _, tt := range tests {
		payload := BuildLarkCard(Event{Type: tt.eventType, SessionID: "claude:chat", StopButton: StopButton{Visible: true, Disabled: true}})
		elements := payload["REDACTED"].(map[string]any)["REDACTED"].([]any)
		button := elements[len(elements)-1].(map[string]any)
		if button["disabled"] != true || button["type"] != "default" {
			t.Fatalf("%s button = %#v, want disabled default", tt.eventType, button)
		}
		text := button["text"].(map[string]any)
		if text["content"] != tt.want {
			t.Fatalf("%s text = %#v, want %s", tt.eventType, text["content"], tt.want)
		}
	}
}

func TestBuildLarkCardDisablesWorkdirTerminalActions(t *testing.T) {
	tests := []struct {
		eventType    string
		wantTitle    string
		wantTemplate string
	}{
		{eventType: "workdir_created", wantTitle: "✅ 工作目录已创建", wantTemplate: "green"},
		{eventType: "workdir_cancelled", wantTitle: "⏹ 已取消", wantTemplate: "grey"},
	}
	for _, tt := range tests {
		payload := BuildLarkCard(Event{
			Type:      tt.eventType,
			SessionID: "claude:chat:message:msg-1",
			Segments:  []Segment{{Kind: SegmentText, Text: "workdir /tmp/work"}},
			Actions:   WorkDirActions("/tmp/work", true),
		})
		header := payload["header"].(map[string]any)
		if header["template"] != tt.wantTemplate {
			t.Fatalf("%s template = %#v, want %s", tt.eventType, header["template"], tt.wantTemplate)
		}
		title := header["title"].(map[string]any)
		if title["content"] != tt.wantTitle {
			t.Fatalf("%s title = %#v, want %s", tt.eventType, title["content"], tt.wantTitle)
		}
		elements := payload["body"].(map[string]any)["elements"].([]any)
		disabledButtons := 0
		for _, raw := range elements {
			button := raw.(map[string]any)
			if button["tag"] != "button" {
				continue
			}
			disabledButtons++
			if button["disabled"] != true {
				t.Fatalf("%s button = %#v, want disabled", tt.eventType, button)
			}
			if _, ok := button["behaviors"]; ok {
				t.Fatalf("%s disabled action should not include behaviors: %#v", tt.eventType, button)
			}
		}
		if disabledButtons != 2 {
			t.Fatalf("%s disabled buttons = %d, want 2", tt.eventType, disabledButtons)
		}
	}
}

func TestBuildLarkCardUsesDynamicHeaderAndStreamingMode(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:           "stream",
		HeaderTitle:    "🧠 正在推理 · ⏱ 3s",
		HeaderTemplate: "blue",
		Streaming:      true,
		Activity:       "reasoning",
	})
	header := payload["header"].(map[string]any)
	if header["template"] != "blue" {
		t.Fatalf("template = %#v, want blue", header["template"])
	}
	title := header["title"].(map[string]any)
	if title["content"] != "🧠 正在推理 · ⏱ 3s" {
		t.Fatalf("title = %#v", title["content"])
	}
	config := payload["config"].(map[string]any)
	if config["streaming_mode"] != true {
		t.Fatalf("streaming mode = %#v, want true", config["streaming_mode"])
	}
	if _, ok := config["streaming_config"]; !ok {
		t.Fatalf("streaming config missing: %#v", config)
	}
}

func TestBuildLarkCardSupportsStoppedGreyHeader(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:           "stopped",
		HeaderTitle:    "⏹ 已停止 · ⏱ 5s",
		HeaderTemplate: "grey",
		StopButton:     StopButton{Visible: true, Disabled: true},
	})
	header := payload["header"].(map[string]any)
	if header["template"] != "grey" {
		t.Fatalf("template = %#v, want grey", header["template"])
	}
	title := header["title"].(map[string]any)
	if title["content"] != "⏹ 已停止 · ⏱ 5s" {
		t.Fatalf("title = %#v", title["content"])
	}
	config := payload["config"].(map[string]any)
	if config["streaming_mode"] != false {
		t.Fatalf("streaming mode = %#v, want false", config["streaming_mode"])
	}
}

func containsAny(text string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

func columnSetText(columnSet map[string]any) string {
	var b strings.Builder
	for _, rawColumn := range columnSet["columns"].([]any) {
		column := rawColumn.(map[string]any)
		elements := column["elements"].([]any)
		for _, rawElement := range elements {
			element := rawElement.(map[string]any)
			if element["tag"] == "markdown" {
				b.WriteString(element["content"].(string))
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func assertColumnWeights(t *testing.T, columnSet map[string]any, want []int) {
	t.Helper()
	columns := columnSet["columns"].([]any)
	if len(columns) != len(want) {
		t.Fatalf("columns = %#v, want %d", columns, len(want))
	}
	for i, rawColumn := range columns {
		column := rawColumn.(map[string]any)
		if column["weight"] != want[i] {
			t.Fatalf("column %d weight = %#v, want %d", i, column["weight"], want[i])
		}
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
	if toolElements[0]["content"] != "Bash(ls)" {
		t.Fatalf("tool panel = %#v", toolElements)
	}
}
