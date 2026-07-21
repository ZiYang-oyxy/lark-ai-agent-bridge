package card

import (
	"fmt"
	"strings"
)

func BuildLarkCard(e Event) map[string]any {
	e = normalizeTerminalEvent(e)
	var elements []any
	if e.HelpCard != nil {
		e.Streaming = false
		elements = buildHelpElements(e.SessionID, *e.HelpCard)
		answer, _, _ := splitCardSections(e.Segments)
		if strings.TrimSpace(answer) != "" {
			elements = append([]any{noteElement("help_update_status", answer)}, elements...)
		}
		for _, action := range buildButtonActions(e) {
			elements = append(elements, action)
		}
	} else if e.StatusCard != nil {
		e.Streaming = false
		elements = buildStatusElements(e.SessionID, *e.StatusCard)
	} else if e.ResumeCard != nil {
		e.Streaming = false
		elements = buildResumeElements(e.SessionID, *e.ResumeCard)
	} else if e.LocalConfigOverview != nil {
		e.Streaming = false
		elements = buildLocalConfigOverviewElements(e.SessionID, *e.LocalConfigOverview)
	} else if e.AgentModeForm != nil {
		e.Streaming = false
		elements = buildAgentModeFormElements(e.SessionID, *e.AgentModeForm)
	} else if e.ConfigForm != nil {
		e.Streaming = false
		elements = buildConfigFormElements(e.SessionID, *e.ConfigForm)
	} else if e.MarkdownLayout {
		elements = []any{markdownElement("answer", e.Markdown)}
	} else {
		elements = make([]any, 0, len(e.Segments)+5)
		if e.InlineTimelineLayout {
			elements = append(elements, markdownElement("answer", e.Markdown))
			_, thought, _ := splitCardSections(e.Segments)
			if !e.HideAgentPanels && strings.TrimSpace(thought) != "" {
				elements = append(elements, collapsiblePanelElement(
					"panel_thought",
					"思考过程",
					false,
					[]map[string]any{markdownElement("timeline_thought", thought)},
				))
			}
		} else if e.OrderedLayout {
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

// buildConfigFormElements renders the /config (and /local-config) form as four
// bordered sections — 运行参数 / 会话行为 / 群消息 / 访问控制 — instead of a flat
// vertical list. Each field is a bold label + one-line hint + control, produced
// by fieldElements. Model / Effort / Agent-mode are intentionally not rendered
// here (those live on /agent-mode or are being retired); the ConfigForm struct
// still carries them for now.
func buildConfigFormElements(sessionID string, form ConfigForm) []any {
	intro := "⚙️ **全局运行偏好**\n\n改后只影响新进入队列的消息。此为全局默认，各群可用 `/local-config` 覆盖。"
	saveAction := "config.save"
	saveValue := ""
	if form.ChatID != "" {
		intro = "⚙️ **本群运行偏好覆盖**\n\n仅影响当前群；未修改的项继承全局 `/config`。修改后只影响新进入队列的消息。"
		saveAction = "local_config.save"
		saveValue = form.ChatID
	}

	runtime := []map[string]any{}
	runtime = append(runtime, fieldElements("cfg_home", "Agent home", "默认继承 executable 环境；显式选择注入 CONFIG_DIR", configSelectOptions("agent_home", form.AgentHome, form.AgentHomes))...)
	runtime = append(runtime, fieldElements("cfg_bin", "Agent bin", "主机项用当前 Agent 默认 executable，其余为预设", configSelectOptions("agent_bin", form.AgentBin, form.AgentBins))...)

	conversation := []map[string]any{}
	conversation = append(conversation, fieldElements("cfg_reply", "Reply mode", "append 保留全过程 · clean-card 只留末答 · latest-card 复用最新卡", configSelect("reply_mode", form.ReplyMode, form.ReplyModes))...)
	conversation = append(conversation, fieldElements("cfg_conv", "Conversation mode", "chat 按群共用会话 · topic 按话题隔离", configSelect("conversation_mode", form.ConversationMode, form.ConversationModes))...)

	group := []map[string]any{}
	group = append(group, fieldElements("cfg_group", "群消息接收", "mention_only 仅 @bot · participated_topics 已参与话题 · all 所有群消息（后两档需群消息权限）", configSelectOptions("group_message_mode", form.GroupMessageMode, []SelectOption{
		{Value: "mention_only", Label: "仅响应 @bot（默认）"},
		{Value: "participated_topics", Label: "接收已参与话题的所有消息"},
		{Value: "all_group_messages", Label: "接收所有群消息"},
	}))...)
	group = append(group, fieldElements("cfg_bots", "响应其他 bot", "默认忽略；开启后仍按群消息模式判断", configSelectOptions("respond_to_bots", form.RespondToBots, []SelectOption{
		{Value: "false", Label: "忽略（默认）"},
		{Value: "true", Label: "响应"},
	}))...)
	saveButton := map[string]any{
		"tag":              "button",
		"name":             "submit_runtime_config",
		"text":             map[string]any{"tag": "plain_text", "content": "保存"},
		"type":             "primary",
		"width":            "fill",
		"form_action_type": "submit",
		"behaviors":        callbackBehavior(sessionID, saveAction, saveValue),
	}
	closeButton := map[string]any{
		"tag":       "button",
		"name":      "close_runtime_config",
		"text":      map[string]any{"tag": "plain_text", "content": "关闭"},
		"type":      "default",
		"width":     "fill",
		"behaviors": callbackBehavior(sessionID, "config.close", ""),
	}

	return []any{
		markdownElement("config_intro", intro),
		map[string]any{
			"tag":  "form",
			"name": "runtime_config",
			"elements": []any{
				sectionElement("🤖 运行参数", runtime),
				sectionElement("💬 会话行为", conversation),
				sectionElement("👥 群消息", group),
				accessPanelElement(form),
				map[string]any{
					"tag":                "column_set",
					"horizontal_spacing": "8px",
					"columns": []any{
						map[string]any{"tag": "column", "width": "weighted", "weight": 1, "elements": []any{saveButton}},
						map[string]any{"tag": "column", "width": "weighted", "weight": 1, "elements": []any{closeButton}},
					},
				},
			},
		},
	}
}

func buildAgentModeFormElements(sessionID string, form AgentModeForm) []any {
	return []any{
		markdownElement("agent_mode_intro", "⚙️ **Agent mode**\n\n选择后影响后续新进入队列的消息。"),
		map[string]any{
			"tag":  "form",
			"name": "agent_mode_form",
			"elements": []any{
				markdownElement("agent_mode_select_label", "**使用 Agent**\n选择 `claude` 或 `codex`。"),
				configSelectOptions("agent", form.Agent, form.Agents),
				map[string]any{
					"tag":              "button",
					"name":             "submit_agent_mode",
					"text":             map[string]any{"tag": "plain_text", "content": "保存"},
					"type":             "primary",
					"form_action_type": "submit",
					"behaviors":        callbackBehavior(sessionID, "agent_mode.save", ""),
				},
			},
		},
	}
}

// buildHelpElements renders the sectioned /help card: one bordered section per
// command group, a divider, a small grey footer note, then status and config
// shortcuts. Group help adds the current chat's local-config shortcut.
func buildHelpElements(sessionID string, help HelpCard) []any {
	elements := make([]any, 0, len(help.Groups)+3)
	for groupIdx, group := range help.Groups {
		body := make([]map[string]any, 0, len(group.Lines))
		for lineIdx, line := range group.Lines {
			body = append(body, markdownElement(fmt.Sprintf("help_%d_%d", groupIdx, lineIdx), line))
		}
		elements = append(elements, sectionElement(group.Title, body))
	}
	elements = append(elements, map[string]any{"tag": "hr"})
	if strings.TrimSpace(help.Footer) != "" {
		elements = append(elements, noteElement("help_footer", help.Footer))
	}
	buttons := []Action{
		{ID: "help.status", Label: "📊 状态"},
		{ID: "help.open_config", Label: "⚙️ 全局配置"},
	}
	if chatID := strings.TrimSpace(help.ChatID); chatID != "" {
		buttons = append(buttons, Action{ID: "help.open_local_config", Label: "🏘️ 本群配置", Value: chatID})
	}
	if row := buttonRowElements(buttons, sessionID); row != nil {
		elements = append(elements, row)
	}
	return elements
}

// buildLocalConfigOverviewElements renders the read-only /local-config summary:
// a small intro note, one bordered section listing each overridable field's
// effective value with a （本群）/（继承）badge, a count of overridden fields, and
// a row of three buttons (edit / reset / close). The edit and reset callbacks
// are wired in a later task; this card only renders them.
func buildLocalConfigOverviewElements(sessionID string, overview LocalConfigOverview) []any {
	body := make([]map[string]any, 0, len(overview.Items))
	for i, item := range overview.Items {
		badge := "（继承）"
		if item.Overridden {
			badge = "（本群）"
		}
		body = append(body, markdownElement(
			fmt.Sprintf("lc_item_%d", i),
			fmt.Sprintf("**%s**：%s %s", item.Label, item.Value, badge),
		))
	}

	elements := []any{
		noteElement("lc_intro", "仅影响当前群；未覆盖项继承全局 `/config`。标「（本群）」的是本群已覆盖，标「（继承）」的沿用全局。"),
		sectionElement("当前生效值", body),
		noteElement("lc_count", fmt.Sprintf("本群覆盖了 %d 项。点「编辑覆盖」逐项调整，或「重置」清空。", overview.OverrideCount)),
	}
	buttons := []Action{
		{ID: "local_config.edit", Label: "编辑覆盖", Value: overview.ChatID},
		{ID: "local_config.reset", Label: "重置本群", Value: overview.ChatID},
		{ID: "config.close", Label: "关闭"},
	}
	if row := buttonRowElements(buttons, sessionID); row != nil {
		elements = append(elements, row)
	}
	return elements
}

// statusFieldLine renders one status row as a single compact markdown line:
// **标签**：值. Values flagged Code are wrapped in inline code (paths, ids, mode
// keys) so raw technical values stay readable and un-translated. Empty values
// fall back to a grey placeholder.
func statusFieldLine(id string, field StatusField) map[string]any {
	value := strings.TrimSpace(field.Value)
	if value == "" {
		value = "_（无）_"
	} else if field.Code {
		value = "`" + value + "`"
	}
	return markdownElement(id, fmt.Sprintf("**%s**：%s", field.Label, value))
}

// buildStatusElements renders the /status card as bordered sections — 会话概览 /
// 运行偏好 / 运行时 — mirroring the /config and /local-config visual language.
// Chinese labels front every row; technical values (mode keys, ids, workdir)
// stay verbatim as inline code. A refresh + open-config button row closes the
// card. NotStarted sessions render only the sections that have fields plus a
// friendly hint that no session has started yet.
func buildStatusElements(sessionID string, status StatusCard) []any {
	elements := make([]any, 0, len(status.Sections)+3)
	elements = append(elements, noteElement("status_intro", "当前会话与运行偏好一览。技术取值（模式键、Session、工作目录）保留原文。"))
	for sectionIdx, section := range status.Sections {
		body := make([]map[string]any, 0, len(section.Fields))
		for fieldIdx, field := range section.Fields {
			body = append(body, statusFieldLine(fmt.Sprintf("status_%d_%d", sectionIdx, fieldIdx), field))
		}
		elements = append(elements, sectionElement(section.Title, body))
	}
	if status.NotStarted {
		elements = append(elements, noteElement("status_not_started", "本会话尚未开始运行。直接发送消息即可开启第一轮任务。"))
	}
	buttons := []Action{
		{ID: "status.refresh", Label: "🔄 刷新"},
		{ID: "help.open_config", Label: "⚙️ 全局配置"},
	}
	if row := buttonRowElements(buttons, sessionID); row != nil {
		elements = append(elements, row)
	}
	return elements
}

// buildResumeElements renders the /resume list: an intro note naming the agent
// and workdir the sessions belong to, then one bordered section per recent
// session (index · time · [当前] badge, optional summary) with a one-click 恢复
// button whose callback carries the session id. Empty Items renders a friendly
// note pointing back at plain messaging.
func buildResumeElements(sessionID string, resume ResumeCard) []any {
	elements := make([]any, 0, len(resume.Items)+2)
	elements = append(elements, noteElement("resume_intro", fmt.Sprintf("最近的历史会话（Agent `%s` · 工作目录 `%s`）。点「恢复」即可继续该会话，下一条普通消息将接着它跑。", resume.Agent, resume.WorkDir)))
	if len(resume.Items) == 0 {
		elements = append(elements, noteElement("resume_empty", "当前 Agent 与工作目录下没有可恢复的历史会话。直接发送消息开始新会话即可。"))
		return elements
	}
	for i, item := range resume.Items {
		title := fmt.Sprintf("%d · %s", item.Index, item.UpdatedAt)
		if item.Current {
			title += "  ·  当前"
		}
		body := []map[string]any{
			markdownElement(fmt.Sprintf("resume_%d_id", i), "`"+item.SessionID+"`"),
		}
		if summary := strings.TrimSpace(item.Summary); summary != "" {
			body = append(body, noteElement(fmt.Sprintf("resume_%d_summary", i), summary))
		}
		if row := buttonRowElements([]Action{{ID: "resume.select", Label: "恢复", Value: item.SessionID, Disabled: item.Current}}, sessionID); row != nil {
			body = append(body, row)
		}
		elements = append(elements, sectionElement(title, body))
	}
	return elements
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
		if !action.Disabled && action.Confirm != nil {
			button["confirm"] = map[string]any{
				"title": map[string]any{"tag": "plain_text", "content": action.Confirm.Title},
				"text":  map[string]any{"tag": "plain_text", "content": action.Confirm.Text},
			}
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
			button["confirm"] = map[string]any{
				"title": map[string]any{"tag": "plain_text", "content": "确认停止任务？"},
				"text":  map[string]any{"tag": "plain_text", "content": "停止后，本轮任务将立即结束，当前已生成的内容会保留。"},
			}
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
	first, second := MetaRows(meta)
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

// MetaRows formats the two compact metadata rows shared by full CardKit and
// lightweight append replies.
func MetaRows(meta Meta) (string, string) {
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
	if meta.CtxOK {
		dot := "🟢"
		switch {
		case meta.CtxUsedPercent >= 85:
			dot = "🔴"
		case meta.CtxUsedPercent >= 60:
			dot = "🟡"
		}
		if meta.CtxWindow > 0 {
			return fmt.Sprintf("%s ctx: %d%% (%s/%s)", dot, meta.CtxUsedPercent, compactInt(meta.CtxTokens), compactInt(meta.CtxWindow))
		}
		return fmt.Sprintf("%s ctx: %d%%", dot, meta.CtxUsedPercent)
	}
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
	case "help":
		return "💡 命令帮助"
	case "status":
		return "📊 会话状态"
	case "resume":
		return "🕘 恢复历史会话"
	case "config":
		return "⚙️ 全局运行偏好"
	case "local_config":
		return "本群运行偏好覆盖"
	case "local_config_overview":
		return "🏠 本群运行偏好覆盖"
	case "local_config_saved":
		return "本群覆盖已保存"
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
		return "grey"
	case "local_config_overview":
		return "turquoise"
	case "status":
		return "indigo"
	case "resume":
		return "wathet"
	case "help", "local_config":
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
