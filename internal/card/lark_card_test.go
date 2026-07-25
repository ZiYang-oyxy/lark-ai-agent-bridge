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
	if !strings.Contains(serialized, "停止") {
		t.Fatalf("full shell missing stop button: %s", serialized)
	}
	// meta 行已整体停用:agent/模型/tokens/user/host/workdir 都不应再进卡片。
	for _, hidden := range []string{"🍊", "tokens:", "/work", "📁", "🖥️"} {
		if strings.Contains(serialized, hidden) {
			t.Fatalf("meta fragment %q still rendered: %s", hidden, serialized)
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

func TestBuildLarkCardIncludesActionsAndHidesMeta(t *testing.T) {
	card := BuildLarkCard(Event{
		Type:      "workdir_confirm",
		SessionID: "claude:chat",
		Segments:  []Segment{{Kind: SegmentText, Text: "Workdir does not exist: /tmp/work"}},
		Actions:   WorkDirCreateActions("/tmp/work"),
		Meta:      Meta{Agent: "claude", Model: "opus", RunTokens: 42, TotalTokens: 4200, User: "dev", IP: "192.0.2.1", WorkDir: "/tmp/work", Status: "running"},
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
			// meta 两行已整体停用,卡片里不应再出现任何运行信息行。
			if id, _ := m["element_id"].(string); id == "meta_primary" || id == "meta_runtime" {
				t.Fatalf("meta line %q still rendered, want hidden", id)
			}
		}
	}
	if foundButtons != 2 {
		t.Fatalf("button count = %d, want 2", foundButtons)
	}
}

// meta 三行(agent/会话ID/模型/tokens · user/ip/workdir · 版本/最新/开发者模式)
// 由三个独立开关 ShowMetaRow{Agent,Runtime,Developer} 控制:全 false 时返回空切片
// 与旧的"总开关关闭"视觉一致;分别打开只渲染对应行。
func TestMetaRowsGatedByIndependentToggles(t *testing.T) {
	base := Meta{
		Agent:          "claude",
		SessionID:      "a1b2c3d4-e5f6",
		ModelInfo:      ModelInfo{Actual: "claude-opus-4-8[1m]", Effort: "default"},
		RunTokens:      21_743_200,
		TotalTokens:    29_261_500,
		CtxOK:          true,
		CtxUsedPercent: 42,
		User:           "developer",
		IP:             "192.0.2.10",
		WorkDir:        "/workspace/lark-agent-workspace",
		Version:        "v0.1.9",
		LatestVersion:  "v0.1.10",
		DeveloperMode:  true,
	}

	// 默认隐藏:切片为空,buildMetaElements 会连分隔线一起跳过。
	if rows := MetaRows(base); len(rows) != 0 {
		t.Fatalf("hidden MetaRows = %+v, want empty", rows)
	}

	// 单独打开 agent 行:只返回一行,element_id 为 meta_primary。
	onAgent := base
	onAgent.ShowMetaRowAgent = true
	rows := MetaRows(onAgent)
	if len(rows) != 1 || rows[0].ElementID != "meta_primary" {
		t.Fatalf("agent-only MetaRows = %+v, want single meta_primary", rows)
	}
	for _, want := range []string{"🍊", "ctx: 42%", "claude-opus-4-8[1m]"} {
		if !strings.Contains(rows[0].Text, want) {
			t.Fatalf("agent row %q missing %q", rows[0].Text, want)
		}
	}

	// 单独打开 runtime 行:只返回一行,element_id 为 meta_runtime。
	onRuntime := base
	onRuntime.ShowMetaRowRuntime = true
	rows = MetaRows(onRuntime)
	if len(rows) != 1 || rows[0].ElementID != "meta_runtime" {
		t.Fatalf("runtime-only MetaRows = %+v, want single meta_runtime", rows)
	}
	for _, want := range []string{"developer", "192.0.2.10", "/workspace/lark-agent-workspace"} {
		if !strings.Contains(rows[0].Text, want) {
			t.Fatalf("runtime row %q missing %q", rows[0].Text, want)
		}
	}

	// 单独打开 developer 行:版本号前置 emoji 由 DeveloperMode 决定
	// (开=🐛 debug、关=🦋 蝴蝶),后跟 · 最新 v<latest>。base.DeveloperMode=true 应
	// 显示 🐛;且不再显式渲染"开发者模式 ✅/❌"段——emoji 已承担此意义。
	onDev := base
	onDev.ShowMetaRowDeveloper = true
	rows = MetaRows(onDev)
	if len(rows) != 1 || rows[0].ElementID != "meta_developer" {
		t.Fatalf("developer-only MetaRows = %+v, want single meta_developer", rows)
	}
	for _, want := range []string{"🐛 v0.1.9", "最新 v0.1.10"} {
		if !strings.Contains(rows[0].Text, want) {
			t.Fatalf("developer row %q missing %q", rows[0].Text, want)
		}
	}
	if strings.Contains(rows[0].Text, "🦋") {
		t.Fatalf("dev-mode-on row must not contain stable-emoji 🦋: %q", rows[0].Text)
	}
	if strings.Contains(rows[0].Text, "开发者模式") {
		t.Fatalf("developer row must not render explicit 开发者模式 label (emoji-carried): %q", rows[0].Text)
	}
	if strings.Contains(rows[0].Text, "🏷️") {
		t.Fatalf("legacy version tag 🏷️ must be gone: %q", rows[0].Text)
	}

	// 三个都打开:返回三行,顺序 agent → runtime → developer。
	onAll := base
	onAll.ShowMetaRowAgent = true
	onAll.ShowMetaRowRuntime = true
	onAll.ShowMetaRowDeveloper = true
	rows = MetaRows(onAll)
	if len(rows) != 3 {
		t.Fatalf("all-on MetaRows len = %d, want 3", len(rows))
	}
	wantOrder := []string{"meta_primary", "meta_runtime", "meta_developer"}
	for i, want := range wantOrder {
		if rows[i].ElementID != want {
			t.Fatalf("row %d id = %q, want %q", i, rows[i].ElementID, want)
		}
	}
}

// LatestVersion 为空(peek cache miss)时,开发者行只显示 emoji + 当前版本,
// 不显示"最新"段。开发者模式 false 时 emoji 用 🦋(正式版通道)。
func TestMetaRowsDeveloperFallbacks(t *testing.T) {
	meta := Meta{
		ShowMetaRowDeveloper: true,
		Version:              "v0.1.9",
		// LatestVersion 留空,模拟 update client 尚未 warm cache
		DeveloperMode: false,
	}
	rows := MetaRows(meta)
	if len(rows) != 1 || rows[0].ElementID != "meta_developer" {
		t.Fatalf("developer fallback rows = %+v, want single meta_developer", rows)
	}
	if strings.Contains(rows[0].Text, "最新") {
		t.Fatalf("empty LatestVersion should not render 最新 segment: %q", rows[0].Text)
	}
	if !strings.Contains(rows[0].Text, "🦋 v0.1.9") {
		t.Fatalf("dev mode off should render stable emoji 🦋 + version: %q", rows[0].Text)
	}
	if strings.Contains(rows[0].Text, "🐛") {
		t.Fatalf("dev mode off row must not contain debug emoji 🐛: %q", rows[0].Text)
	}
	if strings.Contains(rows[0].Text, "开发者模式") {
		t.Fatalf("no explicit 开发者模式 label — emoji carries it: %q", rows[0].Text)
	}
}

func TestBuildLarkCardRendersRuntimeConfigForm(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:      "config",
		SessionID: "claude:chat:message:config-1",
		ConfigForm: &ConfigForm{
			Agent:                "claude",
			AgentHome:            "默认",
			AgentBin:             "主机 claude",
			Model:                "opus",
			Effort:               "high",
			ReplyMode:            "latest-card",
			ConversationMode:     "chat",
			GroupMessageMode:     "mention_only",
			RespondToBots:        "false",
			NotifyOnComplete:     "false",
			ShowMetaRowAgent:     "false",
			ShowMetaRowRuntime:   "false",
			ShowMetaRowDeveloper: "false",
			Agents:               []SelectOption{{Value: "claude", Label: "claude · Claude Code"}},
			AgentHomes:           []SelectOption{{Value: "默认", Label: "默认 · 宿主默认配置目录"}, {Value: "隔离", Label: "隔离 · demo home"}},
			AgentBins:            []SelectOption{{Value: "主机 claude", Label: "主机 claude · bridge 默认可执行"}, {Value: "ark4", Label: "ark4 · 豆包 seed-2-1-pro"}},
			Models:               []string{"default", "sonnet", "opus", "haiku"},
			Efforts:              []string{"default", "low", "medium", "high"},
			ReplyModes:           []string{"append", "append-clean-card", "latest-card"},
			ConversationModes:    []string{"chat", "topic"},
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
	// Model stays dropped from the UI; effort is now editable again (per-chat
	// override supported). status bar 拆成 3 个独立开关(agent / runtime / developer)
	// 后,加上 reply/conversation/group/respond-to-bots/notify-on-complete/effort +
	// 两个 agent selects,总数从 9 个上升到 11 个。
	if _, ok := selects["model"]; ok {
		t.Fatalf("model select must stay removed from config form: %#v", selects)
	}
	if effort, ok := selects["effort"]; !ok || effort["initial_option"] != "high" {
		t.Fatalf("effort select missing or wrong initial: %#v", selects)
	}
	if opts := selects["effort"]["options"].([]any); len(opts) != 4 {
		t.Fatalf("effort select must expose 4 options (default/low/medium/high), got %#v", opts)
	}
	// statusbar 三行回到 select_static 布尔下拉(checker 不是 form 字段、勾选不生效),
	// select 总数为 8 + 3 = 11。
	if len(selects) != 11 ||
		selects["reply_mode"]["initial_option"] != "latest-card" ||
		selects["conversation_mode"]["initial_option"] != "chat" ||
		selects["group_message_mode"]["initial_option"] != "mention_only" ||
		selects["respond_to_bots"]["initial_option"] != "false" ||
		selects["notify_on_complete"]["initial_option"] != "false" {
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
	// Model / Agent-mode copy stays gone from both markdown copy and the
	// serialized form. Effort was re-added, so it is no longer forbidden here.
	guidance := copy.String()
	if containsAny(guidance, "**Model**", "/agent-mode") ||
		containsAny(formText, "config_model_label", "cfg_agent_mode", `"name":"model"`) {
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

// 📊 元信息行 section 应渲染三个 select_static 布尔下拉,name = show_meta_row_
// {agent,runtime,developer},初始值由 form 字段 "true"/"false" 决定。
//
// 曾经尝试过 CardKit `checker`(视觉是复选框),但 checker 不是 form 输入字段——
// 勾选状态不会被 form.submit 收集,用户实测"点了但保存后回旧值"。回到 select
// 版本(两选项"隐藏"/"显示")与 respond_to_bots / notify_on_complete 一致,提交
// 行为可靠。示例 mock 行放在字段 hint(而非选项 label)里,让用户直观看到开启
// 后卡片底部的样子。
func TestBuildLarkCardStatusBarSectionChecker(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:      "config",
		SessionID: "claude:chat:message:cfg-2",
		ConfigForm: &ConfigForm{
			Agent: "claude", AgentHome: "默认", AgentBin: "主机 claude",
			Model: "opus", Effort: "high",
			ReplyMode: "latest-card", ConversationMode: "chat",
			GroupMessageMode: "mention_only", RespondToBots: "false", NotifyOnComplete: "false",
			ShowMetaRowAgent: "true", ShowMetaRowRuntime: "false", ShowMetaRowDeveloper: "true",
			Agents:     []SelectOption{{Value: "claude", Label: "claude"}},
			AgentHomes: []SelectOption{{Value: "默认", Label: "默认"}},
			AgentBins:  []SelectOption{{Value: "主机 claude", Label: "主机 claude"}},
			Models:     []string{"default"}, Efforts: []string{"default"},
			ReplyModes: []string{"append"}, ConversationModes: []string{"chat"},
		},
	})
	metaSelects := map[string]map[string]any{}
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			tag, _ := n["tag"].(string)
			name, _ := n["name"].(string)
			if tag == "select_static" && strings.HasPrefix(name, "show_meta_row_") {
				metaSelects[name] = n
			}
			for _, c := range n {
				walk(c)
			}
		case []any:
			for _, c := range n {
				walk(c)
			}
		case []map[string]any:
			for _, c := range n {
				walk(c)
			}
		}
	}
	walk(payload)
	if len(metaSelects) != 3 {
		t.Fatalf("expected 3 statusbar select_static, got %d: %#v", len(metaSelects), metaSelects)
	}
	if metaSelects["show_meta_row_agent"]["initial_option"] != "true" {
		t.Fatalf("agent select initial = %#v, want true", metaSelects["show_meta_row_agent"]["initial_option"])
	}
	if metaSelects["show_meta_row_runtime"]["initial_option"] != "false" {
		t.Fatalf("runtime select initial = %#v, want false", metaSelects["show_meta_row_runtime"]["initial_option"])
	}
	if metaSelects["show_meta_row_developer"]["initial_option"] != "true" {
		t.Fatalf("developer select initial = %#v, want true", metaSelects["show_meta_row_developer"]["initial_option"])
	}
	// 每个布尔下拉必有 false/true 两个选项;value 与 form 字段值一致。
	for name, sel := range metaSelects {
		opts, ok := sel["options"].([]any)
		if !ok || len(opts) != 2 {
			t.Fatalf("%s options should have 2 entries, got %#v", name, sel["options"])
		}
		values := map[string]bool{}
		for _, o := range opts {
			m := o.(map[string]any)
			values[m["value"].(string)] = true
		}
		if !values["true"] || !values["false"] {
			t.Fatalf("%s options must cover true/false, got %#v", name, opts)
		}
		// 需求②:所有下拉都应设 width: fill,让宽度覆盖卡片,不随文字长度伸缩。
		if sel["width"] != "fill" {
			t.Fatalf("%s select width = %#v, want \"fill\"", name, sel["width"])
		}
	}
	// mock 示例文案改放在字段 hint(通过 fieldElements 生成的 markdownElement),
	// 用整卡的 markdown 文本聚合搜索,验证示例行仍随卡片渲染出来。
	var mdBuf strings.Builder
	var walkMD func(any)
	walkMD = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			if n["tag"] == "markdown" {
				if s, ok := n["content"].(string); ok {
					mdBuf.WriteString(s)
					mdBuf.WriteString("\n")
				}
			}
			for _, c := range n {
				walkMD(c)
			}
		case []any:
			for _, c := range n {
				walkMD(c)
			}
		case []map[string]any:
			for _, c := range n {
				walkMD(c)
			}
		}
	}
	walkMD(payload)
	md := mdBuf.String()
	if !strings.Contains(md, "🍊") || !strings.Contains(md, "ctx:") {
		t.Fatalf("agent hint should mock the status bar (🍊 + ctx:), got:\n%s", md)
	}
	if !strings.Contains(md, "👤") || !strings.Contains(md, "📁") {
		t.Fatalf("runtime hint should mock 👤 + 📁 line, got:\n%s", md)
	}
	if !strings.Contains(md, "🐛") || !strings.Contains(md, "🦋") {
		t.Fatalf("developer hint should mention both 🐛 rc / 🦋 stable emojis, got:\n%s", md)
	}
	if !strings.Contains(md, "最新") {
		t.Fatalf("developer hint should include ⬆️ 最新 semantics, got:\n%s", md)
	}
	// 回到 select 后不应再残留任何 statusbar 系列的 checker,并且不能重新出现
	// multi_select_static;这是把方向锁死在"3 个独立布尔下拉"的强断言。
	var walkNegative func(any)
	walkNegative = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			tag, _ := n["tag"].(string)
			name, _ := n["name"].(string)
			if tag == "checker" && strings.HasPrefix(name, "show_meta_row_") {
				t.Fatalf("statusbar section must not emit checker for %q — checker is not form-collectable", name)
			}
			if tag == "multi_select_static" && name == "meta_rows_multi" {
				t.Fatal("statusbar section must not emit multi_select_static meta_rows_multi anymore")
			}
			for _, c := range n {
				walkNegative(c)
			}
		case []any:
			for _, c := range n {
				walkNegative(c)
			}
		case []map[string]any:
			for _, c := range n {
				walkNegative(c)
			}
		}
	}
	walkNegative(payload)
}

// 需求②:config / agent-mode / help 一族卡片里所有 select_static 都必须 width=fill,
// 让下拉宽度与卡片对齐,不随所选文字长度伸缩、视觉参差不齐。锁在 configSelect /
// configSelectOptions 两个工厂里,这里做整卡 walk 断言不留漏网之鱼。
func TestBuildLarkCardConfigSelectStaticIsFullWidth(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:      "config",
		SessionID: "claude:chat:message:cfg-width",
		ConfigForm: &ConfigForm{
			Agent: "claude", AgentHome: "默认", AgentBin: "主机 claude",
			Model: "opus", Effort: "high",
			ReplyMode: "latest-card", ConversationMode: "chat",
			GroupMessageMode: "mention_only", RespondToBots: "false", NotifyOnComplete: "false",
			ShowMetaRowAgent: "true", ShowMetaRowRuntime: "true", ShowMetaRowDeveloper: "true",
			Agents:     []SelectOption{{Value: "claude", Label: "claude"}},
			AgentHomes: []SelectOption{{Value: "默认", Label: "默认"}},
			AgentBins:  []SelectOption{{Value: "主机 claude", Label: "主机 claude"}},
			Models:     []string{"default"}, Efforts: []string{"default"},
			ReplyModes: []string{"append"}, ConversationModes: []string{"chat"},
		},
	})
	seen := 0
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			if n["tag"] == "select_static" {
				seen++
				if n["width"] != "fill" {
					name, _ := n["name"].(string)
					t.Fatalf("select_static %q width = %#v, want \"fill\" (需求②:下拉必须占卡片全宽)", name, n["width"])
				}
			}
			for _, c := range n {
				walk(c)
			}
		case []any:
			for _, c := range n {
				walk(c)
			}
		case []map[string]any:
			for _, c := range n {
				walk(c)
			}
		}
	}
	walk(payload)
	if seen == 0 {
		t.Fatal("expected config card to contain select_static elements, got 0")
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
	card := BuildLarkCard(Event{Type: "stream", SessionID: "claude:chat", StopButton: StopButton{Visible: true, GrantID: "grant-stop"}})
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
	if value["action_id"] != "stop" || value["session"] != "claude:chat" || value["grant_id"] != "grant-stop" {
		t.Fatalf("stop callback = %#v", value)
	}
}

func TestBuildLarkCardGenericAndResumeActionsCarryGrantIDs(t *testing.T) {
	payload := BuildLarkCard(Event{SessionID: "s", Actions: []Action{{ID: "create_workdir", Label: "创建", Value: "/tmp/a", GrantID: "grant-workdir"}}})
	value := collectButtons(payload)[0]["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
	if value["grant_id"] != "grant-workdir" {
		t.Fatalf("generic action value = %#v", value)
	}
	resume := BuildLarkCard(Event{Type: "resume", SessionID: "resume-card", ResumeCard: &ResumeCard{Agent: "claude", Items: []ResumeItem{{Index: 1, SessionID: "target", GrantID: "grant-resume"}}}})
	resumeValue := collectButtons(resume)[0]["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
	if resumeValue["grant_id"] != "grant-resume" {
		t.Fatalf("resume action value = %#v", resumeValue)
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
			{Title: "⏰ 定时", Lines: []string{"**`/cron`** `[list [all]]` 查看周期任务", "**`/cron`** `add <自然语言任务>` 创建 · **`info|run|enable|disable|del <id>`** 管理", "**`/timer`** `[list [all]]` 查看一次性任务", "**`/timer`** `add <自然语言任务>` 创建 · **`info|del <id>`** 管理"}},
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
		}, VersionStatus: &HelpVersionStatus{
			CurrentVersion:  "v1.0.0",
			Status:          "发现新版本",
			LatestVersion:   "v1.2.0",
			UpdateAvailable: true,
			DetailsAction:   Action{ID: "update.details", Label: "查看更新 →", Value: "1.2.0"},
		}},
	})
	elements := payload["body"].(map[string]any)["elements"].([]any)
	if len(elements) < 2 {
		t.Fatalf("help elements = %#v", elements)
	}
	updatePanel := elements[0].(map[string]any)
	if updatePanel["tag"] != "collapsible_panel" {
		t.Fatalf("first help element must be update panel: %#v", updatePanel)
	}
	header := updatePanel["header"].(map[string]any)
	title := header["title"].(map[string]string)["content"]
	// 版本区间 vCurrent → vLatest 已并入 section 标题（原「✨ 发现新版本」+ 独立 markdown 行合成同一行）。
	if title != "✨ 发现新版本 v1.0.0 → v1.2.0" {
		t.Fatalf("update panel title = %q", title)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !containsAll(text, "v1.0.0 → v1.2.0", "update.details", "查看更新 →", "**`/new`**") {
		t.Fatalf("sectioned update help missing content: %s", text)
	}
	if strings.Contains(text, "查看本次更新内容，确认后可升级") {
		t.Fatalf("sectioned update help contains redundant guidance: %s", text)
	}
	var buttons []map[string]any
	collectHelpButtonData(elements, &buttons)
	updateButtons := 0
	for _, button := range buttons {
		behavior := button["behaviors"].([]any)[0].(map[string]any)
		value := behavior["value"].(map[string]any)
		if value["action_id"] == "update.details" {
			updateButtons++
			if button["type"] != "primary" || value["value"] != "1.2.0" {
				t.Fatalf("update button = %#v", button)
			}
		}
	}
	if updateButtons != 1 {
		t.Fatalf("update button count = %d, want 1", updateButtons)
	}
}

func TestBuildLarkCardRendersInactiveVersionStatusWithoutUpdateAction(t *testing.T) {
	tests := []struct {
		name    string
		current string
		status  string
	}{
		{name: "latest", current: "v1.2.0", status: "已是最新版本"},
		{name: "development", current: "dev", status: "开发构建不可自升级"},
		{name: "unsupported", current: "v1.2.0", status: "不支持当前平台"},
		{name: "check failed", current: "v1.2.0", status: "暂时无法检查更新"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := BuildLarkCard(Event{
				Type:      "help",
				SessionID: "help:" + tt.name,
				HelpCard: &HelpCard{
					VersionStatus: &HelpVersionStatus{CurrentVersion: tt.current, Status: tt.status},
					Groups:        []HelpGroup{{Title: "💬 会话", Lines: []string{"**`/new`** 开新会话"}}},
				},
			})
			elements := payload["body"].(map[string]any)["elements"].([]any)
			first := elements[0].(map[string]any)
			want := fmt.Sprintf("当前版本：`%s` · %s", tt.current, tt.status)
			if first["tag"] != "markdown" || first["content"] != want || first["text_size"] != "notation" {
				t.Fatalf("inactive version status = %#v", first)
			}
			var buttonIDs []string
			collectHelpButtons(elements, &buttonIDs)
			for _, id := range buttonIDs {
				if id == "update.details" {
					t.Fatalf("inactive version status must not render update action: %#v", buttonIDs)
				}
			}
		})
	}
}

// 需求②：/status 卡在 event.VersionStatus 存在且 UpdateAvailable 时，顶部叠加
// 与 /help 一致的「✨ 发现新版本 vX → vY」section（带 update.details 按钮）。
func TestBuildLarkCardStatusCardPrependsAvailableVersionHint(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:       "status",
		SessionID:  "status:with-update",
		StatusCard: &StatusCard{Sections: []StatusSection{{Title: "会话概览", Fields: []StatusField{{Label: "Session", Value: "s1"}}}}},
		VersionStatus: &HelpVersionStatus{
			CurrentVersion:  "v1.0.0",
			LatestVersion:   "v1.2.0",
			UpdateAvailable: true,
			DetailsAction:   Action{ID: "update.details", Label: "查看更新 →", Value: "1.2.0"},
		},
	})
	elements := payload["body"].(map[string]any)["elements"].([]any)
	if len(elements) < 2 {
		t.Fatalf("status elements = %d, want >= 2 (hint + status body)", len(elements))
	}
	hint := elements[0].(map[string]any)
	if hint["tag"] != "collapsible_panel" {
		t.Fatalf("first element must be version hint panel: %#v", hint)
	}
	title := hint["header"].(map[string]any)["title"].(map[string]string)["content"]
	if title != "✨ 发现新版本 v1.0.0 → v1.2.0" {
		t.Fatalf("hint title = %q", title)
	}
	data, _ := json.Marshal(payload)
	if !strings.Contains(string(data), "update.details") {
		t.Fatalf("status card missing update.details button: %s", data)
	}
}

// 需求②：/config 卡同样叠加升级提示。
func TestBuildLarkCardConfigFormPrependsAvailableVersionHint(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:       "config",
		SessionID:  "config:with-update",
		ConfigForm: &ConfigForm{Agent: "claude", ReplyMode: "append"},
		VersionStatus: &HelpVersionStatus{
			CurrentVersion:  "v1.0.0",
			LatestVersion:   "v1.2.0",
			UpdateAvailable: true,
			DetailsAction:   Action{ID: "update.details", Label: "查看更新 →", Value: "1.2.0"},
		},
	})
	elements := payload["body"].(map[string]any)["elements"].([]any)
	hint := elements[0].(map[string]any)
	if hint["tag"] != "collapsible_panel" {
		t.Fatalf("first element must be version hint panel: %#v", hint)
	}
	title := hint["header"].(map[string]any)["title"].(map[string]string)["content"]
	if title != "✨ 发现新版本 v1.0.0 → v1.2.0" {
		t.Fatalf("hint title = %q", title)
	}
}

// 需求②：无 UpdateAvailable 时（nil VersionStatus）不渲染任何升级提示。
func TestBuildLarkCardStatusCardSkipsHintWhenNoUpdate(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:       "status",
		SessionID:  "status:no-update",
		StatusCard: &StatusCard{Sections: []StatusSection{{Title: "会话概览", Fields: []StatusField{{Label: "Session", Value: "s1"}}}}},
	})
	data, _ := json.Marshal(payload)
	if strings.Contains(string(data), "发现新版本") {
		t.Fatalf("status card without version status must not show update hint: %s", data)
	}
}

// 需求③：/config 卡的 🤖 运行参数 / 💬 会话行为 / 👥 群消息 / 📊 元信息行 四段默认收起。
func TestBuildLarkCardConfigFormSectionsDefaultCollapsed(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:       "config",
		SessionID:  "config:collapsed",
		ConfigForm: &ConfigForm{Agent: "claude", ReplyMode: "append"},
	})
	form := findFirstForm(payload["body"].(map[string]any)["elements"].([]any))
	if form == nil {
		t.Fatal("config card must contain a form")
	}
	titles := map[string]bool{"🤖 运行参数": false, "💬 会话行为": false, "👥 群消息": false, "📊 元信息行": false}
	for _, raw := range form["elements"].([]any) {
		el, _ := raw.(map[string]any)
		if el == nil || el["tag"] != "collapsible_panel" {
			continue
		}
		title := el["header"].(map[string]any)["title"].(map[string]string)["content"]
		if _, watched := titles[title]; !watched {
			continue
		}
		titles[title] = true
		if el["expanded"] != false {
			t.Fatalf("config section %q must default to collapsed, got expanded=%v", title, el["expanded"])
		}
	}
	for title, seen := range titles {
		if !seen {
			t.Fatalf("config section %q not rendered", title)
		}
	}
}

// findFirstForm 深度找第一个 tag=form 的元素。
func findFirstForm(elements []any) map[string]any {
	for _, raw := range elements {
		el, _ := raw.(map[string]any)
		if el == nil {
			continue
		}
		if el["tag"] == "form" {
			return el
		}
		if sub, ok := el["elements"].([]any); ok {
			if f := findFirstForm(sub); f != nil {
				return f
			}
		}
	}
	return nil
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
		if nested, ok := m["elements"].([]map[string]any); ok {
			items := make([]any, len(nested))
			for i := range nested {
				items[i] = nested[i]
			}
			collectHelpButtonData(items, out)
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
				{Label: "回复模式", Value: "latest-card", Overridden: true},
				{Label: "会话模式", Value: "topic", Overridden: true},
				{Label: "群消息接收", Value: "仅响应 @bot", Overridden: false},
				{Label: "响应其他 bot", Value: "忽略", Overridden: false},
				{Label: "Agent 可执行文件", Value: "主机", Overridden: false},
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
	for _, want := range []string{"本群覆盖了 2 项", "回复模式", "（本群）", "（继承）", "继承全局"} {
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
	// Compact rows show time, summary and the 当前 marker; the session id is not
	// shown on-screen (it rides the button callback value only).
	for _, want := range []string{"2026-07-21 10:00:00", "2026-07-20 09:00:00", "当前", "最新一轮", "上一轮", "恢复"} {
		if !strings.Contains(text, want) {
			t.Fatalf("resume card missing %q: %s", want, text)
		}
	}
	// The non-current row's id rides its resume.select callback value; the
	// current row's button is disabled and carries no callback.
	if !strings.Contains(text, "s-older") {
		t.Fatalf("resume card should carry s-older on a callback value: %s", text)
	}

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

// TestBuildLarkCardThreeSectionLayout 验证 append-clean 三段布局的元素顺序、展开态与标题计数:
// 思考推理折叠区(正文前, expanded, ×N) → 正文 answer → 工具调用折叠区(正文后, folded, ×N)。
func TestBuildLarkCardThreeSectionLayout(t *testing.T) {
	payload := BuildLarkCard(Event{
		Type:               "stream",
		Streaming:          true,
		ThreeSectionLayout: true,
		ThoughtExpanded:    true,
		ToolsExpanded:      false,
		ThoughtRoundCount:  3,
		ToolRoundCount:     6,
		Segments: []Segment{
			{Kind: SegmentText, Text: "正文答复"},
			{Kind: SegmentThought, Text: "最新一轮思考"},
			{Kind: SegmentTool, Text: "Bash(cat x)"},
		},
	})
	elements := payload["body"].(map[string]any)["elements"].([]any)

	// 定位三个关键元素的下标与属性。
	var thoughtIdx, answerIdx, toolsIdx = -1, -1, -1
	for i, raw := range elements {
		m, _ := raw.(map[string]any)
		switch m["element_id"] {
		case "panel_thought":
			thoughtIdx = i
			if m["tag"] != "collapsible_panel" || m["expanded"] != true {
				t.Fatalf("thought panel wrong: %#v", m)
			}
			title := m["header"].(map[string]any)["title"].(map[string]string)["content"]
			if !strings.Contains(title, "思考推理") || !strings.Contains(title, "×3") {
				t.Fatalf("thought title = %q, want 思考推理 + ×3", title)
			}
		case "answer":
			answerIdx = i
		case "panel_tools":
			toolsIdx = i
			if m["tag"] != "collapsible_panel" || m["expanded"] != false {
				t.Fatalf("tools panel wrong: %#v", m)
			}
			title := m["header"].(map[string]any)["title"].(map[string]string)["content"]
			if !strings.Contains(title, "工具调用") || !strings.Contains(title, "×6") {
				t.Fatalf("tools title = %q, want 工具调用 + ×6", title)
			}
		}
	}
	if thoughtIdx < 0 || answerIdx < 0 || toolsIdx < 0 {
		t.Fatalf("missing elements: thought=%d answer=%d tools=%d\n%#v", thoughtIdx, answerIdx, toolsIdx, elements)
	}
	// 顺序:思考在正文前,工具在正文后。
	if !(thoughtIdx < answerIdx && answerIdx < toolsIdx) {
		t.Fatalf("order wrong: thought=%d answer=%d tools=%d, want thought<answer<tools", thoughtIdx, answerIdx, toolsIdx)
	}
	// 不应再出现旧的合并 panel_process。
	if strings.Contains(fmt.Sprint(elements), "panel_process") {
		t.Fatalf("three-section layout must not emit panel_process: %#v", elements)
	}
}

// TestThreeSectionTitleFallsBackToToolCallCount 验证工具计数在 ToolRoundCount 缺失(如终态
// 仅从 result 拿到 ToolCallCount)时回退到 ToolCallCount。
func TestThreeSectionTitleFallsBackToToolCallCount(t *testing.T) {
	title := toolsSectionTitle(Event{ToolCallCount: 4})
	if !strings.Contains(title, "×4") {
		t.Fatalf("tools title = %q, want ×4 from ToolCallCount fallback", title)
	}
	empty := toolsSectionTitle(Event{})
	if !strings.Contains(empty, "暂无") {
		t.Fatalf("empty tools title = %q, want 暂无", empty)
	}
}
