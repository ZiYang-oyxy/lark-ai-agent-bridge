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
	} else if e.StatusCard != nil {
		e.Streaming = false
		elements = buildStatusElements(e.SessionID, *e.StatusCard)
		if hint := buildHelpVersionElements(e.SessionID, e.VersionStatus); len(hint) > 0 {
			elements = append(hint, elements...)
		}
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
		if hint := buildHelpVersionElements(e.SessionID, e.VersionStatus); len(hint) > 0 {
			elements = append(hint, elements...)
		}
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
		} else if e.ThreeSectionLayout {
			elements = append(elements, buildThreeSectionElements(e)...)
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
	runtime = append(runtime, fieldElements("cfg_home", "Agent 主目录", "默认继承 executable 环境；显式选择注入 CONFIG_DIR", configSelectOptions("agent_home", form.AgentHome, form.AgentHomes))...)
	runtime = append(runtime, fieldElements("cfg_bin", "Agent 可执行文件", "主机项用当前 Agent 默认 executable，其余为预设", configSelectOptions("agent_bin", form.AgentBin, form.AgentBins))...)
	runtime = append(runtime, fieldElements("cfg_effort", "推理深度", "default 跟随 Agent 自身设定 · low/medium/high 显式指定思考强度", configSelectOptions("effort", form.Effort, effortOptions(form.Efforts)))...)

	conversation := []map[string]any{}
	conversation = append(conversation, fieldElements("cfg_reply", "回复模式", "append 保留全过程 · clean-card 只留末答 · latest-card 复用最新卡", configSelect("reply_mode", form.ReplyMode, form.ReplyModes))...)
	conversation = append(conversation, fieldElements("cfg_conv", "会话模式", "chat 按群共用会话 · topic 按话题隔离", configSelect("conversation_mode", form.ConversationMode, form.ConversationModes))...)

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
	group = append(group, fieldElements("cfg_notify", "完成通知", "开启后每次任务完成补发一条 @发起人的消息产生红点提醒；默认关闭避免打扰", configSelectOptions("notify_on_complete", form.NotifyOnComplete, []SelectOption{
		{Value: "false", Label: "关闭（默认）"},
		{Value: "true", Label: "完成时提醒发起人"},
	}))...)

	statusBar := buildStatusBarSection(form)

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
			// 各段默认收起：/config 首屏只见标题，用户按需展开。access 段本就默认收起。
			"elements": []any{
				collapsiblePanelElement("cfg_p_runtime", "🤖 运行参数", false, runtime),
				collapsiblePanelElement("cfg_p_conv", "💬 会话行为", false, conversation),
				collapsiblePanelElement("cfg_p_group", "👥 群消息", false, group),
				collapsiblePanelElement("cfg_p_bar", "📊 元信息行", false, statusBar),
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

// buildStatusBarSection 用三个 `select_static` 布尔下拉呈现三个元信息行开关。
//
// 曾经用过飞书 CardKit `checker` 组件——视觉上是复选框、贴合语义,但实测
// checker 并不是 form 输入字段:即便放在 form 内,勾选状态也**不会**被 form.submit
// 批量收集,用户点了但保存后回到旧值。回到 select_static 两选项("隐藏"/"显示")
// 的做法:与本卡片其他布尔字段(respond_to_bots/notify_on_complete)保持一致,
// handler 侧无需改动,提交行为可靠。
//
// 每行的 hint 里带一段**带 mock 数据的示例行**,让用户一眼看到"开启后卡片底部
// 会长什么样",比只列字段名(agent/会话/模型)直觉得多。
//
// element_id 上限 20 字符,fieldElements 追加 _label/_hint 要 prefix ≤ 14——用短 slug
// bar_(status bar 一族)。
func buildStatusBarSection(form ConfigForm) []map[string]any {
	boolOpts := func(onLabel string) []SelectOption {
		return []SelectOption{
			{Value: "false", Label: "隐藏（默认）"},
			{Value: "true", Label: onLabel},
		}
	}
	out := []map[string]any{
		markdownElement("cfg_bar_intro", "**元信息行**\n选择要显示的行；未勾选的行不渲染。示例展示了开启后卡片底部的样子（示例数据）。"),
	}
	out = append(out, fieldElements("cfg_bar_agent",
		"Agent 行",
		"例：🍊 535a99 · 🧠 claude-opus-4-7[1m]（high） · 🟢 ctx: 35% (354.5k/1000k)",
		configSelectOptions("show_meta_row_agent", form.ShowMetaRowAgent, boolOpts("显示 Agent 行")))...)
	out = append(out, fieldElements("cfg_bar_rt",
		"主机信息行",
		"例：👤 lijun.996 · 🖥️ 192.0.2.42 · 📁 /home/<USER>/ws",
		configSelectOptions("show_meta_row_runtime", form.ShowMetaRowRuntime, boolOpts("显示主机信息行")))...)
	out = append(out, fieldElements("cfg_bar_dev",
		"开发者行",
		"例：🐛 v0.1.8-rc.5 · ⬆️ 最新 v0.1.8-rc.6（🐛 rc / 🦋 stable）",
		configSelectOptions("show_meta_row_developer", form.ShowMetaRowDeveloper, boolOpts("显示开发者行")))...)
	return out
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
	elements := make([]any, 0, len(help.Groups)+4)
	elements = append(elements, buildHelpVersionElements(sessionID, help.VersionStatus)...)
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

// buildHelpVersionElements keeps version information at the top of /help.
// Available updates get a bordered callout with the details CTA inside it;
// passive states stay compact so they do not compete with command help.
func buildHelpVersionElements(sessionID string, status *HelpVersionStatus) []any {
	if status == nil || strings.TrimSpace(status.CurrentVersion) == "" {
		return nil
	}
	if !status.UpdateAvailable {
		content := fmt.Sprintf("当前版本：`%s`", status.CurrentVersion)
		if detail := strings.TrimSpace(status.Status); detail != "" {
			content += " · " + detail
		}
		return []any{noteElement("help_version_status", content)}
	}

	// 有可用更新时把版本区间「vCurrent → vLatest」并进 section 标题，
	// 与「✨ 发现新版本」共居一行；body 只留「查看更新 →」按钮。
	body := []map[string]any{}
	if actionRow := buttonRowElements([]Action{status.DetailsAction}, sessionID); actionRow != nil {
		body = append(body, actionRow)
	}
	title := fmt.Sprintf("✨ 发现新版本 %s → %s", status.CurrentVersion, status.LatestVersion)
	panel := sectionElement(title, body)
	panel["border"] = map[string]string{"color": "blue", "corner_radius": "5px"}
	return []any{panel}
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

// buildResumeElements renders a compact /resume list: a one-line intro, then one
// row per recent session laid out as [ text | 恢复 button ]. Each row's text is a
// single line — 序号 · 时间 · 摘要 (truncated) with a 当前 marker — so rows stay
// low. The session id lives only on the button callback value (it is machine
// detail, redundant on-screen); users tell rows apart by time and summary. Empty
// Items renders a friendly note.
func buildResumeElements(sessionID string, resume ResumeCard) []any {
	elements := make([]any, 0, len(resume.Items)+2)
	elements = append(elements, noteElement("resume_intro", fmt.Sprintf("最近历史会话（`%s`）· 点「恢复」继续", resume.Agent)))
	if len(resume.Items) == 0 {
		elements = append(elements, noteElement("resume_empty", "当前 Agent 与工作目录下没有可恢复的历史会话。直接发送消息开始新会话即可。"))
		return elements
	}
	for i, item := range resume.Items {
		line := fmt.Sprintf("**%d.** %s", item.Index, item.UpdatedAt)
		if item.Current {
			line += " · 当前"
		}
		if summary := truncateResumeSummary(item.Summary); summary != "" {
			line += " · " + summary
		}
		text := markdownElement(fmt.Sprintf("resume_%d", i), line)
		button := map[string]any{
			"tag":      "button",
			"text":     map[string]any{"tag": "plain_text", "content": "恢复"},
			"type":     "primary",
			"width":    "default",
			"size":     "small",
			"disabled": item.Current,
		}
		if !item.Current {
			button["behaviors"] = callbackBehavior(sessionID, "resume.select", item.SessionID)
		}
		elements = append(elements, map[string]any{
			"tag":                "column_set",
			"horizontal_spacing": "8px",
			"columns": []any{
				map[string]any{"tag": "column", "width": "weighted", "weight": 1, "vertical_align": "center", "elements": []any{text}},
				map[string]any{"tag": "column", "width": "auto", "vertical_align": "center", "elements": []any{button}},
			},
		})
	}
	return elements
}

// truncateResumeSummary keeps a session summary to one short line so /resume rows
// stay compact; it collapses newlines and clips over-long summaries with an
// ellipsis.
func truncateResumeSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	summary = strings.ReplaceAll(summary, "\n", " ")
	const max = 24
	runes := []rune(summary)
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return summary
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

// effortOptions turns a plain effort list (e.g. "default","low","medium","high")
// into SelectOption pairs that annotate "default" so users know it defers to
// the agent's own setting.
func effortOptions(values []string) []SelectOption {
	opts := make([]SelectOption, 0, len(values))
	for _, v := range values {
		label := v
		if v == "default" {
			label = "default（跟随 Agent 默认）"
		}
		opts = append(opts, SelectOption{Value: v, Label: label})
	}
	return opts
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
		// 让下拉占满卡片全宽,避免宽度跟着当前所选文字伸缩、视觉参差不齐。
		"width": "fill",
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
		// 让下拉占满卡片全宽,避免宽度跟着当前所选文字伸缩、视觉参差不齐。
		"width": "fill",
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

// buildThreeSectionElements 渲染 append-clean-card 的三段结构(参考 feishu-ai-agent-platform):
//
//	① 思考推理折叠区(element_id=panel_thought,正文之前,默认展开,只显示最新一次 COT)
//	② 正文(element_id=answer,流式)
//	③ 工具调用折叠区(element_id=panel_tools,正文之后,默认折叠,只显示最新一次调用命令+输出)
//
// 折叠区标题带「× N」计数(对齐用户参考的第三方卡片「工具调用 ×6」),让用户即使只看到
// 最新一次,也知道背后累计发生了多少轮思考 / 多少次工具调用。展开态由 e.ThoughtExpanded /
// e.ToolsExpanded 决定(stream 层填);终态两段都折叠。空段不渲染对应折叠区。
func buildThreeSectionElements(e Event) []any {
	answer, thought, tools := splitCardSections(e.Segments)
	elements := make([]any, 0, 3)
	if !e.HideAgentPanels && strings.TrimSpace(thought) != "" {
		elements = append(elements, collapsiblePanelElement(
			"panel_thought",
			thoughtSectionTitle(e),
			e.ThoughtExpanded,
			[]map[string]any{markdownElement("thought", thought)},
		))
	}
	if strings.TrimSpace(answer) != "" || e.Streaming {
		elements = append(elements, markdownElement("answer", answer))
	}
	if !e.HideAgentPanels && strings.TrimSpace(tools) != "" {
		elements = append(elements, collapsiblePanelElement(
			"panel_tools",
			toolsSectionTitle(e),
			e.ToolsExpanded,
			[]map[string]any{markdownElement("tools", tools)},
		))
	}
	return elements
}

// thoughtSectionTitle 拼「💭 思考推理（生成中/已完成[· × N]，点击收起/展开）」。
// 动作词随 expanded 切,状态词随 Streaming 切,计数随累计轮次切。
func thoughtSectionTitle(e Event) string {
	action := "点击展开"
	if e.ThoughtExpanded {
		action = "点击收起"
	}
	state := "已完成"
	if e.Streaming {
		state = "生成中"
	}
	if e.ThoughtRoundCount > 1 {
		return fmt.Sprintf("💭 思考推理（%s · ×%d，%s）", state, e.ThoughtRoundCount, action)
	}
	return fmt.Sprintf("💭 思考推理（%s，%s）", state, action)
}

// toolsSectionTitle 拼「🔧 工具调用（×N，点击展开/收起）」。次数优先取 ToolRoundCount,
// 回退到 ToolCallCount(终态从 result 兜底)。无次数时显示「暂无」。
func toolsSectionTitle(e Event) string {
	action := "点击展开"
	if e.ToolsExpanded {
		action = "点击收起"
	}
	count := e.ToolRoundCount
	if count == 0 {
		count = e.ToolCallCount
	}
	if count > 0 {
		return fmt.Sprintf("🔧 工具调用（×%d，%s）", count, action)
	}
	return fmt.Sprintf("🔧 工具调用（暂无，%s）", action)
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

// buildMetaElements 以"紧凑行"呈现底部 meta(v3):
// agent 行 · agent/会话/模型/tokens,runtime 行 · user/ip/workdir,
// developer 行 · 版本/最新版本/开发者模式。三行由独立开关控制显隐,任一开则渲
// 分隔线。空字段跳过,空行不渲染。
func buildMetaElements(meta Meta) []any {
	rows := MetaRows(meta)
	if len(rows) == 0 {
		return nil
	}
	elements := []any{map[string]any{"tag": "hr"}}
	for _, row := range rows {
		elements = append(elements, metaLineElement(row.ElementID, row.Text))
	}
	return elements
}

// MetaRow 是 status bar 里一行渲染的组合:element_id 用于 CardKit 指纹稳定与
// 精确定位,Text 是渲染出的 markdown 文本。空 Text 的行由 MetaRows 直接跳过。
type MetaRow struct {
	ElementID string
	Text      string
}

// MetaRows 组装最多三行 status bar,每行由对应的 ShowMetaRow* 独立开关控制。
// 关闭且行内容为空时不渲染;所有开关都关或所有行内容都空时返回空切片。
// full CardKit 与 lightweight markdown 回复共用此函数,以保证两处 footer 一致。
func MetaRows(meta Meta) []MetaRow {
	var rows []MetaRow
	if meta.ShowMetaRowAgent {
		var parts []string
		if meta.Agent != "" {
			parts = append(parts, agentEmoji(meta.Agent)+" "+shortSessionID(meta.SessionID, meta.Agent))
		}
		if model := metaModelText(meta); model != "" {
			parts = append(parts, "🧠 "+model)
		}
		if tokens := metaTokenText(meta); tokens != "" {
			parts = append(parts, tokens)
		}
		if len(parts) > 0 {
			rows = append(rows, MetaRow{ElementID: "meta_primary", Text: strings.Join(parts, " · ")})
		}
	}
	if meta.ShowMetaRowRuntime {
		var parts []string
		if meta.User != "" {
			parts = append(parts, "👤 "+meta.User)
		}
		if meta.IP != "" {
			parts = append(parts, "🖥️ "+meta.IP)
		}
		if meta.WorkDir != "" {
			parts = append(parts, "📁 `"+meta.WorkDir+"`")
		}
		if len(parts) > 0 {
			rows = append(rows, MetaRow{ElementID: "meta_runtime", Text: strings.Join(parts, " · ")})
		}
	}
	if meta.ShowMetaRowDeveloper {
		if text := metaDeveloperText(meta); text != "" {
			rows = append(rows, MetaRow{ElementID: "meta_developer", Text: text})
		}
	}
	return rows
}

// metaDeveloperText 渲染开发者行内容:<emoji> v<current> · 最新 v<latest>(若可用且不同)。
// 版本号前置的 emoji 同时承担"通道指示"职责:开发者模式(rc 通道)→ 🐛 debug 昆虫,
// 正式版通道 → 🦋 蝴蝶。这样一个 emoji 传达"当前版本 + 我在哪个通道",不需要额外的
// "开发者模式 ✅/❌"段。Version 为空但已知通道时也渲染 emoji + 空版本,让用户至少
// 知道通道状态。
func metaDeveloperText(meta Meta) string {
	emoji := "🦋"
	if meta.DeveloperMode {
		emoji = "🐛"
	}
	var parts []string
	if v := strings.TrimSpace(meta.Version); v != "" {
		parts = append(parts, emoji+" "+v)
	} else {
		parts = append(parts, emoji)
	}
	if latest := strings.TrimSpace(meta.LatestVersion); latest != "" {
		parts = append(parts, "⬆️ 最新 "+latest)
	}
	return strings.Join(parts, " · ")
}

// metaModelText 用实际模型值(缺失显 unknown),effort 以括号后缀呈现。
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
		// 近似值(本轮侧车尚未落盘,沿用同 session 上一次已知占用)前缀 ~,
		// 与实时值区分,但仍显示占用而非误导性的累计流水 token。
		prefix := ""
		if meta.CtxApprox {
			prefix = "~"
		}
		if meta.CtxWindow > 0 {
			return fmt.Sprintf("%s %sctx: %d%% (%s/%s)", dot, prefix, meta.CtxUsedPercent, compactInt(meta.CtxTokens), compactInt(meta.CtxWindow))
		}
		return fmt.Sprintf("%s %sctx: %d%%", dot, prefix, meta.CtxUsedPercent)
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
	if totalTokens > 0 && totalTokens != runTokens {
		return fmt.Sprintf("🔢 tokens: 本轮 %s · 累计 %s", compactInt(runTokens), compactInt(totalTokens))
	}
	return fmt.Sprintf("🔢 tokens: 本轮 %s", compactInt(runTokens))
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

// agentEmoji picks a per-agent icon so the footer distinguishes which agent
// client produced the reply at a glance.
func agentEmoji(agent string) string {
	switch strings.ToLower(agent) {
	case "claude":
		return "🍊"
	case "codex":
		return "⚙️"
	default:
		return "🤖"
	}
}

// shortSessionID returns the first 6 characters of the session id for the
// footer. It falls back to the agent display name when no session id is known
// yet (e.g. before the first turn establishes one).
func shortSessionID(sessionID, agent string) string {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return displayAgent(agent)
	}
	if len(id) > 6 {
		return id[:6]
	}
	return id
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
