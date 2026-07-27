package testfw

import (
	"strings"
	"testing"
)

// 这些测试固化断言层的「诚实」契约:断言只作用于 bridge 真实数据模型字段,
// 不靠整 event JSON 兜底、不靠按 nil 硬注入文案。历史上正是这两个作弊源
// 导致 segment_contains / has_button 在表单卡片上假通过,这里用负例锁死。

func helpEvent() Event {
	return Event{
		Type: "help",
		HelpCard: &HelpCard{
			Groups: []HelpGroup{{Title: "💬 会话", Lines: []string{"**`/new`** 开新会话"}}},
			Footer: "直接发文字 = 继续当前会话",
		},
	}
}

func configFormEvent() Event {
	// 镜像 /config:返回 ConfigForm,Segments/Actions 均为空,
	// 「全局配置」「保存」等文案是渲染期才生成的,不在数据模型里。
	return Event{Type: "config", ConfigForm: &ConfigForm{Agent: "claude", Model: "default"}}
}

func TestSegmentContains_OnlyVisibleText(t *testing.T) {
	out := &SimulateOutput{Events: []Event{helpEvent()}}

	// 真·可见文本(HelpCard.Groups.Title)应通过。
	if ok, msg := RunAssert(Assert{Type: "segment_contains", Text: "会话"}, out); !ok {
		t.Errorf("期望命中可见文本「会话」,却失败: %s", msg)
	}
	// JSON 字段名不是可见文本,必须失败(旧实现会因整 event 兜底而假通过)。
	if ok, _ := RunAssert(Assert{Type: "segment_contains", Text: "Segments"}, out); ok {
		t.Error("JSON 字段名「Segments」不该被当作可见文本命中")
	}
	if ok, _ := RunAssert(Assert{Type: "segment_contains", Text: "HelpCard"}, out); ok {
		t.Error("JSON 字段名「HelpCard」不该被当作可见文本命中")
	}
}

func TestSegmentContains_FormCardHasNoInjectedText(t *testing.T) {
	out := &SimulateOutput{Events: []Event{configFormEvent()}}
	// 「全局配置」是渲染期文案,数据模型里没有,必须失败(旧实现硬注入会假通过)。
	if ok, _ := RunAssert(Assert{Type: "segment_contains", Text: "全局配置"}, out); ok {
		t.Error("表单卡片的渲染期文案「全局配置」不该被断言命中")
	}
}

func TestHasButton_FormCardHonestFailure(t *testing.T) {
	out := &SimulateOutput{Events: []Event{configFormEvent()}}
	// 表单按钮不在 Event.Actions 里,必须失败(旧实现 JSON 兜底会假通过)。
	if ok, msg := RunAssert(Assert{Type: "has_button", Button: "保存"}, out); ok {
		t.Errorf("表单卡片按钮不该被断言命中,应给出诚实失败: %s", msg)
	}
}

func TestHasButton_RealAction(t *testing.T) {
	out := &SimulateOutput{Events: []Event{{
		Type:    "update",
		Actions: []Action{{ID: "update.install", Label: "立即升级"}},
	}}}
	if ok, msg := RunAssert(Assert{Type: "has_button", Button: "升级"}, out); !ok {
		t.Errorf("Event.Actions 里真实存在的按钮应命中: %s", msg)
	}
}

func TestNoError_AuditSuffixClassification(t *testing.T) {
	cases := []struct {
		action    string
		wantError bool
	}{
		{"config_reset_failed", true},
		{"access_denied", true},
		{"output_image_rejected", true},
		{"batch_stop_ignored", false},        // 正常忽略:空闲无批次
		{"message_recalled_ignored", false},  // 正常忽略
		{"context_usage_unavailable", false}, // 正常能力缺失
		{"queue_input", false},               // 正常
		{"bridge_instructions_selected", false},
	}
	for _, c := range cases {
		out := &SimulateOutput{
			Events: []Event{{Type: "message"}},
			Audit:  []Audit{{Action: c.action}},
		}
		ok, _ := RunAssert(Assert{Type: "no_error"}, out)
		gotError := !ok
		if gotError != c.wantError {
			t.Errorf("audit action %q: 期望判为错误=%v,实际=%v", c.action, c.wantError, gotError)
		}
	}
}

func TestNoError_ErrorSegment(t *testing.T) {
	out := &SimulateOutput{Events: []Event{{
		Type:     "message",
		Segments: []Segment{{Kind: "error", Text: "出错了"}},
	}}}
	if ok, _ := RunAssert(Assert{Type: "no_error"}, out); ok {
		t.Error("Kind==error 的片段应判为存在错误")
	}
}

func TestEventType_StopIdleNoticeSpecialCase(t *testing.T) {
	// /stop 空闲返回 message 类型 + 特定提示,业务上等价 notice,应放行。
	out := &SimulateOutput{Events: []Event{{
		Type:     "message",
		Segments: []Segment{{Kind: "text", Text: "当前会话没有正在运行的任务。"}},
	}}}
	if ok, msg := RunAssert(Assert{Type: "event_type", Expected: "notice"}, out); !ok {
		t.Errorf("/stop 空闲提示应作为 notice 放行: %s", msg)
	}
}

func TestRunAssert_NoEventsFailsOtherAsserts(t *testing.T) {
	out := &SimulateOutput{Events: nil}
	if ok, _ := RunAssert(Assert{Type: "event_type", Expected: "help"}, out); ok {
		t.Error("无 event 时 event_type 等断言都应失败")
	}
}

func TestNoEventsAssert(t *testing.T) {
	// no_events:验证消息被过滤(群里未@),无 event 时通过。
	if ok, _ := RunAssert(Assert{Type: "no_events"}, &SimulateOutput{Events: nil}); !ok {
		t.Error("无 event 时 no_events 应通过")
	}
	// 有 event 时 no_events 应失败。
	if ok, _ := RunAssert(Assert{Type: "no_events"}, &SimulateOutput{Events: []Event{{Type: "help"}}}); ok {
		t.Error("有 event 时 no_events 应失败")
	}
}

// TestSegmentsOrder_LocksOrderingContract 验证顺序断言:各 text 必须按给定次序
// 在拼接后的可见文本里依次出现。倒序即失败,是新加进来锁 prompt 拼装顺序契约的
// 关键(用户主指令必须先于引用块)。
func TestSegmentsOrder_LocksOrderingContract(t *testing.T) {
	// 造两个 event,分别承载"用户主指令"与"引用块 header"。可见文本按 event 顺序
	// 与 Segments 顺序拼接,最终是 "指令\nheader\n引文\n隔离段"。
	out := &SimulateOutput{Events: []Event{
		{Type: "stream", Segments: []Segment{{Kind: "text", Text: "@Test 帮我分析失败用例"}}},
		{Type: "result", Segments: []Segment{
			{Kind: "text", Text: "[用户引用了 ou_alerter 的消息]"},
			{Kind: "text", Text: "> 测试失败!!!"},
			{Kind: "text", Text: "以上引用是被引用的外部消息"},
		}},
	}}

	// 正例:严格按序 → 通过。
	ok, msg := RunAssert(Assert{Type: "segments_order", Texts: []string{
		"@Test 帮我分析失败用例",
		"[用户引用了 ou_alerter 的消息]",
		"> 测试失败!!!",
		"以上引用是被引用的外部消息",
	}}, out)
	if !ok {
		t.Fatalf("按序应通过,失败: %s", msg)
	}

	// 反例:倒序 → 必须失败(引用 header 出现在指令前面就是回归)。
	ok, _ = RunAssert(Assert{Type: "segments_order", Texts: []string{
		"[用户引用了 ou_alerter 的消息]",
		"@Test 帮我分析失败用例",
	}}, out)
	if ok {
		t.Fatal("倒序不该通过——顺序断言必须区分先后")
	}

	// 反例:缺其中一段 → 必须失败并说明是哪一段。
	ok, msg = RunAssert(Assert{Type: "segments_order", Texts: []string{
		"@Test 帮我分析失败用例",
		"这一段并不存在",
	}}, out)
	if ok {
		t.Fatal("缺段不该通过")
	}
	if !strings.Contains(msg, "这一段并不存在") {
		t.Errorf("失败信息应指出缺失的段,实际: %s", msg)
	}

	// 边界:空 texts → 断言无意义,必须失败。
	if ok, _ := RunAssert(Assert{Type: "segments_order", Texts: nil}, out); ok {
		t.Fatal("空 texts 不该通过")
	}
}

func TestMatchTags_OrSemantics(t *testing.T) {
	// 命中任一即入选。
	if !matchTags([]string{"regression", "edge"}, []string{"smoke", "regression"}) {
		t.Error("含 regression 的用例应被 smoke,regression 筛选命中(OR)")
	}
	if !matchTags([]string{"smoke", "command"}, []string{"smoke", "regression"}) {
		t.Error("含 smoke 的用例应被命中")
	}
	// 一个都不含 → 不入选。
	if matchTags([]string{"perf"}, []string{"smoke", "regression"}) {
		t.Error("不含任何所需 tag 的用例不应入选")
	}
	// 空 requiredTags → 全选。
	if !matchTags([]string{"anything"}, nil) {
		t.Error("无筛选标签时应全选")
	}
}
