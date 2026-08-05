# Codex 与 Claude 流消息展示契约

本文记录 Bridge 对 Claude `stream-json` 和 Codex `exec --json` 的归一化规则，以及三种 Reply mode 的用户可见语义。它是未来升级 Agent CLI、调整卡片布局或改造 parser 时的兼容性基线；原始 JSON 字段不是卡片协议。

## 归一化目标

Bridge 把两个 Agent 的不同传输事件投影为四类内容：最终回答、可见过程进展、原生思考、工具调用。运行期间，最新的完整 assistant 文本会保留在答案区，过程区可同时保留它作为执行记录；终态仍只将最后一条文本归类为最终回答。

| 归一化类别 | 面向用户的含义 | 不显示的内容 |
| --- | --- | --- |
| 最终回答 | 本轮交付的最后一段 assistant 文本 | session id、token 原始字段、协议事件名 |
| 过程进展 | Agent 在继续调用工具前给出的阶段性说明 | private/encrypted chain of thought、未解析的内部 metadata |
| 原生思考 | CLI 明确提供、可安全展示的 `thinking` / `reasoning` 文本 | 需要解密的内容、仅有 ID 的 block |
| 工具调用 | 一次 `tool_use` 或 Codex command 及其摘要/结果 | 原始 tool call id、完整敏感参数 |

工具次数按一次调用去重：Claude 以 `tool_use.id` 为主键，Codex 以 `command_execution.id` 为主键。一个工具的 use 和 result 是同一次调用的两个事件，不能计为两次。

## Claude `stream-json`

Claude 的 assistant message 可能只含文本、只含 `tool_use`，或同时包含二者。Bridge 暂存最近的 assistant 文本：同一条 message 中出现 `tool_use`，或之后继续出现工具活动时，该文本提升为过程进展；流终止时仍处于暂存状态的文本才归为最终回答。

| Claude 原始内容 | Bridge 归类 | 说明 |
| --- | --- | --- |
| assistant `text`，之后有 `tool_use` | 过程进展 | 例如“第一轮检查完成” |
| assistant `text` 与 `tool_use` 同一 message | 过程进展 + 工具调用 | 文本不会同时充当最终回答 |
| 最后一条 assistant `text`，随后 `result` / EOF | 最终回答 | 只在 clean/latest 的中间正文显示 |
| `thinking` / `reasoning` / `redacted_thinking` | 原生思考 | 保持独立折叠区 |
| `tool_use`、`tool_result` | 工具调用 | 通过 id 合并和计数 |
| `content_block_delta` | 增量预览 | 终态以完整快照或最终结果收敛 |

## Codex `exec --json`

Codex 的 `agent_message` 不带“commentary”或“final” phase，不能仅根据事件类型判断。Bridge 收到每一条完整 `agent_message` 时先将其显示为当前答案候选，并暂存它：后续出现 `item.*`、下一条 `agent_message` 或其他活动时，已暂存文本归为过程进展，但不会清空答案区；下一条完整 `agent_message` 才会替换答案区内容。`turn.completed` 到达时，最后一条暂存文本归为最终回答。

| Codex 原始事件 | Bridge 归类 | 说明 |
| --- | --- | --- |
| `item.completed` / `agent_message`，后续仍有活动 | 答案候选 + 过程进展 | 先显示为最新答案，依靠后续时序保留过程记录，不合成推理摘要 |
| `item.completed` / `agent_message`，直到 `turn.completed` | 最终回答 | 本轮最后一条文本 |
| `item.*` / `reasoning` 带可显示 text | 原生思考 | 无 text 的 reasoning 不渲染 |
| `item.started` / `command_execution` | 工具调用 use | id 是调用计数主键 |
| `item.completed` / `command_execution` | 工具调用 result | 与 use 归属同一次调用 |
| `thread.started`、usage、未知事件 | 运行 metadata / audit | 不进入用户正文 |

这是一条必要的启发式约束：如果 Codex CLI 将来为 `agent_message` 提供稳定 phase，parser 可以使用该字段，但必须保持上述用户可见分类和最后回答不丢失的结果。

## Reply mode 展示语义

| 回复模式（兼容 key） | 上方过程区 | 中间正文 | 下方工具区 | 终态保留 |
| --- | --- | --- | --- | --- |
| Coder（`append`） | 不单独展示 | 依事件顺序内联“进展、工具、最终回答” | 不单独展示 | 全部有序内容 |
| Worker（`append-clean-card`） | 独立“思考推理”时间线，最新 2 条 | 运行中显示最新完整消息；终态保留最终回答 | 独立“工具调用”时间线，最新 2 条 | 两个折叠区、完整累计次数、超出数量提示 |
| Singleton（`latest-card`） | 合并过程折叠区 | 运行中显示最新完整消息；终态保留最终回答 | 合并工具折叠区 | 过程与工具次数均保留，不使用 Coder 的最近 2 条时间线 |

Worker 的条目格式是 `🔹 #N · HH:MM:SS` 紧邻内容与分隔线，分隔线上下不插入空白段；折叠标题分别使用 `💭 思考推理` 与 `🔧 工具调用`。超过两条时，对应折叠标题仅提示“仅保留最新 2 条”，正文不重复提示，也不展示较早省略数量。思考和工具必须是两个独立折叠区，最终回答始终位于二者之间。

## 回归 fixture

`internal/bridge/agent_stream_contract_test.go` 使用同一个语义 fixture 覆盖两种原始格式：两轮阶段进展、四次单步工具调用（`pwd`、`git status --short` 各两次）和一个最终总结。它断言：

- 两条阶段文本都进入过程进展，不会成为最终回答；
- 最终总结是唯一的 `AnswerSegments`，且在有序时间线末尾；
- `ToolCallCount` 与有序工具 use 数都是 4；
- Claude 的过程文本来自 `ProgressSegments`，Codex 的过程文本来自按时序判定的 thought 段。

跨 Reply mode 的渲染回归由 `TestClaudeProgressAndToolCountsSurviveEveryReplyMode` 继续覆盖，验证 `append`、`append-clean-card` 与 `latest-card` 不丢过程文本、最终回答或工具次数。修改 parser 或卡片聚合逻辑时，应先扩展这两个 fixture，再改变实现。
