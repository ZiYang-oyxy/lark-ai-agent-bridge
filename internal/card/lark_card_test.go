package card

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestConfigCardIncludesCollapsedAccessPanel(t *testing.T) {
	payload := BuildLarkCard(Event{Type: "config", SessionID: "config", ConfigForm: &ConfigForm{
		AllowedUsers: []string{"ou_user"}, Admins: []string{"ou_admin"},
		AllowedChats: []AccessChat{{ID: "oc_123456789", Name: "Project"}}, OwnerState: "ok owner=present",
	}})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"访问控制", "collapsible_panel", "ou_user", "ou_admin", "Project", "456789", "ok owner=present"} {
		if !strings.Contains(text, want) {
			t.Fatalf("card missing %q: %s", want, text)
		}
	}
}

func TestBuildLarkCardRunningEmptyProcessOmitsPanel(t *testing.T) {
	// v2:运行中若思考/工具都为空,不为占位而显示空面板。
	payload := BuildLarkCard(Event{Type: "stream", Streaming: true, Segments: []Segment{{Kind: SegmentText, Text: "writing"}}})
	for _, raw := range payload["body"].(map[string]any)["elements"].([]any) {
		if m, _ := raw.(map[string]any); m["REDACTED"] == "REDACTED" {
			t.Fatalf("empty process should not render panel: %#v", m)
		}
	}
}

func TestBuildLarkCardMergesProcessPanelWithStableExpansion(t *testing.T) {
	// v2:思考+工具合并为单个 panel_process,运行中固定折叠,标题带工具计数。
	payload := BuildLarkCard(Event{
		Type:            "stream",
		Streaming:       true,
		ProcessExpanded: false,
		ToolCallCount:   3,
		Segments: []Segment{
			{Kind: SegmentText, Text: "answer"},
			{Kind: SegmentThought, Text: "thinking"},
			{Kind: SegmentTool, Text: "Bash(ls)"},
		},
	})
	var panels []map[string]any
	for _, raw := range payload["body"].(map[string]any)["elements"].([]any) {
		if m, _ := raw.(map[string]any); m["tag"] == "collapsible_panel" {
			panels = append(panels, m)
		}
	}
	if len(panels) != 1 || panels[0]["element_id"] != "panel_process" {
		t.Fatalf("panels = %#v, want single panel_process", panels)
	}
	if panels[0]["expanded"] != false {
		t.Fatalf("running process panel must stay folded: %#v", panels[0]["expanded"])
	}
	title := panels[0]["header"].(map[string]any)["title"].(map[string]string)["content"]
	if !strings.Contains(title, "工具调用（3）") {
		t.Fatalf("panel title = %q, want tool count", title)
	}
}

func TestBuildLarkCardStreamingEmptyAnswerReservesSingleNativeTarget(t *testing.T) {
	payload := BuildLarkCard(Event{Type: "stream", Streaming: true})
	answers := answerElements(payload)
	want := []map[string]any{{"tag": "markdown", "element_id": "answer", "content": ""}}
	if !reflect.DeepEqual(answers, want) {
		t.Fatalf("answer elements = %#v, want %#v", answers, want)
	}
}

func TestBuildLarkCardMarkdownLayoutIsHeaderlessAndPanelFree(t *testing.T) {
	payload := BuildLarkCard(Event{Type: "stream", Streaming: true, MarkdownLayout: true, Markdown: "answer\n\n> ✅ **Bash** · pwd"})
	if _, ok := payload["header"]; ok {
		t.Fatalf("markdown layout unexpectedly has header: %#v", payload["header"])
	}
	elements := payload["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 1 {
		t.Fatalf("markdown elements = %#v, want one", elements)
	}
	answer := elements[0].(map[string]any)
	if answer["tag"] != "markdown" || answer["element_id"] != "answer" || answer["content"] != "answer\n\n> ✅ **Bash** · pwd" {
		t.Fatalf("markdown answer = %#v", answer)
	}
	if strings.Contains(fmt.Sprint(payload), "collapsible_panel") {
		t.Fatalf("markdown layout contains panel: %#v", payload)
	}
}

func TestBuildLarkCardTerminalEmptyAnswerDoesNotCreateNativeTarget(t *testing.T) {
	for _, eventType := range []string{"result", "error", "stopped"} {
		t.Run(eventType, func(t *testing.T) {
			if answers := answerElements(BuildLarkCard(Event{Type: eventType})); len(answers) != 0 {
				t.Fatalf("answer elements = %#v, want none", answers)
			}
		})
	}
}

func TestPrepareLarkCardCapacityFallbackIsNativeIneligible(t *testing.T) {
	prepared, err := PrepareLarkCard(Event{
		Type:      "stream",
		Streaming: true,
		SessionID: strings.Repeat("session", LarkCardSoftMaxJSONBytes),
		Actions: []Action{{
			ID:    "action",
			Label: strings.Repeat("label", LarkCardSoftMaxJSONBytes),
			Value: strings.Repeat("value", LarkCardSoftMaxJSONBytes),
		}},
	})
	if err != nil {
		t.Fatalf("PrepareLarkCard() error: %v", err)
	}
	if prepared.NativeReady() {
		t.Fatal("capacity fallback is native-ready")
	}
}

func answerElements(payload map[string]any) []map[string]any {
	elements, _ := payload["body"].(map[string]any)["elements"].([]any)
	answers := make([]map[string]any, 0, 1)
	for _, raw := range elements {
		element, _ := raw.(map[string]any)
		if element["element_id"] == "answer" {
			answers = append(answers, element)
		}
	}
	return answers
}

func TestBuildLarkCardUsesValidElementIDs(t *testing.T) {
	valid := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,19}$`)
	cards := []map[string]any{
		BuildLarkCard(Event{
			Type:      "config",
			SessionID: "claude:chat:message:config-1",
			ConfigForm: &ConfigForm{
				Model: "REDACTED", Effort: "REDACTED", ReplyMode: "REDACTED",
				Models: []string{"default"}, Efforts: []string{"default"}, ReplyModes: []string{"append"},
			},
		}),
		BuildLarkCard(Event{
			Type: "stream", SessionID: "claude:chat", Streaming: true,
			Segments: []Segment{{Kind: SegmentText, Text: "answer"}, {Kind: SegmentThought, Text: "thought"}, {Kind: SegmentTool, Text: "tool"}},
			Actions:  WorkDirCreateActions("/tmp/work"), StopButton: StopButton{Visible: true},
		}),
	}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if id, ok := typed["element_id"].(string); ok && !valid.MatchString(id) {
				t.Errorf("invalid Feishu element_id %q", id)
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case []map[string]any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	for _, payload := range cards {
		walk(payload)
	}
}

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
	metaLines := map[string]string{}
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
		case "markdown":
			if id, _ := m["element_id"].(string); id == "meta_primary" || id == "meta_runtime" {
				metaLines[id], _ = m["content"].(string)
			}
		}
	}
	if foundButtons != 2 {
		t.Fatalf("button count = %d, want 2", foundButtons)
	}
	if !foundDivider {
		t.Fatal("meta divider missing")
	}
	if metaLines["meta_primary"] == "" || metaLines["meta_runtime"] == "" {
		t.Fatalf("meta lines = %#v, want primary and runtime rows", metaLines)
	}
	metaContent := metaLines["meta_primary"] + "\n" + metaLines["meta_runtime"]
	if containsAny(metaContent, "agent=", "model=", "workdir=", "status=") {
		t.Fatalf("meta content contains machine prefixes: %q", metaContent)
	}
	if !containsAll(metaContent, "REDACTED", "REDACTED", "REDACTED", "REDACTED", "REDACTED", "REDACTED") {
		t.Fatalf("meta content = %q", metaContent)
	}
	// 紧凑行用间隔点连接同一行的字段。
	if !strings.Contains(metaLines["meta_primary"], " · ") {
		t.Fatalf("primary line not compact: %q", metaLines["meta_primary"])
	}
}

func TestMetaRowsFormatsCompleteFooter(t *testing.T) {
	primary, runtime := MetaRows(Meta{
		Agent:       "claude",
		ModelInfo:   ModelInfo{Actual: "claude-opus-4-8[1m]", Effort: "default"},
		RunTokens:   21_743_200,
		TotalTokens: 29_261_500,
		User:        "developer",
		IP:          "192.0.2.10",
		WorkDir:     "/workspace/lark-agent-workspace",
	})
	if want := "🤖 Claude · 🧠 claude-opus-4-8[1m]（default） · 🔢 tokens: ▶ 21743.2k / ∑ 29261.5k"; primary != want {
		t.Fatalf("primary = %q, want %q", primary, want)
	}
	if want := "👤 developer · 🖥️ 192.0.2.10 · 📁 `/workspace/lark-agent-workspace`"; runtime != want {
		t.Fatalf("runtime = %q, want %q", runtime, want)
	}
}

func TestBuildLarkCardLabelsRequestedAndActualModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		info ModelInfo
		want []string
		not  []string
	}{
		{name: "reported actual", info: ModelInfo{Requested: "opus", Actual: "claude-opus-4-1", Effort: "high"}, want: []string{"claude-opus-4-1", "（high）"}, not: []string{"opus）"}},
		{name: "missing actual", info: ModelInfo{Requested: "sonnet", Effort: "medium"}, want: []string{"unknown", "（medium）"}, not: []string{"sonnet"}},
		{name: "default requested", info: ModelInfo{Requested: "default", Effort: "low"}, want: []string{"unknown", "（low）"}, not: []string{"default"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := BuildLarkCard(Event{Type: "result", Meta: Meta{Agent: "claude", Model: "legacy-must-not-win", ModelInfo: tc.info}})
			elements := payload["body"].(map[string]any)["elements"].([]any)
			var text string
			for _, raw := range elements {
				element := raw.(map[string]any)
				if id, _ := element["element_id"].(string); id == "meta_primary" || id == "meta_runtime" {
					text += element["content"].(string)
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
			Agent:             "claude",
			AgentHome:         "默认",
			AgentBin:          "主机 claude",
			Model:             "opus",
			Effort:            "high",
			ReplyMode:         "latest-card",
			ConversationMode:  "chat",
			GroupMessageMode:  "mention_only",
			RespondToBots:     "false",
			Agents:            []SelectOption{{Value: "claude", Label: "claude · Claude Code"}},
			AgentHomes:        []SelectOption{{Value: "默认", Label: "默认 · 宿主默认配置目录"}, {Value: "隔离", Label: "隔离 · demo home"}},
			AgentBins:         []SelectOption{{Value: "主机 claude", Label: "主机 claude · bridge 默认可执行"}, {Value: "ark4", Label: "ark4 · 豆包 seed-2-1-pro"}},
			Models:            []string{"default", "sonnet", "opus", "haiku"},
			Efforts:           []string{"default", "low", "medium", "high"},
			ReplyModes:        []string{"append", "append-clean-card", "latest-card"},
			ConversationModes: []string{"chat", "topic"},
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
	var copy strings.Builder
	for _, raw := range controls {
		control := raw.(map[string]any)
		if control["tag"] == "markdown" {
			copy.WriteString(control["content"].(string))
			copy.WriteByte('\n')
		}
		if control["tag"] == "select_static" {
			selects[control["name"].(string)] = control
		}
		if control["tag"] == "button" {
			submit = control
		}
	}
	if len(selects) != 8 || selects["model"]["initial_option"] != "opus" || selects["effort"]["initial_option"] != "high" || selects["reply_mode"]["initial_option"] != "latest-card" || selects["conversation_mode"]["initial_option"] != "chat" || selects["group_message_mode"]["initial_option"] != "mention_only" || selects["respond_to_bots"]["initial_option"] != "false" {
		t.Fatalf("select controls = %#v", selects)
	}
	if selects["agent_home"]["initial_option"] != "默认" || selects["agent_bin"]["initial_option"] != "主机 claude" {
		t.Fatalf("agent select controls = %#v", selects)
	}
	if len(selects["agent_home"]["options"].([]any)) != 2 || len(selects["agent_bin"]["options"].([]any)) != 2 {
		t.Fatalf("agent select options = home %#v bin %#v", selects["agent_home"]["options"], selects["agent_bin"]["options"])
	}
	// The ark4 bin option must submit the bare label but display the description.
	binOpts := selects["agent_bin"]["options"].([]any)
	ark4 := binOpts[1].(map[string]any)
	if ark4["value"] != "ark4" || ark4["text"].(map[string]any)["content"] != "ark4 · 豆包 seed-2-1-pro" {
		t.Fatalf("ark4 bin option value/display not separated: %#v", ark4)
	}
	if len(selects["model"]["options"].([]any)) != 4 || len(selects["effort"]["options"].([]any)) != 4 || len(selects["reply_mode"]["options"].([]any)) != 3 || len(selects["conversation_mode"]["options"].([]any)) != 2 {
		t.Fatalf("select options = model %#v effort %#v reply %#v", selects["model"]["options"], selects["effort"]["options"], selects["reply_mode"]["options"])
	}
	if submit == nil || submit["form_action_type"] != "submit" {
		t.Fatalf("submit button = %#v", submit)
	}
	if !containsAll(copy.String(), "`claude`", "/agent-mode", "Codex", "executable") {
		t.Fatalf("config guidance = %q", copy.String())
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
		{eventType: "interrupted", want: "已中断"},
	}
	for _, tt := range tests {
		payload := BuildLarkCard(Event{Type: tt.eventType, SessionID: "claude:chat", StopButton: StopButton{Visible: true, Disabled: true}})
		elements := payload["body"].(map[string]any)["elements"].([]any)
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

func TestBuildLarkCardTerminalEventsAreNonInteractive(t *testing.T) {
	for _, eventType := range []string{"result", "error", "stopped", "interrupted"} {
		t.Run(eventType, func(t *testing.T) {
			payload := BuildLarkCard(Event{
				Type:      eventType,
				SessionID: "claude:chat",
				Streaming: true,
				Actions: []Action{
					{ID: "stop", Label: "Stop"},
					{ID: "create_workdir", Label: "Create"},
					{ID: "cancel_workdir", Label: "Cancel"},
					{ID: "custom", Label: "Custom"},
				},
				StopButton: StopButton{Visible: true},
			})
			config := payload["config"].(map[string]any)
			if config["streaming_mode"] != false {
				t.Fatalf("streaming mode = %#v, want false", config["streaming_mode"])
			}
			if _, ok := config["streaming_config"]; ok {
				t.Fatalf("terminal streaming config = %#v", config["streaming_config"])
			}
			assertDisabledButtons(t, payload)
			if eventType == "interrupted" {
				header := payload["header"].(map[string]any)
				if header["template"] != "orange" || header["title"].(map[string]any)["content"] != "服务重启，任务已中断" {
					t.Fatalf("interrupted header = %#v", header)
				}
			}
		})
	}

	stream := BuildLarkCard(Event{Type: "stream", SessionID: "claude:chat", Actions: []Action{{ID: "custom", Label: "Custom"}}, StopButton: StopButton{Visible: true}})
	buttons := collectButtons(stream)
	if len(buttons) != 2 {
		t.Fatalf("stream buttons = %#v", buttons)
	}
	for _, button := range buttons {
		if _, ok := button["behaviors"]; !ok {
			t.Fatalf("live button lost behavior: %#v", button)
		}
	}
}

func TestBuildLarkCardInterruptedWithoutPanelsKeepsPlainBodyText(t *testing.T) {
	payload := BuildLarkCard(Event{Type: "interrupted", HideAgentPanels: true, Segments: []Segment{{Kind: SegmentText, Text: "服务重启，已中断，请重新发送"}}})
	for _, element := range payload["body"].(map[string]any)["elements"].([]any) {
		if markdown, ok := element.(map[string]any); ok && markdown["element_id"] == "answer" {
			if markdown["content"] != "服务重启，已中断，请重新发送" {
				t.Fatalf("interrupted body = %#v", markdown["content"])
			}
			return
		}
	}
	t.Fatalf("interrupted answer missing: %#v", payload)
}

func collectButtons(value any) []map[string]any {
	var buttons []map[string]any
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if typed["tag"] == "button" {
				buttons = append(buttons, typed)
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return buttons
}

func assertDisabledButtons(t *testing.T, payload map[string]any) {
	t.Helper()
	for _, button := range collectButtons(payload) {
		if button["disabled"] != true {
			t.Fatalf("terminal button enabled: %#v", button)
		}
		if _, ok := button["behaviors"]; ok {
			t.Fatalf("terminal button has behavior: %#v", button)
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
	// v2:思考与工具合并进单个 panel_process,内部仍分 thought / tools 两块。
	if len(panels) != 1 {
		t.Fatalf("panels = %#v, want single process panel", panels)
	}
	if panels[0]["element_id"] != "panel_process" {
		t.Fatalf("panel id = %#v, want panel_process", panels[0]["element_id"])
	}
	if panels[0]["expanded"] != false {
		t.Fatalf("process panel expanded = %#v, want false", panels[0]["expanded"])
	}
	processElements := panels[0]["elements"].([]map[string]any)
	byID := map[string]string{}
	for _, el := range processElements {
		byID[el["element_id"].(string)] = el["content"].(string)
	}
	if byID["thought"] != "inspect plan" || byID["tools"] != "Bash(ls)" {
		t.Fatalf("process panel body = %#v", byID)
	}
}

func TestBuildLarkCardOrderedTimeline(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:          "result",
		OrderedLayout: true,
		Segments: []Segment{
			{Kind: SegmentThought, Text: "内部思考"},
			{Kind: SegmentText, Text: "先检查"},
			{Kind: SegmentTool, Text: "Bash(ls)"},
			{Kind: SegmentText, Text: "最终答案"},
		},
	})
	elements := payload["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 4 {
		t.Fatalf("ordered elements = %#v, want thought + 3 timeline blocks", elements)
	}
	thought := elements[0].(map[string]any)
	first := elements[1].(map[string]any)
	tool := elements[2].(map[string]any)
	last := elements[3].(map[string]any)
	if thought["tag"] != "collapsible_panel" || thought["element_id"] != "panel_thought" {
		t.Fatalf("ordered thought element = %#v", thought)
	}
	if first["tag"] != "markdown" || first["content"] != "先检查" {
		t.Fatalf("first ordered element = %#v", first)
	}
	if tool["tag"] != "collapsible_panel" || tool["expanded"] != false {
		t.Fatalf("ordered tool element = %#v", tool)
	}
	toolElements := tool["elements"].([]map[string]any)
	if len(toolElements) != 1 || toolElements[0]["content"] != "Bash(ls)" {
		t.Fatalf("ordered tool body = %#v", toolElements)
	}
	if last["tag"] != "markdown" || last["content"] != "最终答案" {
		t.Fatalf("last ordered element = %#v", last)
	}
}

func TestBuildLarkCardKeepsLegacyAggregateLayout(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type: "result",
		Segments: []Segment{
			{Kind: SegmentText, Text: "先检查"},
			{Kind: SegmentTool, Text: "Bash(ls)"},
			{Kind: SegmentText, Text: "最终答案"},
		},
	})
	elements := payload["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("legacy elements = %#v, want answer + process panel", elements)
	}
	answer := elements[0].(map[string]any)
	process := elements[1].(map[string]any)
	if answer["tag"] != "markdown" || answer["content"] != "先检查\n\n最终答案" {
		t.Fatalf("legacy answer = %#v", answer)
	}
	if process["tag"] != "collapsible_panel" || process["element_id"] != "panel_process" {
		t.Fatalf("legacy process panel = %#v", process)
	}
}
