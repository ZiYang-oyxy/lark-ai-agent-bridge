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

func TestBuildLarkCardInlineTimelineKeepsFullShellAndPlainTools(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:                 "stream",
		Streaming:            true,
		InlineTimelineLayout: true,
		Markdown:             "回复\n\n> ⏳ **Bash** · git status",
		Segments: []Segment{
			{Kind: SegmentThought, Text: "reasoning"},
			{Kind: SegmentTool, Text: "must not render as panel"},
		},
		HeaderTitle:    "正在执行工具 · ⏱ 8s",
		HeaderTemplate: "blue",
		StopButton:     StopButton{Visible: true},
		Meta: Meta{
			Agent: "claude", Model: "model", RunTokens: 10, TotalTokens: 20,
			User: "user", IP: "host", WorkDir: "/work",
		},
	})
	if _, ok := payload["header"]; !ok {
		t.Fatalf("inline timeline missing header: %#v", payload)
	}
	elements := payload["body"].(map[string]any)["elements"].([]any)
	answers := answerElements(payload)
	if len(answers) != 1 || answers[0]["content"] != "回复\n\n> ⏳ **Bash** · git status" {
		t.Fatalf("answer elements = %#v", answers)
	}
	var panels []map[string]any
	for _, raw := range elements {
		element, _ := raw.(map[string]any)
		if element["tag"] == "collapsible_panel" {
			panels = append(panels, element)
		}
	}
	if len(panels) != 1 || panels[0]["element_id"] != "panel_thought" || panels[0]["expanded"] != false {
		t.Fatalf("panels = %#v, want one folded thought panel", panels)
	}
	serialized := fmt.Sprint(payload)
	for _, want := range []string{"停止", "Claude", "model", "tokens:", "user", "host", "/work"} {
		if !strings.Contains(serialized, want) {
			t.Fatalf("full shell missing %q: %s", want, serialized)
		}
	}
	if strings.Contains(serialized, "must not render as panel") {
		t.Fatalf("tool body rendered outside inline Markdown: %s", serialized)
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
	formElements := form["elements"].([]any)
	actionRow := formElements[len(formElements)-1].(map[string]any)
	if actionRow["tag"] != "column_set" {
		t.Fatalf("config action row = %#v, want column_set", actionRow)
	}
	columns := actionRow["columns"].([]any)
	if len(columns) != 2 {
		t.Fatalf("config action columns = %d, want 2", len(columns))
	}
	for i, raw := range columns {
		column := raw.(map[string]any)
		if column["width"] != "weighted" || column["weight"] != 1 {
			t.Fatalf("config action column %d = %#v", i, column)
		}
		button := column["elements"].([]any)[0].(map[string]any)
		if button["width"] != "fill" {
			t.Fatalf("config action button %d width = %#v", i, button["width"])
		}
	}
	selects := map[string]map[string]any{}
	buttons := map[string]map[string]any{}
	var copy strings.Builder
	collectConfigControls(formElements, selects, buttons, &copy)
	submit := buttons["submit_runtime_config"]
	closeButton := buttons["close_runtime_config"]
	// Model/Effort dropped from the UI (Task 3); six selects remain.
	if _, ok := selects["model"]; ok {
		t.Fatalf("model select must be removed from config form: %#v", selects)
	}
	if _, ok := selects["effort"]; ok {
		t.Fatalf("effort select must be removed from config form: %#v", selects)
	}
	if len(selects) != 6 || selects["reply_mode"]["initial_option"] != "latest-card" || selects["conversation_mode"]["initial_option"] != "chat" || selects["group_message_mode"]["initial_option"] != "mention_only" || selects["respond_to_bots"]["initial_option"] != "false" {
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
	if len(selects["reply_mode"]["options"].([]any)) != 3 || len(selects["conversation_mode"]["options"].([]any)) != 2 {
		t.Fatalf("select options = reply %#v conversation %#v", selects["reply_mode"]["options"], selects["conversation_mode"]["options"])
	}
	if submit == nil || submit["form_action_type"] != "submit" {
		t.Fatalf("submit button = %#v", submit)
	}
	if closeButton == nil || closeButton["type"] != "default" || closeButton["form_action_type"] != nil {
		t.Fatalf("close button = %#v", closeButton)
	}
	if text := closeButton["text"].(map[string]any)["content"]; text != "关闭" {
		t.Fatalf("close button text = %#v, want 关闭", text)
	}
	formBlob, err := json.Marshal(form)
	if err != nil {
		t.Fatal(err)
	}
	formText := string(formBlob)
	// The section titles (living in collapsible_panel headers) must anchor the
	// four visual regions.
	if !containsAll(formText, "运行参数", "会话行为", "群消息", "访问控制") {
		t.Fatalf("config sections missing = %s", formText)
	}
	// Model / Effort / Agent-mode copy must be gone from both markdown copy and
	// the serialized form.
	guidance := copy.String()
	if containsAny(guidance, "**Model**", "**Effort**", "/agent-mode") ||
		containsAny(formText, "config_model_label", "config_effort_label", "cfg_agent_mode", `"name":"model"`, `"name":"effort"`) {
		t.Fatalf("config form still references dropped fields; guidance=%q form=%s", guidance, formText)
	}
	behaviors := submit["behaviors"].([]any)
	value := behaviors[0].(map[string]any)["value"].(map[string]any)
	if value["action_id"] != "config.save" || value["session"] != "claude:chat:message:config-1" {
		t.Fatalf("submit callback = %#v", value)
	}
	closeBehaviors := closeButton["behaviors"].([]any)
	closeValue := closeBehaviors[0].(map[string]any)["value"].(map[string]any)
	if closeValue["action_id"] != "config.close" || closeValue["session"] != "claude:chat:message:config-1" {
		t.Fatalf("close callback = %#v", closeValue)
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
	if _, ok := button["confirm"]; ok {
		t.Fatalf("disabled stop button should not include confirm: %#v", button["confirm"])
	}
}

func TestBuildLarkCardStopButtonRequiresConfirmation(t *testing.T) {
	card := BuildLarkCard(Event{Type: "stream", SessionID: "claude:chat", StopButton: StopButton{Visible: true}})
	elements := card["body"].(map[string]any)["elements"].([]any)
	button := elements[0].(map[string]any)
	confirm := button["confirm"].(map[string]any)
	title := confirm["title"].(map[string]any)
	text := confirm["text"].(map[string]any)
	if title["tag"] != "plain_text" || title["content"] != "确认停止任务？" {
		t.Fatalf("confirm title = %#v", title)
	}
	if text["tag"] != "plain_text" || text["content"] != "停止后，本轮任务将立即结束，当前已生成的内容会保留。" {
		t.Fatalf("confirm text = %#v", text)
	}
	behaviors := button["behaviors"].([]any)
	value := behaviors[0].(map[string]any)["value"].(map[string]any)
	if value["action_id"] != "stop" || value["session"] != "claude:chat" {
		t.Fatalf("stop callback = %#v", value)
	}
}

func TestBuildLarkCardSupportsGenericActionConfirmation(t *testing.T) {
	payload := BuildLarkCard(Event{SessionID: "update", Actions: []Action{{
		ID: "update.install", Label: "立即升级", Value: "1.2.3",
		Confirm: &ActionConfirm{Title: "确认升级？", Text: "Bridge 将短暂重启。"},
	}}})
	buttons := collectButtons(payload)
	if len(buttons) != 1 {
		t.Fatalf("buttons = %#v", buttons)
	}
	confirm := buttons[0]["confirm"].(map[string]any)
	if confirm["title"].(map[string]any)["content"] != "确认升级？" || confirm["text"].(map[string]any)["content"] != "Bridge 将短暂重启。" {
		t.Fatalf("confirm = %#v", confirm)
	}
}

func TestBuildLarkCardOmitsGenericConfirmationWhenDisabled(t *testing.T) {
	payload := BuildLarkCard(Event{SessionID: "update", Actions: []Action{{
		ID: "update.install", Label: "立即升级", Disabled: true,
		Confirm: &ActionConfirm{Title: "确认升级？", Text: "Bridge 将短暂重启。"},
	}}})
	button := collectButtons(payload)[0]
	if _, ok := button["confirm"]; ok {
		t.Fatalf("disabled button has confirm: %#v", button)
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

func TestBuildLarkCardRendersHelpCard(t *testing.T) {
	help := &HelpCard{
		Groups: []HelpGroup{
			{Title: "💬 会话", Lines: []string{"**`/new`** 开新会话", "**`/status`** 当前会话状态"}},
			{Title: "⚙️ 配置", Lines: []string{"**`/config`** 全局运行偏好"}},
			{Title: "⏰ 定时", Lines: []string{"**`/cron`** 周期任务", "**`/timer`** 一次性任务"}},
			{Title: "🔒 权限", Lines: []string{"**`/invite`** user|admin @人"}},
		},
		Footer: "直接发文字 = 继续当前会话 · 群里默认需 @bot",
		ChatID: "oc-a",
	}
	payload := BuildLarkCard(Event{
		Type:      "help",
		SessionID: "help:msg",
		HelpCard:  help,
	})

	header := payload["header"].(map[string]any)
	if header["template"] != "blue" {
		t.Fatalf("help template = %#v, want blue", header["template"])
	}
	if title := header["title"].(map[string]any)["content"]; title != "💡 命令帮助" {
		t.Fatalf("help title = %#v, want 💡 命令帮助", title)
	}

	if payload["config"].(map[string]any)["streaming_mode"] != false {
		t.Fatalf("help card must be non-streaming: %#v", payload["config"])
	}

	elements := payload["body"].(map[string]any)["elements"].([]any)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"💬 会话", "⚙️ 配置", "⏰ 定时", "🔒 权限", "直接发文字 = 继续当前会话"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help card missing %q: %s", want, text)
		}
	}

	// Section containers: one collapsible_panel per group.
	var sections int
	for _, raw := range elements {
		if m, _ := raw.(map[string]any); m["tag"] == "collapsible_panel" {
			sections++
		}
	}
	if sections != 4 {
		t.Fatalf("help sections = %d, want 4", sections)
	}

	// Exactly one hr divider before the footer note.
	var hrs int
	for _, raw := range elements {
		if m, _ := raw.(map[string]any); m["tag"] == "hr" {
			hrs++
		}
	}
	if hrs != 1 {
		t.Fatalf("help hr count = %d, want 1", hrs)
	}

	var buttons []map[string]any
	collectHelpButtonData(elements, &buttons)
	var buttonIDs, buttonLabels []string
	for _, button := range buttons {
		buttonLabels = append(buttonLabels, button["text"].(map[string]any)["content"].(string))
		behavior := button["behaviors"].([]any)[0].(map[string]any)
		value := behavior["value"].(map[string]any)
		buttonIDs = append(buttonIDs, value["action_id"].(string))
	}
	if want := []string{"help.status", "help.open_config", "help.open_local_config"}; !reflect.DeepEqual(buttonIDs, want) {
		t.Fatalf("help buttons = %#v, want %#v", buttonIDs, want)
	}
	if want := []string{"📊 状态", "⚙️ 全局配置", "🏘️ 本群配置"}; !reflect.DeepEqual(buttonLabels, want) {
		t.Fatalf("help button labels = %#v, want %#v", buttonLabels, want)
	}
	localValue := buttons[2]["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
	if localValue["value"] != "oc-a" {
		t.Fatalf("local config callback value = %#v, want oc-a", localValue)
	}
}

func TestBuildLarkCardOmitsLocalConfigFromDirectMessageHelp(t *testing.T) {
	payload := BuildLarkCard(Event{Type: "help", SessionID: "help:dm", HelpCard: &HelpCard{}})
	var buttons []map[string]any
	collectHelpButtonData(payload["body"].(map[string]any)["elements"].([]any), &buttons)
	var buttonIDs []string
	for _, button := range buttons {
		value := button["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
		buttonIDs = append(buttonIDs, value["action_id"].(string))
	}
	if want := []string{"help.status", "help.open_config"}; !reflect.DeepEqual(buttonIDs, want) {
		t.Fatalf("direct-message help buttons = %#v, want %#v", buttonIDs, want)
	}
}

func TestBuildLarkCardRendersUpdateStatusWithSectionedHelp(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:      "help",
		SessionID: "help:update",
		HelpCard: &HelpCard{Groups: []HelpGroup{
			{Title: "💬 会话", Lines: []string{"**`/new`** 开新会话"}},
		}},
		Segments: []Segment{{Kind: SegmentText, Text: "当前版本：v1.0.0\n发现新版本 v1.2.0"}},
		Actions:  []Action{{ID: "update.details", Label: "查看更新", Value: "1.2.0"}},
	})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !containsAll(text, "当前版本：v1.0.0", "发现新版本 v1.2.0", "update.details", "**`/new`**") {
		t.Fatalf("sectioned update help missing content: %s", text)
	}
}

func collectHelpButtons(elements []any, out *[]string) {
	var buttons []map[string]any
	collectHelpButtonData(elements, &buttons)
	for _, button := range buttons {
		for _, raw := range button["behaviors"].([]any) {
			behavior := raw.(map[string]any)
			if behavior["type"] != "callback" {
				continue
			}
			value := behavior["value"].(map[string]any)
			if id, _ := value["action_id"].(string); id != "" {
				*out = append(*out, id)
			}
		}
	}
}

func collectHelpButtonData(elements []any, out *[]map[string]any) {
	for _, raw := range elements {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["tag"] == "button" {
			*out = append(*out, m)
		}
		if nested, ok := m["elements"].([]any); ok {
			collectHelpButtonData(nested, out)
		}
		if columns, ok := m["columns"].([]any); ok {
			collectHelpButtonData(columns, out)
		}
	}
}

// collectConfigControls walks the (now sectioned) config form tree, gathering
// select_static / button controls by name and accumulating markdown copy so the
// test does not depend on how deeply fields are nested inside sections.
func collectConfigControls(elements []any, selects, buttons map[string]map[string]any, copy *strings.Builder) {
	for _, raw := range elements {
		control, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch control["tag"] {
		case "markdown":
			if content, ok := control["content"].(string); ok {
				copy.WriteString(content)
				copy.WriteByte('\n')
			}
		case "select_static":
			if name, ok := control["name"].(string); ok {
				selects[name] = control
			}
		case "button":
			if name, ok := control["name"].(string); ok {
				buttons[name] = control
			}
		}
		if nested, ok := control["elements"].([]any); ok {
			collectConfigControls(nested, selects, buttons, copy)
		}
		if nested, ok := control["elements"].([]map[string]any); ok {
			generic := make([]any, len(nested))
			for i, n := range nested {
				generic[i] = n
			}
			collectConfigControls(generic, selects, buttons, copy)
		}
		if columns, ok := control["columns"].([]any); ok {
			collectConfigControls(columns, selects, buttons, copy)
		}
	}
}

func TestBuildConfigFormElementsGroupsFourSectionsAndDropsModelEffort(t *testing.T) {
	elements := buildConfigFormElements("claude:chat:message:cfg", ConfigForm{
		Agent: "claude", AgentHome: "默认", AgentBin: "主机 claude",
		ReplyMode: "append", ConversationMode: "chat",
		GroupMessageMode: "mention_only", RespondToBots: "false",
		AgentHomes:        []SelectOption{{Value: "默认", Label: "默认"}},
		AgentBins:         []SelectOption{{Value: "主机 claude", Label: "主机 claude"}},
		ReplyModes:        []string{"append", "append-clean-card", "latest-card"},
		ConversationModes: []string{"chat", "topic"},
	})
	blob, err := json.Marshal(elements)
	if err != nil {
		t.Fatal(err)
	}
	text := string(blob)
	for _, want := range []string{"运行参数", "会话行为", "群消息", "collapsible_panel"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config form missing section marker %q: %s", want, text)
		}
	}
	for _, banned := range []string{"config_model_label", "config_effort_label", "cfg_agent_mode", "/agent-mode"} {
		if strings.Contains(text, banned) {
			t.Fatalf("config form still contains dropped element %q: %s", banned, text)
		}
	}
	// intro copy must state global default + per-group override hint.
	if !strings.Contains(text, "全局") || !strings.Contains(text, "/local-config") {
		t.Fatalf("config intro copy missing global/override hint: %s", text)
	}
}

func TestBuildConfigFormElementsKeepsLocalActionsInEqualWidthRow(t *testing.T) {
	elements := buildConfigFormElements("local-config-card", ConfigForm{
		ChatID: "oc-a", Agent: "claude", AgentHome: "默认", AgentBin: "主机 claude",
		ReplyMode: "append", ConversationMode: "chat",
		GroupMessageMode: "mention_only", RespondToBots: "false",
	})
	form := elements[1].(map[string]any)
	formElements := form["elements"].([]any)
	row := formElements[len(formElements)-1].(map[string]any)
	if row["tag"] != "column_set" {
		t.Fatalf("local config action row = %#v, want column_set", row)
	}
	columns := row["columns"].([]any)
	if len(columns) != 2 {
		t.Fatalf("local config action columns = %d, want 2", len(columns))
	}
	for i, raw := range columns {
		column := raw.(map[string]any)
		if column["width"] != "weighted" || column["weight"] != 1 {
			t.Fatalf("local config action column %d = %#v", i, column)
		}
	}

	selects := map[string]map[string]any{}
	buttons := map[string]map[string]any{}
	var copy strings.Builder
	collectConfigControls(formElements, selects, buttons, &copy)
	save := buttons["submit_runtime_config"]
	if save == nil || save["form_action_type"] != "submit" || save["width"] != "fill" {
		t.Fatalf("local config save button = %#v", save)
	}
	saveValue := save["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
	if saveValue["action_id"] != "local_config.save" || saveValue["value"] != "oc-a" {
		t.Fatalf("local config save callback = %#v", saveValue)
	}
	closeButton := buttons["close_runtime_config"]
	closeValue := closeButton["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
	if closeButton["width"] != "fill" || closeValue["action_id"] != "config.close" {
		t.Fatalf("local config close button = %#v callback=%#v", closeButton, closeValue)
	}
}

func TestConfigEventHeaderIsGreyAndGlobal(t *testing.T) {
	if got := templateForEvent("config"); got != "grey" {
		t.Fatalf("config template = %q, want grey", got)
	}
	if got := titleForEvent("config"); !strings.Contains(got, "全局") {
		t.Fatalf("config title = %q, want it to mention 全局", got)
	}
}

func TestBuildLarkCardRendersLocalConfigOverview(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:      "local_config_overview",
		SessionID: "local-config:msg",
		LocalConfigOverview: &LocalConfigOverview{
			ChatID: "oc-42",
			Items: []LocalConfigItem{
				{Label: "Reply mode", Value: "latest-card", Overridden: true},
				{Label: "Conversation mode", Value: "topic", Overridden: true},
				{Label: "群消息接收", Value: "仅响应 @bot", Overridden: false},
				{Label: "响应其他 bot", Value: "忽略", Overridden: false},
				{Label: "Agent bin", Value: "主机", Overridden: false},
			},
			OverrideCount: 2,
		},
	})

	header := payload["header"].(map[string]any)
	if header["template"] != "turquoise" {
		t.Fatalf("local config overview template = %#v, want turquoise", header["template"])
	}
	if title := header["title"].(map[string]any)["content"]; title != "🏠 本群运行偏好覆盖" {
		t.Fatalf("local config overview title = %#v, want 🏠 本群运行偏好覆盖", title)
	}

	if payload["config"].(map[string]any)["streaming_mode"] != false {
		t.Fatalf("local config overview must be non-streaming: %#v", payload["config"])
	}

	elements := payload["body"].(map[string]any)["elements"].([]any)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"本群覆盖了 2 项", "Reply mode", "（本群）", "（继承）", "继承全局"} {
		if !strings.Contains(text, want) {
			t.Fatalf("local config overview missing %q: %s", want, text)
		}
	}

	// Exactly three local config overview buttons in a single button row.
	var buttonIDs []string
	collectHelpButtons(elements, &buttonIDs)
	want := []string{"local_config.edit", "local_config.reset", "config.close"}
	if !reflect.DeepEqual(buttonIDs, want) {
		t.Fatalf("local config overview buttons = %#v, want %#v", buttonIDs, want)
	}
}

// collectCallbackIDs walks a fully-marshalled card payload (so both []any and
// []map[string]any element slices normalise to []any) and returns every button
// callback action_id in document order.
func collectCallbackIDs(t *testing.T, payload map[string]any) []string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var normalised map[string]any
	if err := json.Unmarshal(data, &normalised); err != nil {
		t.Fatal(err)
	}
	elements := normalised["body"].(map[string]any)["elements"].([]any)
	var buttons []map[string]any
	collectHelpButtonData(elements, &buttons)
	var ids []string
	for _, button := range buttons {
		behaviors, ok := button["behaviors"].([]any)
		if !ok {
			// A disabled button (e.g. the current session's 恢复) carries no
			// callback behaviors; skip it rather than panic.
			continue
		}
		for _, raw := range behaviors {
			behavior, _ := raw.(map[string]any)
			if behavior["type"] != "callback" {
				continue
			}
			if value, _ := behavior["value"].(map[string]any); value != nil {
				if id, _ := value["action_id"].(string); id != "" {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

func TestBuildLarkCardRendersStatusCard(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:      "status",
		SessionID: "status:msg",
		StatusCard: &StatusCard{
			Sections: []StatusSection{
				{Title: "📋 会话概览", Fields: []StatusField{
					{Label: "运行模式", Value: "claude_oneshot", Code: true},
					{Label: "会话隔离", Value: "按群共用（chat）"},
				}},
				{Title: "🧠 运行时", Fields: []StatusField{
					{Label: "状态", Value: "ready", Code: true},
					{Label: "工作目录", Value: "/tmp/work", Code: true},
				}},
			},
		},
	})

	header := payload["header"].(map[string]any)
	if header["template"] != "indigo" {
		t.Fatalf("status template = %#v, want indigo", header["template"])
	}
	if title := header["title"].(map[string]any)["content"]; title != "📊 会话状态" {
		t.Fatalf("status title = %#v, want 📊 会话状态", title)
	}
	if payload["config"].(map[string]any)["streaming_mode"] != false {
		t.Fatalf("status card must be non-streaming: %#v", payload["config"])
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"📋 会话概览", "🧠 运行时", "运行模式", "`claude_oneshot`", "按群共用（chat）", "`/tmp/work`"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status card missing %q: %s", want, text)
		}
	}

	if ids := collectCallbackIDs(t, payload); !reflect.DeepEqual(ids, []string{"status.refresh", "help.open_config"}) {
		t.Fatalf("status buttons = %#v, want [status.refresh help.open_config]", ids)
	}
}

func TestBuildLarkCardStatusNotStartedShowsHint(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:       "status",
		SessionID:  "status:msg",
		StatusCard: &StatusCard{Sections: []StatusSection{{Title: "📋 会话概览", Fields: []StatusField{{Label: "Agent", Value: "claude", Code: true}}}}, NotStarted: true},
	})
	data, _ := json.Marshal(payload)
	if !strings.Contains(string(data), "本会话尚未开始运行") {
		t.Fatalf("not-started status card missing hint: %s", data)
	}
}

func TestBuildLarkCardRendersResumeCard(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:      "resume",
		SessionID: "resume:msg",
		ResumeCard: &ResumeCard{
			Agent:   "claude",
			WorkDir: "/tmp/work",
			Items: []ResumeItem{
				{Index: 1, SessionID: "s-current", UpdatedAt: "2026-07-21 10:00:00", Summary: "最新一轮", Current: true},
				{Index: 2, SessionID: "s-older", UpdatedAt: "2026-07-20 09:00:00", Summary: "上一轮"},
			},
		},
	})

	header := payload["header"].(map[string]any)
	if header["template"] != "wathet" {
		t.Fatalf("resume template = %#v, want wathet", header["template"])
	}
	if title := header["title"].(map[string]any)["content"]; title != "🕘 恢复历史会话" {
		t.Fatalf("resume title = %#v, want 🕘 恢复历史会话", title)
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"`s-current`", "`s-older`", "当前", "最新一轮", "工作目录 `/tmp/work`"} {
		if !strings.Contains(text, want) {
			t.Fatalf("resume card missing %q: %s", want, text)
		}
	}

	// Both rows carry a resume.select button; the current session's button is
	// disabled but the callback id is still present.
	ids := collectCallbackIDs(t, payload)
	var selects int
	for _, id := range ids {
		if id == "resume.select" {
			selects++
		}
	}
	if selects != 1 {
		t.Fatalf("resume.select callbacks = %d, want 1 (current row's button is disabled and carries no callback)", selects)
	}
}

func TestBuildLarkCardResumeEmptyShowsHint(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:       "resume",
		SessionID:  "resume:msg",
		ResumeCard: &ResumeCard{Agent: "claude", WorkDir: "/tmp/work"},
	})
	data, _ := json.Marshal(payload)
	if !strings.Contains(string(data), "没有可恢复的历史会话") {
		t.Fatalf("empty resume card missing hint: %s", data)
	}
}

func TestMetaTokenTextContextUsage(t *testing.T) {
	tests := []struct {
		name string
		meta Meta
		want string
	}{
		{"green-with-window", Meta{CtxUsedPercent: 42, CtxTokens: 84000, CtxWindow: 200000}, "🟢 ctx: 42% (84k/200k)"},
		{"yellow-boundary-60", Meta{CtxUsedPercent: 60, CtxTokens: 120000, CtxWindow: 200000}, "🟡 ctx: 60% (120k/200k)"},
		{"yellow-boundary-84", Meta{CtxUsedPercent: 84, CtxTokens: 168000, CtxWindow: 200000}, "🟡 ctx: 84% (168k/200k)"},
		{"red-boundary-85", Meta{CtxUsedPercent: 85, CtxTokens: 170000, CtxWindow: 200000}, "🔴 ctx: 85% (170k/200k)"},
		{"green-boundary-59", Meta{CtxUsedPercent: 59, CtxTokens: 118000, CtxWindow: 200000}, "🟢 ctx: 59% (118k/200k)"},
		{"percent-only-no-window", Meta{CtxUsedPercent: 42}, "🟢 ctx: 42%"},
		{"fallback-to-cumulative", Meta{RunTokens: 1200, TotalTokens: 5000}, "🔢 tokens: ▶ 1.2k / ∑ 5k"},
		{"fallback-run-only", Meta{RunTokens: 800}, "🔢 tokens: ▶ 800"},
		{"empty", Meta{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metaTokenText(tt.meta); got != tt.want {
				t.Fatalf("metaTokenText(%+v) = %q, want %q", tt.meta, got, tt.want)
			}
		})
	}
}
