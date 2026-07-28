# Feishu AI Agent Bridge Architecture

## 目标

本项目实现一个独立的 Feishu/Lark AI agent bridge。它把飞书聊天、话题和 CardKit 按钮映射到 Claude Code 或 Codex one-shot 调用，让用户在飞书中发起任务、查看执行状态、阅读结果，并在需要时停止本轮执行。

`reference/lark-agent-workspace/` 只是归档参考，不是本 bridge 的接口约束。

## 核心模型

bridge 不托管交互式终端，也不通过 tmux/PTY 捕获输出。每个已调度 batch 形成一次所选 Agent 子进程调用；一个 batch 可包含一条或多条经 debounce 判定兼容的飞书输入：

- 启动命令：`claude -p --output-format stream-json --verbose --include-partial-messages --dangerously-skip-permissions --effort low <prompt>`
- Codex 新会话使用 `codex exec [-c model_reasoning_effort="<value>"] --json [--image <path> ...] [--] -`，续接使用 `codex exec [-c model_reasoning_effort="<value>"] resume --json [--image <path> ...] <thread-id> -`；prompt 写入 stdin。
- 非 `default` effort 由 Bridge 按入队时冻结的偏好通过 `model_reasoning_effort` 覆盖；`default` 以及 model、sandbox、approval、profile、plugins、MCP、rules 和 git-check 参数仍由所选 executable 和环境决定。
- 子进程 `cmd.Dir` 和 `PWD` 都设置为本轮请求解析出的工作目录。工作目录决策链是三层权威（`internal/bridge` 的 `effectiveWorkDir`）：① 本条消息的一次性 `--workdir` 覆盖；② **topic workspace cwd —— 唯一持久权威**，由 `/cd` / `/ws` 写入 `internal/workspace` 的 store，按 topic scope（`ChatID` 或 `ChatID:ThreadID`）粒度保存；③ 全局 `--default-workdir` / 环境默认目录兜底。会话仍照常**记录**它运行用的 workdir（供 catalog resume 归组），但**决定用哪个目录时不再读 session 的记录值**——记录与决策分离；切目录（`/cd`、`/ws use`）会重置会话，重建时自然落到 scope cwd。simulate 等未接 workspace store 的模式跳过第二层，直接回退默认目录。
- 如果当前 conversation scope 已保存 agent session id，后续消息使用对应 CLI 的 resume 协议续接。
- `/new` 会清空当前 conversation scope 保存的 agent session id，并从新会话开始。
- 执行开始时创建“执行中”卡片，读取 Claude `stream-json` stdout 时增量更新同一张卡片。
- 若连续 `E2E_CARD_HEARTBEAT_SEC`（默认 5 秒）没有成功的正常卡片更新，CardStream 会刷新同一卡片的耗时，表明任务通道仍存活；每次正常更新重置计时，终态和停止请求取消计时器。
- 执行中卡片带一次性“停止”按钮，点击后取消当前 Agent 子进程，同一卡片进入灰色“已停止”状态并置灰按钮。
- 完成、失败和停止后的卡片仍保留灰色 disabled 按钮，文案分别是“已完成”“已结束”“已停止”，避免用户误以为还能继续点击停止。

## 包结构

- `cmd/lark-agent-bridge`：CLI 入口，包含 `doctor`、`simulate`、`simulate-action`、`serve`
- `internal/access`：owner/admin、私聊用户白名单、响应群及群成员策略的持久化授权
- `internal/actiongrant`：敏感 CardKit action 的一次性 capability、过期校验和原子防重放
- `internal/agent`：Claude/Codex one-shot 命令构造
- `internal/session`：会话 key、状态、队列、agent session id、prompt 历史
- `internal/bridge`：消息命令解析、工作目录确认、队列、Agent runner/JSONL parser、CardStream 状态聚合、action 与定时任务桥接
- `internal/schedule`：自然语言规则提案的受限控制面、原子持久化、cron/timer 调度、恢复与运行状态机
- `internal/card`：卡片事件抽象、富文本 segment、fake renderer、长度控制、CardKit 2.0 JSON builder
- `internal/feishu`：飞书消息模型、SDK 长连接、sender/reaction 接口、消息归一化、CardKit HTTP client、CardKit renderer、`card.action.trigger` action 解析
- `internal/audit`：审计事件记录
- `internal/security`：敏感信息脱敏
- `internal/doctor`：就绪检查

`serve` 默认把审计事件写入 `<workdir>/.lark-agent-bridge/audit.jsonl`，也可以通过 `E2E_AUDIT_LOG` 覆盖路径。审计写入前会调用脱敏逻辑，避免 token、secret、password 等敏感值落盘。实际调用 AI Agent 前还会把最终可序列化请求写入 `<workdir>/.lark-agent-bridge/agent-requests.jsonl`（`E2E_AGENT_REQUEST_LOG` 可覆盖）；该受限日志保留精确 prompt，但排除调度凭据和回调。飞书长连接及 HTTP callback 的 SDK 解析前 payload 则写入 `<workdir>/.lark-agent-bridge/feishu-events.jsonl`（`E2E_FEISHU_EVENT_LOG` 可覆盖）。

`doctor --strict` 的 wrapper preflight 只读全局 effective preference，并通过 `agents.json` 解析实际 bin/home：Claude 使用有界 print-mode 探针，Codex 使用与生产一致的 `exec --json` + stdin 探针。未选中的 backend 不参与该项门禁，避免其独立凭据或 TLS 配置阻塞当前有效 agent 的部署验证。

## 命令面

当前飞书命令包括：

- `/new [--workdir <path>] [prompt]`：重置当前 chat/topic 的 Agent 会话；有 prompt 时立即执行，没有 prompt 时只创建 ready 状态。`--workdir` 是**仅对该条消息生效的一次性覆盖**，不会粘到后续消息。
- `/cd [<path>]`：无参时展示当前 topic 的有效工作目录；带路径时（管理员）把该 topic 的权威工作目录切到目标（不存在时走 Create directory 确认卡，创建即切入），切目录同时中断在跑的 run 并重置会话。
- `/ws list|save <name>|use <name>|remove <name>`：管理当前 topic 的命名工作区。`save` 记录当前有效工作目录，`use`（管理员）切入命名工作区（复用 `/cd` 的“中断+落库+重置会话”三连），`list` 只读展示。
- `/status`：查看当前 chat/topic 会话状态；群聊中会额外显示当前群内已知会话数量。
- `/stop`：停止当前所选 Agent 在当前 chat/topic scope 的 active batch；保留后续 queued 输入，空闲时返回安全提示。
- `/config`：配置 agent、agent home、agent bin、model、effort、回复模式（Coder、Worker、Singleton）和 Conversation mode；`/config reset` 恢复环境默认。
- `/invite user|admin|member @user`、`/remove user|admin|member @user`：管理私聊用户、管理员或当前群的指定成员；`member` 只能在已允许的目标群中管理。
- `/invite group`、`/remove group`、`/invite all group`：管理允许响应的群；新加入的群默认采用全体成员模式。
- `/group-access all|selected`：在当前允许群内切换全体成员或指定成员模式。owner/admin 始终可用。
- `/help`：显示帮助。

定时任务使用 `/cron`、`/timer` 及其 `add/info/run/enable/disable/del` 子命令；自然语言消息也可直接触发 Agent 提案。`/sessions`、`/history`、`/topic`、`/attach`、`/interrupt` 文本命令以及 `/claude`、`/codex` 旧入口均不属于当前范围。

消息入口默认 fail-closed：私聊仅允许 owner、admin 和 `allowed_users`；群聊先要求 chat 在 `allowed_chats`，再按该群的 `all_members` 或 `selected_members` 策略判断 sender。旧 access v1 配置迁移为 v2 时，已有响应群保持 `all_members`，避免升级后意外拒绝原有成员；任一访问策略写入都会递增 policy revision。

## 定时任务边界

Agent 只通过单次 token 和私有 Unix socket 提交 `cron`/`timer` 规则字段，不能覆盖用户、chat/topic 或执行配置。Bridge 校验规则并生成草稿，用户确认后才形成 enabled task。到期 occurrence 先原子写入 schedule snapshot，再以确定性 synthetic message ID 投递给现有 session queue，因此复用队列容量、receipt 去重、CardKit 输出和停机收敛能力。

调度准确性采用 at-least-once claim + durable receipt reconciliation，而不是假设进程或网络永不失败。重启只补偿 5 分钟窗口内的最近 occurrence；同任务已有 pending/queued/running run 时，新 occurrence 标记为 `skipped_overlap`；队列拥塞在窗口内重试，执行超过 30 分钟失败退出。结果发送阶段无法判定是否到达时记录 `delivery_unknown`，避免伪报成功。

群聊默认只处理 @ 机器人的消息；单聊默认处理全部文本。

## 会话 Key 与并发

Conversation mode 决定会话 key 和飞书回复位置：

- `chat`（默认）：key 为 `{Agent, ChatID}`，忽略入站 `ThreadID`，回复显式使用 `reply_in_thread=false`。
- `topic`：key 为 `{Agent, ChatID, ThreadID?}`，非空 thread/topic id 进入 key，回复显式使用 `reply_in_thread=true`；P2P 或群聊主会话只有正文中显式可见的 `@bot` 才 mint `@bot:<message_id>` synthetic topic key，否则沿用 `{Agent, ChatID}` chat root key。

`E2E_CONVERSATION_MODE=chat|topic` 提供环境默认；`/config` override 持久化到 `preferences.json`。mode 切换只影响保存成功后接收的新消息，不迁移或删除旧 session。input 入队时冻结 mode，pending workdir 确认也保存当时 preference，因此排队或等待按钮期间的配置变化不会改变该输入的回复位置。

并发规则：

- 同一 conversation scope 内串行执行。
- 执行中收到同一 scope 的新输入时进入持久化队列，并产生 `reaction` 事件提示已排队；workdir、model、effort 或 Conversation mode 不同的连续输入不能合入同一 batch。
- `topic` 模式下不同 topic 使用不同 key，可以并行运行各自的 Agent 子进程；`chat` 模式下同一 chat 只有一个串行 scope。
- 普通文本续接当前 mode 解析出的 scope；它不会隐式创建 session boundary，因此同一 debounce cohort 可以合并。
- 只有显式 `/new` 会重置当前 scope 的 agent session，并作为独占 batch boundary。
- `/stop` 只取消与完整 `{Agent, ChatID, ThreadID?}` key 匹配的 active batch；不清空 queue，当前 batch 收敛为 stopped 后继续调度后续输入。
- 队列上限按单一 scope 的 pending input 计算，满时拒绝新输入且不写入去重 receipt。
- 个人版不设置全局 semaphore、跨 scope FIFO 或公平性调度；唯一的顺序保证是同一 scope 串行，不同 scope 可并行。

## Claude 输出解析

Claude runner 使用 `--output-format stream-json`，通过 stdout pipe 逐行读取 JSONL 事件。每个事件会同时进入两条路径：

- 增量路径：实时更新当前 CardStream 状态，并按 `E2E_CARD_UPDATE_MS` 节流刷新同一张 CardKit 卡片；没有新事件时由独立 idle heartbeat 更新耗时，不依赖 Agent stdout 继续产出。
- 最终路径：在进程退出时汇总最终正文、思考、工具、model、token 和 Claude session id，写回内存会话状态。

当前从 JSONL 事件中提取：

- 正文：`text` block 或 `result` 字段
- 增量正文：`content_block_delta` 中的 `text_delta`
- 思考过程：`thinking`、`reasoning`、`redacted_thinking` block，以及 `thinking_delta`、`reasoning_delta`、`reasoning_content` 等增量字段
- 工具调用：`tool_use`、`tool_result` block，以及 `input_json_delta` 等工具输入增量
- model：顶层或 message 内的 `model`
- token 数：递归统计 usage 中以 `tokens` 结尾的数字字段
- Claude session id：顶层或 message 内的 `session_id`

输出会映射为 `card.Segment`。Claude assistant 文本先暂存：后续出现工具活动时判定为可展示的 progress，流结束时才判定为最终回复。Coder（兼容 key `append`）将 progress、工具安全摘要与末答按事件顺序投影到同一个稳定的 `answer` Markdown 元素；原生 thinking 单独放入折叠面板。Worker（`append-clean-card`）将 thought/progress 与 tools 放入上下时间线，Singleton（`latest-card`）使用合并过程区；两者终态都只裁剪正文，不删除过程消息或工具次数。

## Codex 输出解析

Codex runner 逐行解析 `exec --json` 输出：`thread.started` 保存 thread id，`item.*` 映射回答、reasoning 和 command 工具段，`turn.completed` 统计 token 并收敛终态，`turn.failed` 转为错误。由于 `exec --json` 会把 commentary 与最终回答都降为不带 phase 的 `agent_message`，parser 会暂存最新消息：后续仍有活动时将上一条按原文映射为 thought，只在 `turn.completed` 时将最后一条收敛为最终回答。该路径不依赖 `model_reasoning_summary`。未知事件和协议异常会记录 audit，用于发现 CLI 升级带来的协议漂移。

Codex 的实际模型、推理深度和上下文占用只信任 workspace exporter 的 `context-usage` sidecar。新会话首帧尚无 thread id 时，按 sidecar 契约使用 canonical workdir 下最新记录作为 `~ctx` 近似值；`thread.started` 后优先读取当前 session 文件，并在 sidecar 可用时原子升级模型、推理深度与占用。纯 metadata 变化同样进入 CardKit 预览节流，不要求同时出现正文或工具事件。Bridge 不解析 Codex transcript，也不从 executable 名称猜测模型或推理深度。

## 飞书卡片职责

CardKit 卡片负责展示一次 Claude 请求的状态：

- 执行中：蓝色 header，标题显示当前活动和耗时，例如 `🧠 正在推理 · ⏱ 12s`、`🛠️ 正在执行工具 · ⏱ 18s`、`✍️ 正在回复 · ⏱ 24s`，显示“停止”按钮。
- 结果：绿色 header，标题显示 `✅ 已完成 · ⏱ X`；`append` 保留有序正文、内联工具文字和折叠 thinking，clean/latest 只保留最终答案；停止按钮变为灰色 disabled “已完成”。
- 停止：灰色 header，标题显示 `⏹ 已停止 · ⏱ X`，同一卡片按钮置灰为“已停止”。
- 错误：红色 header，标题显示 `❌ 执行失败 · ⏱ X`，展示错误内容，停止按钮变为灰色 disabled “已结束”。
- 工作目录确认：当 `--workdir` 不存在时提供“Create directory”和“Cancel”按钮；创建成功后确认卡变为绿色终态并禁用按钮，取消后变为灰色终态并禁用按钮。创建成功后 Claude 执行使用独立运行卡片，不覆盖确认卡片。
- 底部状态栏：先用分割线与正文和折叠面板隔开，再使用两行 CardKit `column_set` 分栏展示。
- 底部第一行：agent、model、tokens 三列，权重比例 `10:14:18`，tokens 格式为 `🔢 tokens: ▶ 本轮 / ∑ 累计`。
- 底部第二行：user、ip、workdir 三列，权重比例 `10:12:30`，emoji 分别为 `👤`、`🖥️`、`📁`。
- 底部不展示 status，也不使用 `agent=`、`model=`、`workdir=` 这类机器前缀。

长输出先由 `E2E_CARD_MAX_CHARS` 按 rune 截断，再经过 `internal/card.PrepareLarkCard` 的容量管线。`append` 在此之前还会把单条工具摘要限制为 **80 rune**、thinking 卡片投影限制为最近 **3000 rune**、Inline timeline 限制为 **9000 rune**；timeline 超限时按 entry 删除最旧过程并保留最终回复。容量管线以最终 JSON 字节数为准，固定限制为 **28 KiB** 和 **200 个带 `tag` 的组件**；`PreparedLarkCard` 是不透明值，renderer、同步 callback 和 CardKit HTTP client 只能消费其已验证 JSON，不能重新组装后绕过容量闸门。普通布局超限时按“最旧工具输出 → 最旧思考 → 最近工具/思考摘要 → 正文末尾”压缩；Inline 布局额外按 Markdown 段落保留最近内容并保证 fitted `Answer()` 与 payload 一致。仍无法放入时发送不含调用方内容、动作、会话或工作目录的静态 emergency 卡片。`E2E_CARD_MAX_CHARS` 保持原有可配置性，但平台容量限制不可配置。

按钮处理生产路径只使用飞书长连接 `card.action.trigger`。stop、workdir create/cancel、schedule confirm/cancel 和 resume select 在渲染时签发随机、不透明、持久化的 `ActionGrant`，绑定 actor、chat、session、action、value digest、过期时间和 access policy revision。回调在任何副作用前校验并原子消费 grant；跨 actor、跨 scope、篡改 value、过期、策略变化和重复点击均被拒绝。stop 与 workdir action 允许 owner/admin 代操作，schedule confirmation 只允许草稿创建者。同步回调返回终态卡片，同时 bridge 仍通过 CardKit update 写入同一终态作为兜底和审计证据。设置 `E2E_CALLBACK_ADDR` 后，`serve` 会额外启动 `/card/callback` HTTP 兼容入口；该入口仅用于本地调试、迁移期验证和 real-Lark E2E 中对 service stop 生命周期的确定性触发，不替代生产长连接回调。

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
- metrics 观测
- SQLite、全局 FIFO / 公平调度和企业级多租户队列
