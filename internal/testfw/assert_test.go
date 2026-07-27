package testfw

import "testing"

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

func TestRunAssert_NoEvents(t *testing.T) {
	out := &SimulateOutput{Events: nil}
	if ok, _ := RunAssert(Assert{Type: "event_type", Expected: "help"}, out); ok {
		t.Error("无 event 时任何断言都应失败")
	}
}
