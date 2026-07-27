package testfw

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// L3ChecklistItem 是一条 L3 人工/半自动验证清单条目。
//
// 设计原则:用例集(YAML)描述"发什么消息,产什么结果",L3 checklist 是把这类
// 描述**投影**到真实飞书链路上的操作手册,不重新造断言语义。同一份用例既能
// 在 simulate 层秒级跑绿(smoke/regression),又能作为 L3 时的手工步骤模板,
// 保证行为契约在两层间不漂移。
type L3ChecklistItem struct {
	// Source 是用例来源(yaml 文件相对路径)。
	Source string
	// CaseName 是 TestCase.Name。
	CaseName string
	// Command 是要发给飞书 Test bot 的消息文本(由 L3 sender 用 --as user 身份发送)。
	// L3 sender 是本机具备 `im:message.send_as_user` scope 的一方,归属按环境不同:
	// Linux 服务器上是 Test bot 的 App client,Mac 上是 supervisor 的 App client。
	// 具体分工见 self-loop GUIDE.md「L3」章节。
	Command string
	// Transport 指明必须投递的会话类型和 mention 方式。L3 不可把 P2P
	// 和群聊混用：后者的 IsGroup/mentioned 会改变 bridge 行为。
	Transport string
	// Assertions 是这条步骤在 L3 期望观察到的可见事实,按"audit key" + "飞书回读"分列。
	// L3 断言不重造 assert.go 的语义,只挑用户/审计员能亲眼看到的:
	//   - audit: cardkit_create / cardkit_reply / cardkit_update 等 action 名
	//   - visible: 卡片正文/标题包含的关键词
	Assertions []string
}

// BuildL3Checklist 从测试用例集里挑出适合 L3 复验的**消息触发用例**,
// 产出一份可读的清单。选择规则:
//   - 有 input(消息触发)且未显式标记 l3.skip。
//   - action 型 step 跳过(卡片按钮点击 L3 用消息路径的等价 /action 覆盖,不能凭空点)。
//
// 目的是让 supervisor 到 L3 阶段有一份**行为对齐 simulate 用例**的可执行清单,
// 不用再凭直觉挑要发什么消息。
func BuildL3Checklist(cases []TestCase, sourceHint map[string]string) []L3ChecklistItem {
	items := make([]L3ChecklistItem, 0)
	for _, tc := range cases {
		src := sourceHint[tc.Name]
		for i, step := range tc.Steps {
			if step.Input == "" || (step.L3 != nil && step.L3.Skip) {
				continue // action 型 step 跳过
			}
			item := L3ChecklistItem{
				Source:   src,
				CaseName: fmt.Sprintf("%s [step %d]", tc.Name, i+1),
				Command:  step.Input,
			}
			// 将 YAML 的群聊上下文显式投影出来。simulate 的 group=true 默认
			// 代表已 @；真实 L3 必须真的发到 group chat 并构造 mention。
			mentionedDefault := true
			if step.Mentioned != nil {
				mentionedDefault = *step.Mentioned
			}
			switch {
			case !step.Group:
				item.Transport = "P2P（不 @）"
			case mentionedDefault:
				item.Transport = "群聊（@ Test）"
			default:
				item.Transport = "群聊（不 @ Test）"
			}
			if !step.Group || mentionedDefault {
				item.Assertions = append(item.Assertions, "audit: cardkit_create + cardkit_reply")
			} else {
				item.Assertions = append(item.Assertions, "audit: 无 cardkit_create/cardkit_reply（mention_required）")
			}
			// 从 simulate 层的 assert 提取用户可见的文案关键词作为 L3 回读断言。
			for _, a := range step.Asserts {
				if a.Type == "segment_contains" && a.Text != "" {
					item.Assertions = append(item.Assertions, "visible: 卡片正文含 \""+a.Text+"\"")
				}
				if a.Type == "no_events" {
					item.Assertions = append(item.Assertions, "visible: 无机器人卡片回复")
				}
			}
			if len(item.Assertions) == 0 {
				item.Assertions = append(item.Assertions, "visible: 卡片正常回复(无错误段)")
			}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CaseName < items[j].CaseName })
	return items
}

// WriteL3Checklist 把 checklist 写成 Markdown,固定格式便于 supervisor 逐条勾选。
// L3 由 supervisor 编排、由本机 L3 sender(具备 im:message.send_as_user scope
// 的一方)使用飞书能力(lark-im 等 skill)执行,本文件是操作手册,不是自动化
// 脚本——真正的自动化需要 lark-cli 认证与 chat_id 等具体环境,那属于每台机器
// 的私有配置,不进入通用测试框架。
func WriteL3Checklist(items []L3ChecklistItem, path string) error {
	var b strings.Builder
	b.WriteString("# Bridge L3 E2E Checklist\n\n")
	b.WriteString("由 L3 sender（本机具备 `im:message.send_as_user` 权限的一方；Linux 上是 Test，Mac 上是 supervisor）用 `lark-cli im +messages-send --as user` 按「通道」列逐条发送，\n")
	b.WriteString("再用 `+message-mget` 或 audit.jsonl 回读断言。每条命中即 L3 通过。\n\n")
	b.WriteString("> ⚠️ `P2P` 条目必须发到 Test 的私聊，不能 @；`群聊（@ Test）` 条目必须发到 chat_mode=group 的测试群，并 @ 动态核验的 Test open_id。\n")
	b.WriteString("> ⚠️ `群聊（不 @ Test）` 是负向验收：发到同一测试群但不能 @，并断言无卡片。目标 chat_mode 不匹配时停止，不得把结果记为通过。\n")
	b.WriteString("> ⚠️ 换血 Test 用 rebuild-test.sh <worktree>,supervisor 全程不动。\n\n")
	b.WriteString(fmt.Sprintf("共 %d 条待验条目。\n\n", len(items)))
	b.WriteString("| # | 用例 | 通道 | 消息 | L3 断言 |\n")
	b.WriteString("| - | ---- | ---- | ---- | ------- |\n")
	for i, it := range items {
		asserts := strings.Join(it.Assertions, " · ")
		// 转义 | 避免破坏表格
		safeCmd := strings.ReplaceAll(it.Command, "|", "\\|")
		safeAsserts := strings.ReplaceAll(asserts, "|", "\\|")
		b.WriteString(fmt.Sprintf("| %d | %s | %s | `%s` | %s |\n", i+1, it.CaseName, it.Transport, safeCmd, safeAsserts))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
