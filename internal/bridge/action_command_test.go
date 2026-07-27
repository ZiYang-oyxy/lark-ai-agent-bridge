package bridge

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/agent"
)

// 固化 /action 对等命令的解析与「可消息触发」白名单契约。

func TestParseActionCommand(t *testing.T) {
	cases := []struct {
		text      string
		wantType  CommandType
		wantID    string
		wantValue string
	}{
		{"/action stop", CommandAction, "stop", ""},
		{"/action schedule.confirm draft-123", CommandAction, "schedule.confirm", "draft-123"},
		{"/action update.install v0.1.5", CommandAction, "update.install", "v0.1.5"},
		{"/action", CommandUnknown, "", ""}, // 无 action id → 用法提示
	}
	for _, c := range cases {
		cmd := ParseCommand(Message{Text: c.text}, agent.Claude)
		if cmd.Type != c.wantType {
			t.Errorf("ParseCommand(%q).Type = %q, want %q", c.text, cmd.Type, c.wantType)
			continue
		}
		if c.wantType == CommandAction {
			if cmd.ActionID != c.wantID || cmd.ActionValue != c.wantValue {
				t.Errorf("ParseCommand(%q) = ID:%q Value:%q, want ID:%q Value:%q",
					c.text, cmd.ActionID, cmd.ActionValue, c.wantID, c.wantValue)
			}
		}
	}
}

func TestActionCommandHiddenFromHelp(t *testing.T) {
	// /action 是隐藏命令,不应出现在 /help 卡片里。
	for _, group := range HelpCardData().Groups {
		for _, line := range group.Lines {
			if strings.Contains(line, "/action") {
				t.Errorf("/action 不应出现在 /help: %q", line)
			}
		}
	}
}

func TestMessageTriggerableActionWhitelist(t *testing.T) {
	// 白名单:经实测确认无渲染上下文时也能正确工作的动作。
	triggerable := []string{
		"stop", "schedule.confirm", "schedule.cancel",
		"update.install", "update.details", "update.help", "help.refresh",
	}
	for _, a := range triggerable {
		if !messageTriggerableAction(a) {
			t.Errorf("%q 应在可消息触发白名单内", a)
		}
	}

	// 黑名单:依赖表单值 / 卡片渲染期上下文,消息触发本无法提供,必须被拒。
	rejected := []string{
		"config.save", "local_config.save", "agent_mode.save", // 依赖 FormValues
		"config.close",                     // 依赖 OpenMessageID
		"create_workdir", "cancel_workdir", // 依赖 pendingRun
		"resume.select",                                             // 依赖 resumeContext
		"help.open_config", "help.open_local_config", "help.status", // 依赖 helpContext
		"status.refresh", "local_config.reset", // 依赖 statusContext/helpContext
		"bogus.unknown", "", // 未知/空 → 白名单默认拒绝
	}
	for _, a := range rejected {
		if messageTriggerableAction(a) {
			t.Errorf("%q 不应可消息触发", a)
		}
		// 每个被拒动作都应有非空的用户可读原因。
		if messageTriggerRejectReason(a) == "" {
			t.Errorf("%q 缺少拒绝原因文案", a)
		}
	}
}
