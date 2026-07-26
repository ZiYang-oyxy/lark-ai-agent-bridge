package bridge

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/card"
)

// 两轮、四次单步命令是 UI 回归的最小代表场景：前两条 assistant 文本是
// 过程进展，最后一条才是用户应看到的最终总结。
func TestAgentStreamContractTwoRoundsFourTools(t *testing.T) {
	t.Run("Claude stream-json", func(t *testing.T) {
		result, err := parseClaudeStream(strings.NewReader(twoRoundClaudeStreamFixture()), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertTwoRoundAgentContract(t, result)
		if got := result.ProgressSegments; !equalStrings(got, []string{"第一轮检查完成", "第二轮检查完成"}) {
			t.Fatalf("progress segments = %#v", got)
		}
	})

	t.Run("Codex exec JSON", func(t *testing.T) {
		result, err := parseCodexStream(strings.NewReader(twoRoundCodexStreamFixture()), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertTwoRoundAgentContract(t, result)
		if got := thoughtTexts(result.OrderedSegments); !equalStrings(got, []string{"第一轮检查完成", "第二轮检查完成"}) {
			t.Fatalf("thought timeline = %#v", got)
		}
	})
}

func assertTwoRoundAgentContract(t *testing.T, result AgentRunResult) {
	t.Helper()
	if got := result.AnswerSegments; !equalStrings(got, []string{"两轮全部完成，工作区状态正常。"}) {
		t.Fatalf("final answer segments = %#v", got)
	}
	if result.ToolCallCount != 4 {
		t.Fatalf("tool call count = %d, want 4", result.ToolCallCount)
	}
	if got := toolUseCount(result.OrderedSegments); got != 4 {
		t.Fatalf("ordered tool uses = %d, want 4; segments = %#v", got, result.OrderedSegments)
	}
	if got := lastTextSegment(result.OrderedSegments); got != "两轮全部完成，工作区状态正常。" {
		t.Fatalf("last ordered text = %q", got)
	}
}

func twoRoundClaudeStreamFixture() string {
	return strings.Join([]string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"第一轮检查完成"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"claude-1","input":{"command":"pwd"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"claude-1","content":"/repo"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"claude-2","input":{"command":"git status --short"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"claude-2","content":""}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"第二轮检查完成"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"claude-3","input":{"command":"pwd"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"claude-3","content":"/repo"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"claude-4","input":{"command":"git status --short"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"claude-4","content":""}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"两轮全部完成，工作区状态正常。"}]}}`,
		`{"type":"result","result":"两轮全部完成，工作区状态正常。"}`,
	}, "\n")
}

func twoRoundCodexStreamFixture() string {
	return strings.Join([]string{
		`{"type":"thread.started","thread_id":"contract-thread"}`,
		`{"type":"item.completed","item":{"id":"msg-1","type":"agent_message","text":"第一轮检查完成"}}`,
		`{"type":"item.started","item":{"id":"codex-1","type":"command_execution","command":"pwd"}}`,
		`{"type":"item.completed","item":{"id":"codex-1","type":"command_execution","output":"/repo\\n","exit_code":0}}`,
		`{"type":"item.started","item":{"id":"codex-2","type":"command_execution","command":"git status --short"}}`,
		`{"type":"item.completed","item":{"id":"codex-2","type":"command_execution","output":"","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"msg-2","type":"agent_message","text":"第二轮检查完成"}}`,
		`{"type":"item.started","item":{"id":"codex-3","type":"command_execution","command":"pwd"}}`,
		`{"type":"item.completed","item":{"id":"codex-3","type":"command_execution","output":"/repo\\n","exit_code":0}}`,
		`{"type":"item.started","item":{"id":"codex-4","type":"command_execution","command":"git status --short"}}`,
		`{"type":"item.completed","item":{"id":"codex-4","type":"command_execution","output":"","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"msg-3","type":"agent_message","text":"两轮全部完成，工作区状态正常。"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func thoughtTexts(segments []card.Segment) []string {
	var texts []string
	for _, segment := range segments {
		if segment.Kind == card.SegmentThought {
			texts = append(texts, segment.Text)
		}
	}
	return texts
}

func toolUseCount(segments []card.Segment) int {
	count := 0
	for _, segment := range segments {
		if segment.Kind == card.SegmentTool && segment.Tool != nil && segment.Tool.Phase == "use" {
			count++
		}
	}
	return count
}

func lastTextSegment(segments []card.Segment) string {
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i].Kind == card.SegmentText {
			return segments[i].Text
		}
	}
	return ""
}
