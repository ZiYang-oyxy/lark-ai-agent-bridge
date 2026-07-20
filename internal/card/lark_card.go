package card

import (
	"fmt"
	"strings"
)

func BuildLarkCard(e Event) map[string]any {
	e = normalizeTerminalEvent(e)
	var elements []any
	if e.ConfigForm != nil {
		e.Streaming = false
		elements = buildConfigFormElements(e.SessionID, *e.ConfigForm)
	} else if e.MarkdownLayout {
		elements = []any{markdownElement("answer", e.Markdown)}
	} else {
		elements = make([]any, 0, len(e.Segments)+5)
		if e.OrderedLayout {
			elements = append(elements, buildOrderedTimelineElements(e)...)
		} else {
			answer, thought, tools := splitCardSections(e.Segments)
			if strings.TrimSpace(answer) != "" || e.Streaming {
				elements = append(elements, markdownElement("answer", answer))
			}
			if shouldShowAgentPanels(e, thought, tools) {
				elements = append(elements,
					collapsiblePanelElement("panel_process", processPanelTitle(e, tools), e.ProcessExpanded, processPanelBody(thought, tools)),
				)
			}
		}
		if e.Message != "" {
			elements = append(elements, markdownElement("message", e.Message))
		}
		for _, action := range buildButtonActions(e) {
			elements = append(elements, action)
		}
		elements = append(elements, buildMetaElements(e.Meta)...)
	}
	title := headerTitle(e)
	payload := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":   true,
			"streaming_mode": e.Streaming,
			"summary":        map[string]string{"content": title},
		},
		"body": map[string]any{
			"direction":        "vertical",
			"vertical_spacing": "8px",
			"padding":          "12px 12px 12px 12px",
			"elements":         elements,
		},
	}
	if !e.MarkdownLayout {
		payload["header"] = map[string]any{
			"template": headerTemplate(e),
			"title":    map[string]any{"tag": "plain_text", "content": title},
		}
	}
	if e.Streaming {
		config := payload["config"].(map[string]any)
		config["streaming_config"] = map[string]any{
			"print_frequency_ms": map[string]int{"default": 80, "android": 80, "ios": 80, "pc": 80},
			"print_step":         map[string]int{"default": 1, "android": 1, "ios": 1, "pc": 1},
			"print_strategy":     "fast",
		}
	}
	return payload
}

func buildOrderedTimelineElements(e Event) []any {
	elements := make([]any, 0, len(e.Segments)+1)
	_, thought, _ := splitCardSections(e.Segments)
	if !e.HideAgentPanels && strings.TrimSpace(thought) != "" {
		elements = append(elements, collapsiblePanelElement(
			"panel_thought",
			"思考",
			false,
			[]map[string]any{markdownElement("timeline_thought", thought)},
		))
	}
	textIndex := 0
	toolIndex := 0
	errorIndex := 0
	for _, segment := range e.Segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		switch segment.Kind {
		case SegmentThought:
			continue
		case SegmentTool:
			if e.HideAgentPanels {
				continue
			}
			toolIndex++
			elements = append(elements, collapsiblePanelElement(
				fmt.Sprintf("timeline_tool_%d", toolIndex),
				"工具调用",
				false,
				[]map[string]any{markdownElement(fmt.Sprintf("timeline_tool_body_%d", toolIndex), text)},
			))
		case SegmentError:
			errorIndex++
			elements = append(elements, markdownElement(fmt.Sprintf("timeline_error_%d", errorIndex), "**Error**\n"+text))
		default:
			textIndex++
			elements = append(elements, markdownElement(fmt.Sprintf("timeline_text_%d", textIndex), text))
		}
	}
	return elements
}

func terminalCardEvent(eventType string) bool {
	switch eventType {
	case "result", "error", "stopped", "interrupted":
		return true
	default:
		return false
	}
}

func normalizeTerminalEvent(e Event) Event {
	if !terminalCardEvent(e.Type) {
		return e
	}
	e.Streaming = false
	for i := range e.Actions {
		e.Actions[i].Disabled = true
	}
	if e.StopButton.Visible {
		e.StopButton.Disabled = true
	}
	return e
}

func buildConfigFormElements(sessionID string, form ConfigForm) []any {
	return []any{
		markdownElement("config_intro", "⚙️ **个人运行偏好**\n\n修改后只影响新进入队列的消息。"),
		map[string]any{
			"tag":  "form",
			"name": "runtime_config",
			"elements": []any{
				markdownElement("cfg_agent", "**Agent**\n支持 `claude` 和 `codex`，选项来自 `agents.json`。"),
				configSelectOptions("agent", form.Agent, form.Agents),
				markdownElement("cfg_agent_home", "**Agent home**\n`默认` 继承 executable 的环境配置；显式选择时分别注入 `CLAUDE_CONFIG_DIR` 或 `CODEX_HOME`。"),
				configSelectOptions("agent_home", form.AgentHome, form.AgentHomes),
				markdownElement("cfg_agent_bin", "**Agent bin**\n每项显示为 `名称 · 作用`；主机项使用当前 Agent 的默认 executable，其余为 `agents.json` 预设。"),
				configSelectOptions("agent_bin", form.AgentBin, form.AgentBins),
				markdownElement("config_model_label", "**Model**\n仅对 Claude 生效；Codex 由所选 executable 及其环境配置决定。"),
				configSelect("model", form.Model, form.Models),
				markdownElement("config_effort_label", "**Effort**\n仅对 Claude 生效；Codex 由所选 executable 及其环境配置决定。"),
				configSelect("effort", form.Effort, form.Efforts),
				markdownElement("config_reply_label", "**Reply mode**\n`append` 新建卡片并保留全部回复与过程；`append-clean-card` 新建卡片，完成后只留最后回复；`latest-card` 复用最新卡片，其他同 clean。"),
				configSelect("reply_mode", form.ReplyMode, form.ReplyModes),
				markdownElement("config_scope_label", "**Conversation mode**\n`chat` 回复到普通聊天并按 chat 共用会话；`topic` 回复到话题并按 thread 隔离会话。"),
				configSelect("conversation_mode", form.ConversationMode, form.ConversationModes),
				markdownElement("cfg_group_msg", "**群消息接收**\n`mention_only` 仅响应结构化 @bot；`participated_topics` 接收 Bridge 已参与话题的后续消息；`all_group_messages` 接收所有已授权群消息。后两档需要 `im:message.group_msg`。"),
				configSelectOptions("group_message_mode", form.GroupMessageMode, []SelectOption{
					{Value: "mention_only", Label: "仅响应 @bot（默认）"},
					{Value: "participated_topics", Label: "接收已参与话题的所有消息"},
					{Value: "all_group_messages", Label: "接收所有群消息"},
				}),
				markdownElement("cfg_bot_sender", "**响应其他 bot/app 消息**\n默认忽略；开启后仍按上面的群消息模式判断。Bridge 自身消息始终忽略。"),
				configSelectOptions("respond_to_bots", form.RespondToBots, []SelectOption{
					{Value: "false", Label: "忽略（默认）"},
					{Value: "true", Label: "响应"},
				}),
				accessPanelElement(form),
				map[string]any{
					"tag":              "button",
					"name":             "submit_runtime_config",
					"text":             map[string]any{"tag": "plain_text", "content": "保存"},
					"type":             "primary",
					"form_action_type": "submit",
					"behaviors":        callbackBehavior(sessionID, "config.save", ""),
				},
			},
		},
	}
}

func accessPanelElement(form ConfigForm) map[string]any {
	userLine := "_（暂无）_"
	if len(form.AllowedUsers) > 0 {
		mentions := make([]string, 0, len(form.AllowedUsers))
		for _, id := range form.AllowedUsers {
			mentions = append(mentions, fmt.Sprintf("<at id=\"%s\"></at>", id))
		}
		userLine = strings.Join(mentions, "  ")
	}
	adminLine := "_（暂无）_"
	if len(form.Admins) > 0 {
		mentions := make([]string, 0, len(form.Admins))
		for _, id := range form.Admins {
			mentions = append(mentions, fmt.Sprintf("<at id=\"%s\"></at>", id))
		}
		adminLine = strings.Join(mentions, "  ")
	}
	chatLine := "_（暂无）_"
	if len(form.AllowedChats) > 0 {
		lines := make([]string, 0, len(form.AllowedChats))
		for _, chat := range form.AllowedChats {
			name := chat.Name
			if name == "" {
				name = "(未知群)"
			}
			suffix := chat.ID
			if len(suffix) > 6 {
				suffix = suffix[len(suffix)-6:]
			}
			lines = append(lines, fmt.Sprintf("- **%s**（...%s）", name, suffix))
		}
		chatLine = strings.Join(lines, "\n")
	}
	ownerState := form.OwnerState
	if ownerState == "" {
		ownerState = "unknown owner=missing"
	}
	content := fmt.Sprintf("_留空 = 不响应聊天消息。_\n\n**owner API**：`%s`\n\n**允许私聊的用户**（共 %d 人）\n%s\n\n_加 / 删：_ `/invite user @某人`  `/remove user @某人`\n\n**允许响应的群**（共 %d 个）\n%s\n\n_加 / 删：_ `/invite group`  `/remove group`  `/invite all group`\n\n**管理员**（共 %d 人）\n%s\n\n_加 / 删：_ `/invite admin @某人`  `/remove admin @某人`", ownerState, len(form.AllowedUsers), userLine, len(form.AllowedChats), chatLine, len(form.Admins), adminLine)
	return collapsiblePanelElement("panel_access", "🔒 访问控制", false, []map[string]any{markdownElement("access_summary", content)})
}

func configSelect(name, initial string, values []string) map[string]any {
	options := make([]any, 0, len(values))
	for _, value := range values {
		options = append(options, map[string]any{
			"text":  map[string]any{"tag": "plain_text", "content": value},
			"value": value,
		})
	}
	return map[string]any{
		"tag":            "select_static",
		"name":           name,
		"initial_option": initial,
		"options":        options,
	}
}

// configSelectOptions renders a dropdown whose displayed text differs from the
// submitted value: value is the stored key, Label is the richer display text.
func configSelectOptions(name, initial string, opts []SelectOption) map[string]any {
	options := make([]any, 0, len(opts))
	for _, opt := range opts {
		display := opt.Label
		if display == "" {
			display = opt.Value
		}
		options = append(options, map[string]any{
			"text":  map[string]any{"tag": "plain_text", "content": display},
			"value": opt.Value,
		})
	}
	return map[string]any{
		"tag":            "select_static",
		"name":           name,
		"initial_option": initial,
		"options":        options,
	}
}

func splitCardSections(segments []Segment) (string, string, string) {
	var answer strings.Builder
	var thought strings.Builder
	var tools strings.Builder
	write := func(b *strings.Builder, text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
	}
	for _, seg := range segments {
		switch seg.Kind {
		case SegmentThought:
			write(&thought, seg.Text)
		case SegmentTool:
			write(&tools, seg.Text)
		case SegmentError:
			write(&answer, "**Error**\n"+seg.Text)
		default:
			write(&answer, seg.Text)
		}
	}
	return answer.String(), thought.String(), tools.String()
}

// shouldShowAgentPanels 只在思考或工具真正有内容时渲染过程折叠区。
// 运行中不再为占位而显示空面板(v2:排版简洁 + 骨架稳定)。
func shouldShowAgentPanels(e Event, thought, tools string) bool {
	if e.HideAgentPanels {
		return false
	}
	return strings.TrimSpace(thought) != "" || strings.TrimSpace(tools) != ""
}

// processPanelTitle 生成合并后的"过程"折叠区标题。
// 标题里唯一随运行推进变化的部分是工具计数 N;思考/工具的存在与否只影响是否出现该面板,
// 不再使用"生成中/已完成/暂无"这类每帧可能变化的文案(v2:让骨架尽量稳定,便于 native 流式命中)。
func processPanelTitle(e Event, tools string) string {
	if n := e.ToolCallCount; n > 0 {
		return fmt.Sprintf("过程 · 工具调用（%d）", n)
	}
	if strings.TrimSpace(tools) != "" {
		return "过程 · 工具调用"
	}
	return "过程"
}

// processPanelBody 把思考与工具收进一个折叠区,内部仍保留 thought / tools 两个 element_id。
func processPanelBody(thought, tools string) []map[string]any {
	var body []map[string]any
	if strings.TrimSpace(thought) != "" {
		body = append(body, markdownElement("thought", thought))
	}
	if strings.TrimSpace(tools) != "" {
		body = append(body, markdownElement("tools", tools))
	}
	if len(body) == 0 {
		body = append(body, markdownElement("thought", ""))
	}
	return body
}

func markdownElement(id, content string) map[string]any {
	return map[string]any{
		"tag":        "markdown",
		"element_id": id,
		"content":    content,
	}
}

func collapsiblePanelElement(id, title string, expanded bool, elements []map[string]any) map[string]any {
	return map[string]any{
		"tag":              "collapsible_panel",
		"element_id":       id,
		"expanded":         expanded,
		"vertical_spacing": "8px",
		"padding":          "8px 8px 8px 8px",
		"header": map[string]any{
			"title": map[string]string{
				"tag":     "plain_text",
				"content": title,
			},
			"vertical_align": "center",
			"padding":        "4px 0px 4px 8px",
			"width":          "auto_when_fold",
			"icon": map[string]string{
				"tag":   "standard_icon",
				"token": "down-small-ccm_outlined",
				"color": "",
				"size":  "16px 16px",
			},
			"icon_position":       "right",
			"icon_expanded_angle": -180,
		},
		"border": map[string]string{
			"color":         "grey",
			"corner_radius": "5px",
		},
		"elements": elements,
	}
}

func buildButtonActions(e Event) []any {
	var buttons []any
	for i, action := range e.Actions {
		buttonType := "default"
		if action.ID == "cancel_workdir" {
			buttonType = "danger"
		}
		button := map[string]any{
			"tag":        "button",
			"element_id": fmt.Sprintf("btn_%d", i+1),
			"text":       map[string]any{"tag": "plain_text", "content": action.Label},
			"type":       buttonType,
			"width":      "fill",
			"size":       "medium",
			"disabled":   action.Disabled,
		}
		if !action.Disabled && action.URL != "" {
			button["behaviors"] = []any{map[string]any{"type": "open_url", "default_url": action.URL}}
		} else if !action.Disabled {
			button["behaviors"] = callbackBehavior(e.SessionID, action.ID, action.Value)
		}
		buttons = append(buttons, button)
	}
	if e.StopButton.Visible {
		buttonType := "danger"
		label := stopButtonLabel(e)
		if e.StopButton.Disabled {
			buttonType = "default"
		}
		button := map[string]any{
			"tag":        "button",
			"element_id": "btn_stop",
			"text":       map[string]any{"tag": "plain_text", "content": label},
			"type":       buttonType,
			"width":      "fill",
			"size":       "medium",
			"disabled":   e.StopButton.Disabled,
		}
		if !e.StopButton.Disabled {
			button["behaviors"] = callbackBehavior(e.SessionID, "stop", "")
		}
		buttons = append(buttons, button)
	}
	return buttons
}

func stopButtonLabel(e Event) string {
	if !e.StopButton.Disabled {
		return "停止"
	}
	switch e.Type {
	case "result":
		return "已完成"
	case "error":
		return "已结束"
	case "stopped":
		return "已停止"
	case "interrupted":
		return "已中断"
	default:
		return "已停止"
	}
}

func callbackBehavior(sessionID, actionID, value string) []any {
	return []any{
		map[string]any{
			"type": "callback",
			"value": map[string]any{
				"action_id": actionID,
				"value":     value,
				"session":   sessionID,
			},
		},
	}
}

// buildMetaElements 以"紧凑行"呈现底部 meta(v2):
// 第一行 agent · model(effort) · tokens,第二行 user · ip · workdir。
// 用间隔点连接、图标前缀,避免旧的加权分栏在窄屏错行;空字段跳过,空行不渲染。
func buildMetaElements(meta Meta) []any {
	first, second := metaRows(meta)
	if first == "" && second == "" {
		return nil
	}
	elements := []any{map[string]any{"tag": "hr"}}
	if first != "" {
		elements = append(elements, metaLineElement("meta_primary", first))
	}
	if second != "" {
		elements = append(elements, metaLineElement("meta_runtime", second))
	}
	return elements
}

func metaRows(meta Meta) (string, string) {
	var first []string
	if meta.Agent != "" {
		first = append(first, "🤖 "+displayAgent(meta.Agent))
	}
	if model := metaModelText(meta); model != "" {
		first = append(first, "🧠 "+model)
	}
	if tokens := metaTokenText(meta); tokens != "" {
		first = append(first, tokens)
	}
	var second []string
	if meta.User != "" {
		second = append(second, "👤 "+meta.User)
	}
	if meta.IP != "" {
		second = append(second, "🖥️ "+meta.IP)
	}
	if meta.WorkDir != "" {
		second = append(second, "📁 `"+meta.WorkDir+"`")
	}
	return strings.Join(first, " · "), strings.Join(second, " · ")
}

// metaModelText 用实际模型值(缺失显 unknown),effort 以括号后缀呈现,
// 取代旧的 "requested: x · actual: y · effort: z" 冗长三段。
func metaModelText(meta Meta) string {
	if meta.ModelInfo != (ModelInfo{}) {
		model := meta.ModelInfo.Actual
		if model == "" {
			model = "unknown"
		}
		if effort := meta.ModelInfo.Effort; effort != "" && effort != "unknown" {
			return fmt.Sprintf("%s（%s）", model, effort)
		}
		return model
	}
	return meta.Model
}

func metaTokenText(meta Meta) string {
	runTokens := meta.RunTokens
	totalTokens := meta.TotalTokens
	if runTokens == 0 && meta.Tokens > 0 {
		runTokens = meta.Tokens
	}
	if totalTokens == 0 && meta.Tokens > 0 {
		totalTokens = meta.Tokens
	}
	if runTokens == 0 && totalTokens == 0 {
		return ""
	}
	if totalTokens > 0 {
		return fmt.Sprintf("🔢 tokens: ▶ %s / ∑ %s", compactInt(runTokens), compactInt(totalTokens))
	}
	return fmt.Sprintf("🔢 tokens: ▶ %s", compactInt(runTokens))
}

func metaLineElement(id, content string) map[string]any {
	el := markdownElement(id, content)
	el["text_size"] = "notation"
	return el
}

func displayAgent(agent string) string {
	switch strings.ToLower(agent) {
	case "claude":
		return "Claude"
	default:
		return agent
	}
}

func compactInt(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	value := float64(n) / 1000
	text := fmt.Sprintf("%.1fk", value)
	if strings.HasSuffix(text, ".0k") {
		return strings.TrimSuffix(text, ".0k") + "k"
	}
	return text
}

func titleForEvent(eventType string) string {
	switch eventType {
	case "workdir_confirm":
		return "工作目录确认"
	case "workdir_created":
		return "✅ 工作目录已创建"
	case "workdir_cancelled":
		return "⏹ 已取消"
	case "error":
		return "Agent 错误"
	case "interrupted":
		return "服务重启，任务已中断"
	case "config":
		return "个人运行偏好"
	default:
		return "AI Agent"
	}
}

func templateForEvent(eventType string) string {
	switch eventType {
	case "workdir_confirm":
		return "orange"
	case "workdir_created":
		return "green"
	case "workdir_cancelled":
		return "grey"
	case "error":
		return "red"
	case "interrupted":
		return "orange"
	case "config":
		return "blue"
	default:
		return "green"
	}
}

func headerTitle(e Event) string {
	if e.HeaderTitle != "" {
		return e.HeaderTitle
	}
	return titleForEvent(e.Type)
}

func headerTemplate(e Event) string {
	if e.HeaderTemplate != "" {
		return e.HeaderTemplate
	}
	return templateForEvent(e.Type)
}
