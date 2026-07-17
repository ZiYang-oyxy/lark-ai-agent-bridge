# Feishu AI Agent Bridge Architecture

## 目标

本项目实现一个独立的 Feishu/Lark AI agent bridge。它把飞书聊天、话题和 CardKit 按钮映射到 Claude one-shot 调用，让用户在飞书中发起任务、查看执行状态、阅读结果，并在需要时停止本轮执行。

`reference/lark-agent-workspace/` 只是归档参考，不是本 bridge 的接口约束。

## 核心模型

bridge 当前不再托管交互式终端，也不再通过 tmux/PTY 捕获输出。每条可处理飞书消息会形成一次 Claude 子进程调用：

- 启动命令：`claude -p --output-format stream-json --dangerously-skip-permissions <prompt>`
- 如果当前 chat/topic 已保存 Claude session id，后续普通消息会追加 `--resume <session_id>` 续接内部会话。
- `/new` 会清空当前 chat/topic 保存的 Claude session id，并从新会话开始。
- 执行开始时创建“执行中”卡片，完成后更新同一张卡片为结果。
- 执行中卡片带一次性“停止”按钮，点击后取消当前 Claude 子进程并置灰。

## 包结构

- `cmd/lark-agent-bridge`：CLI 入口，包含 `doctor`、`simulate`、`simulate-action`、`serve`
- `internal/agent`：Claude one-shot 命令构造
- `internal/session`：会话 key、状态、队列、Claude session id、prompt 历史
- `internal/bridge`：消息命令解析、工作目录确认、队列、Claude runner、action 处理
- `internal/card`：卡片事件抽象、富文本 segment、fake renderer、长度控制、CardKit 2.0 JSON builder
- `internal/feishu`：飞书消息模型、SDK 长连接、sender/reaction 接口、消息归一化、CardKit HTTP client、CardKit renderer、`card.action.trigger` action 解析
- `internal/audit`：审计事件记录
- `internal/security`：敏感信息脱敏
- `internal/doctor`：就绪检查

`serve` 默认把审计事件写入 `<workdir>/.lark-agent-bridge/audit.jsonl`，也可以通过 `E2E_AUDIT_LOG` 覆盖路径。审计写入前会调用脱敏逻辑，避免 token、secret、password 等敏感值落盘。

## 命令面

当前飞书命令只保留：

- `/new [--workdir <path>] [prompt]`：重置当前 chat/topic 的 Claude 会话；有 prompt 时立即执行，没有 prompt 时只创建 ready 状态。
- `/status`：查看当前 chat/topic 会话状态；群聊中会额外显示当前群内已知会话数量。
- `/help`：显示帮助。

暂不实现 `/resume`。`/sessions`、`/history`、`/topic`、`/attach`、`/interrupt`、`/stop` 文本命令以及 `/claude`、`/codex` 旧入口均不属于当前范围。

群聊默认只处理 @ 机器人的消息；单聊默认处理全部文本。

## 会话 Key 与并发

会话 key 由以下字段组成：

- agent：当前固定为 `claude`
- 飞书 chat id
- 飞书 thread/topic id，可空

有 thread/topic id 时，topic 会进入 key；无 topic 时只按 chat 维度管理。

并发规则：

- 同一 chat/topic 内串行执行。
- 执行中收到同一 chat/topic 的新输入时进入内存队列，并产生 `reaction` 事件提示已排队。
- 不同 topic 使用不同 key，可以并行运行各自的 Claude 子进程。
- 非 topic 普通文本默认新建会话。
- topic 普通文本默认继续当前 topic 会话；`/new` 会重置当前 topic 会话。

## Claude 输出解析

Claude runner 使用 `--output-format stream-json`，并从 JSONL 事件中提取：

- 正文：`text` block 或 `result` 字段
- 思考过程：`thinking`、`reasoning`、`redacted_thinking` block
- 工具调用：`tool_use`、`tool_result` block
- model：顶层或 message 内的 `model`
- token 数：递归统计 usage 中以 `tokens` 结尾的数字字段
- Claude session id：顶层或 message 内的 `session_id`

输出会映射为 `card.Segment`，CardKit 卡片中正文直接展示，思考过程和工具调用放入折叠面板。

## 飞书卡片职责

CardKit 卡片负责展示一次 Claude 请求的状态：

- 执行中：显示“正在执行 Claude 请求...”和“停止”按钮。
- 结果：展示正文、折叠思考过程、折叠工具调用。
- 错误：展示错误内容。
- 工作目录确认：当 `--workdir` 不存在时提供“Create directory”和“Cancel”按钮。
- 底部 meta：展示 agent、model、tokens、workdir、status。

长输出由 `E2E_CARD_MAX_CHARS` 控制，避免超过飞书卡片限制。

按钮处理生产路径只使用飞书长连接 `card.action.trigger`。设置 `E2E_CALLBACK_ADDR` 后，`serve` 会额外启动 `/card/callback` HTTP 兼容入口；该入口仅用于本地调试和迁移期验证，不作为真实 E2E 依赖。

## 生命周期

bridge 主进程退出时会取消仍在运行的 Claude 子进程，并释放内存中的 pending action、队列和卡片路由状态。当前不做 SQLite/重启恢复，会话状态和 prompt 历史只保存在内存中。

## 暂缓项

- `/resume` 和历史会话恢复
- `codex` agent 适配
- tmux/PTY/WebTTY/共享终端
- 文件/图片输入
- 权限与用户映射
- metrics 观测
- SQLite/重启恢复
