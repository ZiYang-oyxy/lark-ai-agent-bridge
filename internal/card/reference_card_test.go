package card

import (
	"strings"
	"testing"
)

func TestReferenceCardInitialRunningMatchesReferenceShape(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type: "running", ReferenceCardLayout: true, Streaming: true, Activity: "reasoning",
		SessionID: "session-1", StopButton: StopButton{Visible: true, GrantID: "grant-1"},
		Meta: Meta{Agent: "claude", WorkDir: "/must/not/render", ShowMetaRowAgent: true, ShowMetaRowRuntime: true},
	})
	if _, ok := payload["header"]; ok {
		t.Fatalf("reference card unexpectedly has header: %#v", payload["header"])
	}
	elements := referenceElements(t, payload)
	if len(elements) != 2 {
		t.Fatalf("elements = %#v, want activity + stop", elements)
	}
	if elements[0]["content"] != "🧠 正在思考" || elements[0]["text_size"] != "notation" {
		t.Fatalf("activity = %#v", elements[0])
	}
	button := elements[1]
	if button["tag"] != "button" || button["type"] != "danger" || button["disabled"] != nil {
		t.Fatalf("stop button = %#v", button)
	}
	if button["text"].(map[string]any)["content"] != "⏹ 终止" {
		t.Fatalf("stop label = %#v", button["text"])
	}
	behavior := button["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
	if behavior["action_id"] != "stop" || behavior["session"] != "session-1" || behavior["grant_id"] != "grant-1" {
		t.Fatalf("stop callback = %#v", behavior)
	}
	serialized := sprintPayload(payload)
	if strings.Contains(serialized, "/must/not/render") {
		t.Fatalf("reference card rendered bridge meta rows: %s", serialized)
	}
}

func TestReferenceCardOrdersReasoningTextAndToolLifecycle(t *testing.T) {
	segments := []Segment{
		{Kind: SegmentThought, Text: "inspect options"},
		{Kind: SegmentText, Text: "先检查"},
		{Kind: SegmentTool, Text: "raw use", Tool: &ToolMeta{ID: "t1", Name: "Bash", Summary: "pwd", Phase: "use", Input: map[string]any{"command": "pwd"}}},
		{Kind: SegmentTool, Text: "raw result", Tool: &ToolMeta{ID: "t1", Phase: "result", Output: "/repo"}},
		{Kind: SegmentTool, Text: "raw use", Tool: &ToolMeta{ID: "t2", Name: "Read", Summary: "/repo/a.ts", Phase: "use", Input: map[string]any{"file_path": "/repo/a.ts"}}},
		{Kind: SegmentTool, Text: "raw result", Tool: &ToolMeta{ID: "t2", Phase: "result", Output: "ENOENT", IsError: true}},
		{Kind: SegmentText, Text: "最终答案"},
	}
	payload := BuildLarkCard(Event{Type: "result", ReferenceCardLayout: true, Segments: segments})
	elements := referenceElements(t, payload)
	if len(elements) != 5 {
		t.Fatalf("elements = %#v, want reasoning + text + 2 tools + text", elements)
	}
	if elements[0]["tag"] != "collapsible_panel" || elements[0]["expanded"] != false || referencePanelTitle(elements[0]) != "🧠 **思考完成，点击查看**" {
		t.Fatalf("reasoning panel = %#v", elements[0])
	}
	if elements[1]["content"] != "先检查" || elements[4]["content"] != "最终答案" {
		t.Fatalf("text order = %#v", elements)
	}
	firstTool, secondTool := elements[2], elements[3]
	if referencePanelTitle(firstTool) != "✅ **Bash** — pwd" || firstTool["expanded"] != false {
		t.Fatalf("first tool = %#v", firstTool)
	}
	if body := referencePanelBody(firstTool); !strings.Contains(body, "**Command**\n```bash\npwd") || !strings.Contains(body, "**Output**\n```\n/repo") {
		t.Fatalf("first tool body = %q", body)
	}
	if referencePanelTitle(secondTool) != "❌ **Read** — /repo/a.ts" || secondTool["border"].(map[string]any)["color"] != "red" {
		t.Fatalf("second tool = %#v", secondTool)
	}
	if body := referencePanelBody(secondTool); !strings.Contains(body, "**File** `/repo/a.ts`") || !strings.Contains(body, "**Error**\n```\nENOENT") {
		t.Fatalf("second tool body = %q", body)
	}
}

func TestReferenceCardCollapsesThreeConsecutiveTools(t *testing.T) {
	segments := make([]Segment, 0, 6)
	for index, name := range []string{"Bash", "Read", "Edit"} {
		id := string(rune('1' + index))
		segments = append(segments,
			Segment{Kind: SegmentTool, Tool: &ToolMeta{ID: id, Name: name, Summary: name + " summary", Phase: "use"}},
		)
		if index < 2 {
			segments = append(segments, Segment{Kind: SegmentTool, Tool: &ToolMeta{ID: id, Phase: "result", Output: "ok"}})
		}
	}
	running := referenceElements(t, BuildLarkCard(Event{
		Type: "running", ReferenceCardLayout: true, Streaming: true, Activity: "tool", Segments: segments,
		StopButton: StopButton{Visible: true},
	}))
	if len(running) != 4 {
		t.Fatalf("running elements = %#v, want summary + latest + footer + stop", running)
	}
	if title := referencePanelTitle(running[0]); title != "☕ **2 个工具调用**" {
		t.Fatalf("running summary title = %q", title)
	}
	if title := referencePanelTitle(running[1]); title != "⏳ **Edit** — Edit summary" || running[1]["expanded"] != true {
		t.Fatalf("latest running tool = %#v", running[1])
	}

	segments = append(segments, Segment{Kind: SegmentTool, Tool: &ToolMeta{ID: "3", Phase: "result", Output: "ok"}})
	terminal := referenceElements(t, BuildLarkCard(Event{Type: "result", ReferenceCardLayout: true, Segments: segments}))
	if len(terminal) != 1 || referencePanelTitle(terminal[0]) != "☕ **3 个工具调用（已结束）**" {
		t.Fatalf("terminal summary = %#v", terminal)
	}
	body := referencePanelBody(terminal[0])
	for _, want := range []string{"✅ **Bash**", "✅ **Read**", "✅ **Edit**"} {
		if !strings.Contains(body, want) {
			t.Fatalf("summary body %q missing %q", body, want)
		}
	}
	if strings.Contains(body, "**Output**") {
		t.Fatalf("terminal summary leaked tool body: %q", body)
	}
}

func TestReferenceCardSeparatesToolGroupsWithoutElementIDs(t *testing.T) {
	elements := referenceElements(t, BuildLarkCard(Event{Type: "result", ReferenceCardLayout: true, Segments: []Segment{
		{Kind: SegmentTool, Tool: &ToolMeta{ID: "before", Name: "Bash", Phase: "result", Output: "one"}},
		{Kind: SegmentText, Text: "between"},
		{Kind: SegmentTool, Tool: &ToolMeta{ID: "after", Name: "Read", Phase: "result", Output: "two"}},
	}}))
	if len(elements) != 3 || referencePanelTitle(elements[0]) != "✅ **Bash**" || elements[1]["content"] != "between" || referencePanelTitle(elements[2]) != "✅ **Read**" {
		t.Fatalf("separated tool groups = %#v", elements)
	}
	for _, element := range elements {
		if _, exists := element["element_id"]; exists {
			t.Fatalf("reference element unexpectedly has element_id: %#v", element)
		}
	}
}

func TestReferenceCardTerminalNotes(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  string
	}{
		{name: "empty", event: Event{Type: "result", ReferenceCardLayout: true}, want: "_（未返回内容）_"},
		{name: "stopped", event: Event{Type: "stopped", ReferenceCardLayout: true, Segments: []Segment{{Kind: SegmentText, Text: "partial"}}}, want: "_⏹ 已被中断_"},
		{name: "error", event: Event{Type: "error", ReferenceCardLayout: true, Segments: []Segment{{Kind: SegmentError, Text: "boom"}}}, want: "⚠️ agent 失败：boom"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			elements := referenceElements(t, BuildLarkCard(test.event))
			if got := elements[len(elements)-1]["content"]; got != test.want {
				t.Fatalf("terminal note = %#v, want %q; elements=%#v", got, test.want, elements)
			}
		})
	}
}

func referenceElements(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw := payload["body"].(map[string]any)["elements"].([]any)
	elements := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		elements = append(elements, item.(map[string]any))
	}
	return elements
}

func referencePanelTitle(panel map[string]any) string {
	return panel["header"].(map[string]any)["title"].(map[string]any)["content"].(string)
}

func referencePanelBody(panel map[string]any) string {
	return panel["elements"].([]any)[0].(map[string]any)["content"].(string)
}

func sprintPayload(payload map[string]any) string {
	return strings.ReplaceAll(strings.TrimSpace(strings.Join(flattenReferenceStrings(payload), "\n")), "\r", "")
}

func flattenReferenceStrings(value any) []string {
	var out []string
	switch typed := value.(type) {
	case string:
		out = append(out, typed)
	case map[string]any:
		for _, item := range typed {
			out = append(out, flattenReferenceStrings(item)...)
		}
	case []any:
		for _, item := range typed {
			out = append(out, flattenReferenceStrings(item)...)
		}
	}
	return out
}
