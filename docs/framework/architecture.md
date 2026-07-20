# Feishu AI Agent Bridge Architecture

## 目标

本项目实现一个独立的 Feishu/Lark AI agent bridge。它把飞书聊天、话题和 CardKit 按钮映射到 Claude Code 或 Codex one-shot 调用，让用户在飞书中发起任务、查看执行状态、阅读结果，并在需要时停止本轮执行。

`reference/lark-agent-workspace/` 只是归档参考，不是本 bridge 的接口约束。

## 核心模型

bridge 不托管交互式终端，也不通过 tmux/PTY 捕获输出。每个已调度 batch 形成一次所选 Agent 子进程调用；一个 batch 可包含一条或多条经 debounce 判定兼容的飞书输入：

- 启动命令：`claude -p --output-format stream-json --verbose --include-partial-messages --dangerously-skip-permissions --effort low <prompt>`
- Codex 新会话使用 `codex exec --json [--image <path> ...] [--] -`，续接使用 `codex exec resume --json [--image <path> ...] <thread-id> -`；prompt 写入 stdin。
- Bridge 不向 Codex 传 model、effort、sandbox、approval、profile、plugins、MCP、rules 或 git-check 参数，这些全部由所选 executable 和环境决定。
- 子进程 `cmd.Dir` 和 `PWD` 都设置为本轮请求解析出的工作目录；没有 `--workdir` 时使用 `--default-workdir` 或环境默认目录。
- 如果当前 conversation scope 已保存 agent session id，后续消息使用对应 CLI 的 resume 协议续接。
- `/new` 会清空当前 conversation scope 保存的 agent session id，并从新会话开始。
- 执行开始时创建“执行中”卡片，读取 Claude `stream-json` stdout 时增量更新同一张卡片。
- 执行中卡片带一次性“停止”按钮，点击后取消当前 Agent 子进程，同一卡片进入灰色“已停止”状态并置灰按钮。
- 完成、失败和停止后的卡片仍保留灰色 disabled 按钮，文案分别是“已完成”“已结束”“已停止”，避免用户误以为还能继续点击停止。

## 包结构

- `cmd/lark-agent-bridge`：CLI 入口，包含 `doctor`、`simulate`、`simulate-action`、`serve`
- `internal/agent`：Claude/Codex one-shot 命令构造
- `internal/session`：会话 key、状态、队列、agent session id、prompt 历史
- `internal/bridge`：消息命令解析、工作目录确认、队列、Agent runner/JSONL parser、CardStream 状态聚合、action 处理
- `internal/card`：卡片事件抽象、富文本 segment、fake renderer、长度控制、CardKit 2.0 JSON builder
- `internal/feishu`：飞书消息模型、SDK 长连接、sender/reaction 接口、消息归一化、CardKit HTTP client、CardKit renderer、`card.action.trigger` action 解析
- `internal/audit`：审计事件记录
- `internal/security`：敏感信息脱敏
- `internal/doctor`：就绪检查

`serve` 默认把审计事件写入 `<workdir>/.lark-agent-bridge/audit.jsonl`，也可以通过 `E2E_AUDIT_LOG` 覆盖路径。审计写入前会调用脱敏逻辑，避免 token、secret、password 等敏感值落盘。

## 命令面

当前飞书命令只保留：

- `/new [--workdir <path>] [prompt]`：重置当前 chat/topic 的 Agent 会话；有 prompt 时立即执行，没有 prompt 时只创建 ready 状态。
- `/status`：查看当前 chat/topic 会话状态；群聊中会额外显示当前群内已知会话数量。
- `/config`：配置 agent、agent home、agent bin、model、effort、Reply mode 和 Conversation mode；`/config reset` 恢复环境默认。
- `/help`：显示帮助。

暂不实现 `/resume`。`/sessions`、`/history`、`/topic`、`/attach`、`/interrupt`、`/stop` 文本命令以及 `/claude`、`/codex` 旧入口均不属于当前范围。

群聊默认只处理 @ 机器人的消息；单聊默认处理全部文本。

## 会话 Key 与并发

Conversation mode 决定会话 key 和飞书回复位置：

- `chat`（默认）：key 为 `{Agent, ChatID}`，忽略入站 `ThreadID`，回复显式使用 `reply_in_thread=false`。
- `topic`：key 为 `{Agent, ChatID, ThreadID?}`，非空 thread/topic id 进入 key，回复显式使用 `reply_in_thread=true`。

`E2E_CONVERSATION_MODE=chat|topic` 提供环境默认；`/config` override 持久化到 `preferences.json`。mode 切换只影响保存成功后接收的新消息，不迁移或删除旧 session。input 入队时冻结 mode，pending workdir 确认也保存当时 preference，因此排队或等待按钮期间的配置变化不会改变该输入的回复位置。

并发规则：

- 同一 conversation scope 内串行执行。
- 执行中收到同一 scope 的新输入时进入持久化队列，并产生 `reaction` 事件提示已排队；workdir、model、effort 或 Conversation mode 不同的连续输入不能合入同一 batch。
- `topic` 模式下不同 topic 使用不同 key，可以并行运行各自的 Agent 子进程；`chat` 模式下同一 chat 只有一个串行 scope。
- 普通文本续接当前 mode 解析出的 scope；它不会隐式创建 session boundary，因此同一 debounce cohort 可以合并。
- 只有显式 `/new` 会重置当前 scope 的 agent session，并作为独占 batch boundary。
- 队列上限按单一 scope 的 pending input 计算，满时拒绝新输入且不写入去重 receipt。
- 个人版不设置全局 semaphore、跨 scope FIFO 或公平性调度；唯一的顺序保证是同一 scope 串行，不同 scope 可并行。

## Claude 输出解析

Claude runner 使用 `--output-format stream-json`，通过 stdout pipe 逐行读取 JSONL 事件。每个事件会同时进入两条路径：

- 增量路径：实时更新当前 CardStream 状态，并按 `E2E_CARD_UPDATE_MS` 节流刷新同一张 CardKit 卡片。
- 最终路径：在进程退出时汇总最终正文、思考、工具、model、token 和 Claude session id，写回内存会话状态。

当前从 JSONL 事件中提取：

- 正文：`text` block 或 `result` 字段
- 增量正文：`content_block_delta` 中的 `text_delta`
- 思考过程：`thinking`、`reasoning`、`redacted_thinking` block，以及 `thinking_delta`、`reasoning_delta`、`reasoning_content` 等增量字段
- 工具调用：`tool_use`、`tool_result` block，以及 `input_json_delta` 等工具输入增量
- model：顶层或 message 内的 `model`
- token 数：递归统计 usage 中以 `tokens` 结尾的数字字段
- Claude session id：顶层或 message 内的 `session_id`

输出会映射为 `card.Segment`，CardKit 卡片中正文直接展示，思考过程和工具调用放入折叠面板。

## Codex 输出解析

Codex runner 逐行解析 `exec --json` 输出：`thread.started` 保存 thread id，`item.*` 映射回答、reasoning 和 command 工具段，`turn.completed` 统计 token 并收敛终态，`turn.failed` 转为错误。未知事件和协议异常会记录 audit，用于发现 CLI 升级带来的协议漂移。

## 飞书卡片职责

CardKit 卡片负责展示一次 Claude 请求的状态：

- 执行中：蓝色 header，标题显示当前活动和耗时，例如 `🧠 正在推理 · ⏱ 12s`、`🛠️ 正在执行工具 · ⏱ 18s`、`✍️ 正在回复 · ⏱ 24s`，显示“停止”按钮。
- 结果：绿色 header，标题显示 `✅ 已完成 · ⏱ X`，展示正文、折叠思考过程、折叠工具调用，停止按钮变为灰色 disabled “已完成”。
- 停止：灰色 header，标题显示 `⏹ 已停止 · ⏱ X`，同一卡片按钮置灰为“已停止”。
- 错误：红色 header，标题显示 `❌ 执行失败 · ⏱ X`，展示错误内容，停止按钮变为灰色 disabled “已结束”。
- 工作目录确认：当 `--workdir` 不存在时提供“Create directory”和“Cancel”按钮；创建成功后确认卡变为绿色终态并禁用按钮，取消后变为灰色终态并禁用按钮。创建成功后 Claude 执行使用独立运行卡片，不覆盖确认卡片。
- 底部状态栏：先用分割线与正文和折叠面板隔开，再使用两行 CardKit `column_set` 分栏展示。
- 底部第一行：agent、model、tokens 三列，权重比例 `10:14:18`，tokens 格式为 `🔢 tokens: ▶ 本轮 / ∑ 累计`。
- 底部第二行：user、ip、workdir 三列，权重比例 `10:12:30`，emoji 分别为 `👤`、`🖥️`、`📁`。
- 底部不展示 status，也不使用 `agent=`、`model=`、`workdir=` 这类机器前缀。

长输出先由 `E2E_CARD_MAX_CHARS` 按 rune 截断，再经过 `internal/card.PrepareLarkCard` 的容量管线。该管线以最终 JSON 字节数为准，固定限制为 **28 KiB** 和 **200 个带 `tag` 的组件**；`PreparedLarkCard` 是不透明值，renderer、同步 callback 和 CardKit HTTP client 只能消费其已验证 JSON，不能重新组装后绕过容量闸门。超限时按“最旧工具输出 → 最旧思考 → 最近工具/思考摘要 → 正文末尾”压缩，仍无法放入时发送不含调用方内容、动作、会话或工作目录的静态 emergency 卡片。`E2E_CARD_MAX_CHARS` 保持原有可配置性，但平台容量限制不可配置。

按钮处理生产路径只使用飞书长连接 `card.action.trigger`。stop/create/cancel action 会同步返回终态卡片，让飞书客户端立即置灰按钮；同时 bridge 仍通过 CardKit update 写入同一终态作为兜底和审计证据。设置 `E2E_CALLBACK_ADDR` 后，`serve` 会额外启动 `/card/callback` HTTP 兼容入口；该入口仅用于本地调试、迁移期验证和 real-Lark E2E 中对 service stop 生命周期的确定性触发，不替代生产长连接回调。

CardKit client 是最后一道本地硬闸：direct `Card` 会重新测量，`Prepared` 会校验完整性并直接发送其中的精确 JSON 字节；两者必须二选一，任何拒绝均发生在 token 获取、限流、重试和 HTTP 前。renderer 遇到意外的本地 capacity 拒绝时只用相同 sequence 重试一次 emergency 卡，成功后才推进 sequence。

## 生命周期

会话 snapshot 保存 conversation scope context（agent session id、workdir、history、model、token 和去重 receipt）以及当时的队列/活动 batch。恢复的语义是**只恢复 context，不恢复 pending 工作**：旧命令不会自动重跑，用户需要重新发送。

- 恢复时 `debouncing`、`queued`、`starting` input 一律变为 `cancelled`；`running` input 一律变为 `interrupted`。
- 这些终态只生成 recovery audit notice，不会重建旧卡、重新调度或重新启动 Claude。
- 恢复后队列和 active batch 均为空，新的输入从空闲 scope 重新进入正常 debounce/批处理流程。

正常 shutdown 仍会取消正在运行的 Agent 子进程，并释放进程内 pending action 和卡片路由状态；持久化 snapshot 负责让下一次启动保留 context，并明确清理未完成工作。

## Agent 选择（agent / home / bin）

`/config` 卡片可选择运行所用的 agent 类型、home（配置目录）和 bin（可执行路径）。

- 可选清单来自工作目录下的 `.lark-agent-bridge/agents.json`（`internal/config/agents.go`）。该文件缺失或非法时回退内置默认（单个 claude、`默认` home、`主机 claude` bin），不阻断启动。**该机制不引入任何新的 `E2E_*` 环境变量**，配置只走 JSON。
- 用户选择以 label 形式存入 `preferences.json`（`RuntimePreference.Agent/AgentHome/AgentBin`），执行时由 `Service.resolveAgentBinHome` 解析回真实 path。
- home 通过 per-kind env 注入子进程：Claude → `CLAUDE_CONFIG_DIR`，Codex → `CODEX_HOME`。空 home 不注入 config-dir env。bin 为空时，Claude 回退 `Config.ClaudeBin`（默认 `claude`），Codex 回退 `codex`。
- input 入队时冻结已解析的 bin/home，后续修改 `agents.json` 不会改写已排队工作。同一 chat/topic 的 Claude 与 Codex 用 agent kind 隔离 context。
- `doctor` 增加 `agents_config` 软检查：agents.json 不可解析时给出 warning，不作为 fatal。

## 暂缓项

- `/resume` 命令（内部 Agent context 可由 durable snapshot 自动续接，但不开放用户命令）
- tmux/PTY/WebTTY/共享终端
- 权限与用户映射
- metrics 观测
- SQLite、全局 FIFO / 公平调度和企业级多租户队列
