# Feishu Agent Bridge Architecture

## 目标

本项目实现一个独立的 Feishu agent bridge。它把飞书聊天、话题和卡片按钮映射到本地或远端的交互式 AI agent 终端，让用户可以在飞书里控制 `claude`、`codex` 等 agent，同时实时看到 agent 终端输出。

`reference/lark-agent-workspace/` 只是归档参考，不是本 bridge 的接口约束。bridge 可以对接任何满足交互式终端行为的 agent。

## 核心模型

bridge 以长期运行的 tmux/PTY 会话为核心，而不是一次性命令调用：

- 固定 tmux session：`lark-agent-bridge`
- 一个 agent 会话对应一个 tmux window
- 飞书输入写入对应 window 的 stdin
- tmux pane 输出被 bridge 捕获并同步到飞书 CardKit 卡片
- 人也可以通过 `/attach` 返回的 tmux 命令直接 attach 到同一个 window 手动输入

同一会话内部串行执行。agent 正在执行时收到的新输入会进入内存队列，并通过飞书反应提示“已收到并排队”。跨会话可以并发。

## 包结构

- `cmd/lark-agent-bridge`：CLI 入口，包含 `doctor`、`simulate`、`serve`
- `internal/agent`：agent 抽象层，负责 `claude`、`codex` 的命令构造与审批模式映射
- `internal/tmux`：tmux session/window 管理，包含真实 runner 与测试 recording runner
- `internal/tmux` 还包含 pane 增量监听器，用于把终端输出同步到卡片
- `internal/session`：会话 key、状态、队列、prompt 历史和 window 名管理
- `internal/bridge`：消息归一化、命令解析、service 编排
- `internal/card`：卡片事件抽象、富文本 segment、fake renderer、长度控制、飞书 interactive card JSON builder
- `internal/feishu`：从 `fai-agent-ph` 裁剪出的飞书消息模型、SDK 长连接、sender/reaction 接口、消息归一化、内存去重、CardKit HTTP client、CardKit renderer
- `internal/audit`：审计事件记录
- `internal/security`：敏感信息脱敏
- `internal/doctor`：就绪检查

`serve` 默认把审计事件写入 `<workdir>/.lark-agent-bridge/audit.jsonl`，也可以通过 `E2E_AUDIT_LOG` 覆盖路径。审计写入前会调用脱敏逻辑，避免 token、secret、password 等敏感值落盘。

## Agent 交互识别

`internal/agent` 提供启发式交互识别：

- 授权请求：识别 permission、approve、授权、Allow once 等输出
- 选择题：识别带编号的选项
- 恢复 session：识别 resume session/恢复会话等输出，且必须解析出编号候选项

识别结果后续会映射为 CardKit 按钮。第一版先用启发式规则，后续如果 `claude` 或 `codex` 提供稳定 hook/event API，再优先切换到原生事件。

交互卡片默认 120 秒超时，可通过 `E2E_INTERACTION_TIMEOUT_SEC` 调整。超时后 bridge 会向 agent stdin 写入默认值：授权请求写入 `reject`，选择题和恢复候选写入 `cancel`。用户在超时前点击按钮会清除 pending timeout，避免重复写入。点击或超时后，bridge 会短期记录该交互的候选签名；如果 tmux TUI 重绘又带出相同候选列表，后续增量会按普通 stream 展示，避免重复生成同一组按钮。

普通流式输出由 `internal/bridge` 按行拆分为富文本 segment，并合并相邻同类行：

- 普通文本：默认 segment
- 思考过程：识别 thinking、thought、reasoning、思考等标记
- 工具调用：识别 tool、tool_use、function_call、mcp、工具，以及常见 `Bash(...)`、`Read(...)`、`Edit(...)` 等调用格式

这层分段只负责展示，不改变 agent stdin/stdout 行为。

## 会话 Key

会话 key 由以下字段组成：

- agent 名称：`claude` 或 `codex`
- 飞书 chat id
- 飞书 thread/topic id，可空

话题模式下，thread/topic id 进入 key，因此话题会话与普通群聊/单聊会话完全独立。

topic mode 是 chat 级内存配置，默认开启以保持飞书话题隔离；用户可在当前 chat 内发送 `/topic off` 临时忽略 thread/topic id，使后续输入回到普通 chat 会话，发送 `/topic on` 恢复隔离。

## 启动模式

抽象三种审批级别：

- `default`：保留 agent 默认审批行为
- `auto`：尽量自动接受低风险编辑或普通审批
- `full`：完全授权，允许 agent 以高权限参数启动

具体 CLI 参数由每个 agent adapter 负责。当前实现：

- `claude full`：`claude --dangerously-skip-permissions`
- `codex full`：`codex -c check_for_update_on_startup=false --dangerously-bypass-approvals-and-sandbox`
- `claude resume <id>`：`claude --resume <id>`
- `claude resume --last`：`claude --continue`
- `codex resume <id>`：`codex -c check_for_update_on_startup=false resume <id>`
- `codex resume --last`：`codex -c check_for_update_on_startup=false resume --last`

`codex` adapter 会统一追加 `-c check_for_update_on_startup=false`，避免真实 E2E 或常驻会话启动时进入 CLI 自更新提示，导致用户输入和 bridge 队列被更新流程截获。

后续需要基于实际 agent CLI 版本继续校正 `auto` 参数。

## 飞书卡片职责

CardKit 卡片是远程终端同步视图：

- 流式显示终端输出
- 以富文本 segment 区分普通文本、思考过程、工具调用、错误
- 底部展示 agent、model、token 数量、工作目录、状态
- 查询进行中显示一次性的「停止」按钮
- 点击停止后，bridge 向 tmux window 发送中断，把按钮置灰，并释放当前 running 状态；如果同一会话已有排队输入，则推进下一条输入到同一个 tmux window
- 长输出通过分页和兜底截断控制卡片长度；普通命令回复和流式 tmux delta 都会按 `E2E_CARD_MAX_CHARS` 拆页
- 流式分页复用同一个 session/card 更新，用 `page X/Y` 标明当前页，避免 CardKit 后续页因为没有原始飞书消息 id 而无法创建新卡

`serve` 使用 reaction/card 分流 renderer：

- `reaction` 事件调用飞书消息 reaction API
- 其他卡片事件通过 CardKit 按 session 创建或更新同一张卡片
- tmux pane 轮询循环会把终端输出增量映射为 `stream`、`authorization`、`choice`、`resume` 等事件
- 卡片按钮生产入口是飞书长连接 `card.action.trigger`；`internal/feishu` 先把 SDK `CardActionTriggerEvent` 转成轻量 `CardAction`，`serve` 再映射为 `bridge.ActionRequest` 并复用 service action 处理
- `stop`/`interrupt` 按钮成功处理后，长连接回调响应会直接返回 disabled「已停止」`card_json`；同时 service 仍异步更新原 CardKit 卡片并额外回复 terminal「已停止」确认卡
- 设置 `E2E_CALLBACK_ADDR` 后，`serve` 会额外启动 `/card/callback` HTTP 兼容入口；该入口仅用于本地调试和迁移期验证，不作为真实 E2E 依赖

如果消息指定的 `--workdir` 不存在，bridge 会先发送工作目录创建确认卡片，不提前创建目录，也不启动 agent。确认卡片同样受 `E2E_INTERACTION_TIMEOUT_SEC` 控制；超时默认取消 pending 请求，避免无响应时残留待启动任务。

## 生命周期

bridge 主进程退出时需要清理：

- `lark-agent-bridge` tmux session
- 所有 agent window 和进程
- 临时卡片状态
- 运行中队列状态

`serve` 会监听 Ctrl-C 和 `SIGTERM`，退出时调用 service cleanup，清理固定 tmux session。内存队列、watcher 和卡片路由状态随进程退出释放。

会话不会因为闲置被主动释放。默认 24 小时无活跃后，bridge 只发送提醒，用户可以在飞书上终止会话。阈值由 `E2E_IDLE_REMINDER_AFTER_SEC` 控制，后台扫描间隔由 `E2E_IDLE_CHECK_MS` 控制；生产默认值分别是 24 小时和 1 小时，E2E 可临时缩短到几秒。

当前 service 已提供 `RenderIdleReminders`，用于扫描内存会话并产生 `idle_reminder` 卡片事件；它不会停止 tmux window。提醒只对 `idle` 状态会话触发，非空终端输出会刷新会话的 idle 时钟并清除已提醒标记，避免提醒卡片被后续 stream 更新覆盖。提醒卡片携带 `terminate_session` 动作，用户点击后才会执行 `tmux kill-window` 并把会话状态标记为 `stopped`。

## 暂缓项

- 权限与用户映射
- metrics 观测
- 工作目录白名单
- SQLite/重启恢复
- WebTTY
- 飞书按钮点击权限限制
