package bridge

import (
	"context"
	"fmt"
	"strings"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

// selfLoopPromptTemplate wraps a bare "improve bridge" requirement with the
// self-loop briefing. %s is the user-supplied requirement text. The agent is
// told to read its own operating guide first so the layered-verification and
// publish boundaries stay authoritative there rather than duplicated here.
const selfLoopPromptTemplate = `收到一个「修复/改进 bridge（lark-ai-agent-bridge）自身」的需求，走 self-loop 流程处理。

先进 ~/bridge/bridge-self-loop 读它的 GUIDE.md（作业手册：bridge 源码位置、L1/L2/L3 三层验证、环境专属工具、边界），然后用你自身能力（Task/Workflow/Edit/Bash + 飞书 skill）自主完成：分析 → 改 ~/bridge/lark-ai-agent-bridge 源码 → 按 GUIDE 分层验证 → 回报结果。除本需求外无需再向用户确认。

需求如下：
%s`

// handleTodoCommand implements the hidden /.go command (absent from /help):
// it hands the argument to the agent as a "improve bridge itself" requirement
// and drives the self-loop workflow via a normal agent run. Admin-only (the
// admin gate is enforced by the dispatcher before we get here) because it lets
// the bridge modify its own source tree. It inherits the effective agent
// preference so /.go does not silently switch between Claude and Codex.
func (s *Service) handleTodoCommand(ctx context.Context, msg Message, cmd Command, preference config.RuntimePreference) error {
	requirement := strings.TrimSpace(cmd.Text)
	if requirement == "" {
		return s.renderTextWithMode("todo", msg.ID, card.SegmentError,
			"用法：`/.go <需求>`（走 self-loop 完善 bridge 自身，需求为要修复/改进的内容）", preference.ConversationMode)
	}
	s.Audit.Record(msg.Sender, "todo_selfloop", msg.ChatID, requirement)
	runCmd := cmd
	runCmd.Type = CommandRun
	runCmd.Text = fmt.Sprintf(selfLoopPromptTemplate, requirement)
	return s.runWithPreference(ctx, runCmd, msg, "", preference)
}
