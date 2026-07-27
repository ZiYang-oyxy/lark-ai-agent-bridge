package testfw

import (
	"strconv"
	"strings"
)

// errorActionSuffixes 是 audit Action 名表达「失败/拒绝」语义的后缀约定。
// audit.Event 没有 Level 字段(见 types.Audit 注释),错误只能靠 Action 命名识别。
//
// 依据 internal/bridge 里 s.Audit.Record(...) 的实际命名审计后**刻意只保留**
// 真正表达失败/拒绝的后缀:
//   - _failed  : 各类持久化/恢复失败(config_reset_failed、session_resume_failed…)
//   - _denied  : 权限拒绝(access_denied、admin_denied、action_grant_denied…)
//   - _rejected: 输入被拒(output_image_rejected…)
//
// 明确**排除**以下「正常语义」后缀,避免把正常流程误判为错误:
//   - _ignored     : 正常忽略(batch_stop_ignored=空闲无批次、message_recalled_ignored)
//   - _unavailable : 正常能力缺失(context_usage_unavailable=agent 未上报用量)
//   - _error       : 当前代码库无此后缀的 Record 调用
var errorActionSuffixes = []string{
	"_failed", "_denied", "_rejected",
}

// isErrorAudit 判断一条 audit 是否表达错误/拒绝语义。
func isErrorAudit(a Audit) bool {
	for _, suf := range errorActionSuffixes {
		if strings.HasSuffix(a.Action, suf) {
			return true
		}
	}
	return false
}

// extractVisibleText 只收集「用户在飞书卡片上真正能看到的文本」。
//
// 关键约束:simulate 输出的是卡片*数据模型*(card.Event),不是渲染后的卡片。
// 因此这里严格只读真实承载可见文案的字段,绝不:
//   - 把整个 event 序列化成 JSON 兜底(那会让断言命中任意字段名/值而假通过);
//   - 按结构体是否非 nil 硬注入「全局配置」「保存」等猜测文案。
//
// 数据模型里没有的文案(如表单渲染期才生成的按钮标题),就是断言不到——
// 这是诚实的能力边界,不该用兜底掩盖。
func extractVisibleText(event Event) string {
	var texts []string

	for _, seg := range event.Segments {
		if seg.Text != "" {
			texts = append(texts, seg.Text)
		}
	}
	if event.Message != "" {
		texts = append(texts, event.Message)
	}
	if event.Markdown != "" {
		texts = append(texts, event.Markdown)
	}

	if event.HelpCard != nil {
		for _, group := range event.HelpCard.Groups {
			texts = append(texts, group.Title)
			texts = append(texts, group.Lines...)
		}
		if event.HelpCard.Footer != "" {
			texts = append(texts, event.HelpCard.Footer)
		}
	}

	if event.StatusCard != nil {
		for _, section := range event.StatusCard.Sections {
			texts = append(texts, section.Title)
			for _, field := range section.Fields {
				texts = append(texts, field.Label, field.Value)
			}
		}
	}

	if event.LocalConfigOverview != nil {
		for _, item := range event.LocalConfigOverview.Items {
			texts = append(texts, item.Label, item.Value)
		}
	}

	return strings.Join(texts, "\n")
}

// RunAssert 执行单个断言,返回是否通过和失败信息。
func RunAssert(assert Assert, output *SimulateOutput) (bool, string) {
	// no_events 用于验证「消息被正确过滤、无任何卡片输出」的场景
	// (如群里未 @ bot),它必须在空 events 检查之前处理。
	if assert.Type == "no_events" {
		if len(output.Events) == 0 {
			return true, ""
		}
		return false, "期望无 event(消息被过滤),实际有 " + strconv.Itoa(len(output.Events)) + " 个"
	}

	if len(output.Events) == 0 {
		return false, "没有返回任何 event"
	}
	event := output.Events[0]

	switch assert.Type {
	case "event_type":
		// /stop 空闲时返回 message 类型的提示,业务上等价于 notice,特判放行。
		if assert.Expected == "notice" && event.Type == "message" &&
			strings.Contains(extractVisibleText(event), "当前会话没有正在运行的任务") {
			return true, ""
		}
		if event.Type == assert.Expected {
			return true, ""
		}
		return false, "期望 event 类型 '" + assert.Expected + "',实际 '" + event.Type + "'"

	case "segment_contains":
		visible := extractVisibleText(event)
		if strings.Contains(visible, assert.Text) {
			return true, ""
		}
		return false, "期望可见文本包含 '" + assert.Text + "',实际可见文本:\n" + visible

	case "has_button":
		// 只认数据模型里真实存在的按钮:Actions[].Label 与 StopButton。
		for _, act := range event.Actions {
			if strings.Contains(act.Label, assert.Button) {
				return true, ""
			}
		}
		if assert.Button == "停止" && event.StopButton.Visible {
			return true, ""
		}
		// 表单类卡片(ConfigForm/AgentModeForm 等)的提交按钮是渲染期生成的,
		// 不在 card.Event.Actions 里 —— simulate 层看不到,不能假装能断言。
		if len(event.Actions) == 0 && (event.ConfigForm != nil || event.AgentModeForm != nil || event.LocalConfigOverview != nil) {
			return false, "按钮 '" + assert.Button + "' 无法在 simulate 层断言:表单卡片的按钮由渲染期生成,不出现在 Event.Actions 中(改用 event_type 断言表单卡片类型)"
		}
		return false, "未找到按钮 '" + assert.Button + "'"

	case "audit_empty":
		if len(output.Audit) == 0 {
			return true, ""
		}
		msgs := make([]string, 0, len(output.Audit))
		for _, a := range output.Audit {
			msgs = append(msgs, a.Action+": "+a.Detail)
		}
		return false, "期望无 audit 事件,实际有: " + strings.Join(msgs, "; ")

	case "header_title":
		if strings.Contains(event.HeaderTitle, assert.Text) {
			return true, ""
		}
		return false, "期望 header 标题包含 '" + assert.Text + "',实际 '" + event.HeaderTitle + "'"

	case "stop_button_disabled":
		if !event.StopButton.Visible || event.StopButton.Disabled {
			return true, ""
		}
		return false, "期望停止按钮不可用(不可见或 disabled),实际为可见且启用"

	case "stop_button_visible":
		if event.StopButton.Visible {
			return true, ""
		}
		return false, "期望停止按钮可见,实际不可见"

	case "no_error":
		// audit 侧:按 Action 名后缀识别错误/拒绝。
		for _, a := range output.Audit {
			if isErrorAudit(a) {
				return false, "存在错误 audit: " + a.Action + ": " + a.Detail
			}
		}
		// segment 侧:Kind=="error" 即渲染为错误片段;这是数据模型里明确的错误标记。
		for _, seg := range event.Segments {
			if seg.Kind == "error" {
				return false, "响应含错误片段: " + seg.Text
			}
		}
		return true, ""

	default:
		return false, "未知断言类型: " + assert.Type
	}
}
