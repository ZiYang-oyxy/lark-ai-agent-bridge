# Testing Workflow

## 本地基础验证

执行单元测试：

```bash
GOCACHE=$PWD/.cache/go-build go test ./...
```

执行本地一键验证：

```bash
./scripts/verify.sh
```

该脚本会运行 `go test ./...`、`doctor`、群聊未 @ 过滤模拟、默认 agent 模拟、`claude`/`codex` auto/full 启动参数模拟、命令面模拟、话题模式模拟、attach 模拟、interrupt/stop 模拟、同会话排队模拟、跨 chat/topic 独立 session 单测、idle 提醒与终止会话按钮单测、停止按钮状态机单测、`/resume` 恢复模拟、工作目录确认超时模拟、工作目录创建/取消模拟、富文本分段模拟、授权/选择/恢复交互卡片模拟、交互超时模拟、长流式分页模拟，并在未设置飞书凭据时验证 `serve` 会明确拒绝启动。若当前环境已经配置飞书应用凭据，可开启严格模式：

```bash
REQUIRE_LARK=1 ./scripts/verify.sh
```

验证 CLI 编译和帮助：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge help
```

执行 doctor：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge doctor
```

doctor 应检查：

- `tmux` 是否存在
- `claude` 是否存在
- `codex` 是否存在
- `LARK_APP_ID` 是否设置
- `LARK_APP_SECRET` 是否设置
- 默认 tmux session 是否为 `lark-agent-bridge`
- 默认 agent 是否可识别
- 默认工作目录是否存在且是目录
- 审计日志路径是否可创建和写入
- `E2E_CALLBACK_ADDR` 是否设置；未设置也允许通过，表示不启动本地卡片回调 HTTP 服务
- `E2E_CARD_UPDATE_MS` 对应的卡片更新间隔是否为正数
- `E2E_INTERACTION_TIMEOUT_SEC` 对应的交互卡片超时时间是否为正数
- `E2E_CARD_MAX_CHARS` 对应的卡片长度限制是否为正数
- `E2E_QUEUE_QUIET_POLLS` 对应的 quiet-poll 阈值是否为非负数
- `E2E_IDLE_REMINDER_AFTER_SEC` 对应的闲置提醒阈值是否为正数
- `E2E_IDLE_CHECK_MS` 对应的闲置扫描间隔是否为正数
- `$HOME/.codex/config.toml` 是否仍包含已废弃的 `codex_hooks`；若存在，doctor 会以 `warn` 提示保留 `[features].hooks = true` 并移除旧项

流式卡片更新间隔由 `E2E_CARD_UPDATE_MS` 控制，默认 `800` 毫秒。`serve` 会用该间隔轮询 tmux pane 输出，避免对飞书卡片做过高频更新。

审计日志默认写入 `<workdir>/.lark-agent-bridge/audit.jsonl`，可通过 `E2E_AUDIT_LOG=/path/to/audit.jsonl` 覆盖。日志为 JSONL 格式，`detail` 字段会先脱敏。

## 本地模拟消息

不连接飞书，仅模拟一条飞书消息：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/claude hello"
```

默认使用 recording tmux runner，不会真的启动 agent。输出 JSON 包含：

- `events`：卡片事件
- `audit`：审计事件
- `tmux`：bridge 将要执行的 tmux 命令

模拟群聊未 @ 机器人：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -group=true -mentioned=false -text "hello"
```

期望没有事件输出。

模拟同一会话排队：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/claude first" -next-text "/claude second"
```

期望第二条消息输出 `reaction` 事件，`Message` 为 `queued`。

模拟真实 tmux：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -real-tmux=true -text "/claude hello"
```

该命令会创建 `lark-agent-bridge` tmux session 和对应 window，并向 agent 写入输入。验证后可清理：

```bash
tmux kill-session -t lark-agent-bridge
```

真实 tmux smoke：

```bash
./scripts/tmux-smoke.sh
```

该脚本使用独立 session `lark-agent-bridge-smoke`，验证创建 window、写入 stdin、capture 输出和清理。脚本还会执行：

```bash
go test -tags tmux ./internal/tmux -run TestPaneWatcherRealTmuxCapturesExternalInput -count=1
```

该集成测试使用独立 session `lark-agent-bridge-watch-smoke`，模拟同一 tmux window 的外部输入，并确认 `PaneWatcher` 能捕获新增输出；这覆盖 `/attach` 后人工在终端输入、bridge 仍可轮询同步到飞书卡片的底层能力。

退出清理由 `internal/bridge` 单测覆盖：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/bridge -run TestServiceCleanupKillsTmuxSessionAndAudits
```

期望 service cleanup 调用 `tmux kill-session -t lark-agent-bridge`，并写入 `cleanup` 审计事件。`serve` 在信号退出路径中通过 `defer svc.Cleanup(...)` 复用这段逻辑。

模拟启动参数：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/claude --approval full hello"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/codex --workdir /tmp/project inspect"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/codex --approval auto inspect"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "hello"
```

期望无前缀输入默认进入 `claude`；`claude --approval auto` 生成 `claude --permission-mode acceptEdits`，`claude --full` 生成 `claude --dangerously-skip-permissions`；`codex --approval auto` 生成 `codex -c check_for_update_on_startup=false --ask-for-approval on-request`，`codex --full` 生成 `codex -c check_for_update_on_startup=false --dangerously-bypass-approvals-and-sandbox`。

模拟恢复 agent 原生 session：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/resume claude 34ccac3d"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/resume codex --last"
```

期望输出 `status` 事件，tmux 命令分别包含 `claude --resume 34ccac3d` 和 `codex -c check_for_update_on_startup=false resume --last`，且不会产生 `send-keys`。若同一 bridge 会话已有活跃 tmux window，`/resume` 应返回错误，提示先 `/stop`。

模拟工作目录不存在：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/claude --workdir /tmp/missing-for-test hello"
```

期望输出 `workdir_confirm` 事件，并包含 `create_workdir` 和 `cancel_workdir` 动作；此时不应产生 tmux 命令。

模拟工作目录确认超时：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate \
  -text "/claude --workdir /tmp/missing-for-timeout hello" \
  -timeout-now
```

期望先输出 `workdir_confirm`，再输出 `action` 事件 `workdir creation timed out: cancelled`；此时不应创建目录，也不应产生 tmux 命令。

模拟点击创建目录并恢复原请求：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action \
  -prime-text "/claude --workdir /tmp/lark-agent-bridge-confirm-test hello" \
  -action create_workdir \
  -value /tmp/lark-agent-bridge-confirm-test
```

期望先输出 `workdir_confirm`，再输出 `action`，最后输出 `stream`；tmux 命令里应包含新建目录作为 `new-window -c` 的工作目录，并向 agent 写入原 prompt。

模拟点击取消创建目录：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action \
  -prime-text "/claude --workdir /tmp/lark-agent-bridge-cancel-test hello" \
  -action cancel_workdir \
  -value /tmp/lark-agent-bridge-cancel-test
```

期望先输出 `workdir_confirm`，再输出 `action` 事件 `workdir creation cancelled`；此时不应创建目录，也不应产生 tmux `new-window` 命令。

## 本地模拟按钮与终端输出

模拟停止按钮：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action -action stop -session claude:chat-demo
```

期望输出包含 tmux `C-c` 和 disabled stop button。单元测试还会验证停止当前轮后 session 状态释放为 idle，后续输入不会被错误排队；若已有排队输入，停止当前轮后会把下一条 prompt 写入同一个 tmux window。

卡片 action payload 解析由 `internal/bridge` 和 `internal/feishu` 单元测试覆盖。真实按钮入口是长连接 `card.action.trigger`，需要用飞书按钮点击验证：

- `stop` 按钮转为 `ActionRequest{ActionID:"stop"}`
- 授权按钮转为对应 `allow_*` 或 `reject`
- 选择按钮转为 `choice_*`
- 恢复按钮转为 `resume_*`
- 长连接 `OnP2CardActionTrigger` 能收到事件并调用 `Service.HandleAction`
- `stop`/`interrupt` 成功处理后，长连接回调响应会返回一张 disabled「已停止」`card_json`，降低只依赖异步 CardKit update 时客户端不刷新的概率
- action value 为对象、`name/form_value/option` fallback 时都能解析

本地 HTTP 回调入口由 `internal/bridge` 单元测试覆盖，仅作为兼容/调试路径。生产和 E2E 不需要配置公网 callback URL；真实运行时直接启动：

```bash
lark-agent-bridge serve
```

飞书后台的回调配置需要选择“使用长连接接收回调”，并订阅 `card.action.trigger`。点击真实卡片按钮后，bridge 日志/audit 应出现对应 action，tmux/stdin/session/card 状态应发生预期变化。

模拟授权请求输出：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "Tool permission required\nAllow once\nReject"
```

期望输出 `authorization` 事件，并包含四个授权动作。

模拟授权超时默认拒绝：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output \
  -output "Tool permission required\nAllow once\nReject" \
  -timeout-now
```

期望先输出 `authorization`，再输出 `action` 事件 `interaction timed out: sent reject`，tmux 命令中包含写入 `reject`。选择题与恢复候选超时默认写入 `cancel`。

模拟 agent ready 后自动出队：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "done\n>" -next-text "/claude second"
```

期望输出包含 `dequeue` 事件，并且 tmux 命令里有 `send-keys` 写入 `second`。

如果真实 agent 不输出稳定 prompt，可以先用 quiet-poll 兜底配置做部署校准：

```bash
E2E_QUEUE_QUIET_POLLS=3 GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "" -next-text "/claude second"
```

`E2E_QUEUE_QUIET_POLLS` 表示连续多少次没有新增 pane 输出后，把当前轮次视为可出队。该值默认为 `0`，表示不启用 quiet-poll 兜底。

模拟 runtime meta：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "model: claude-sonnet-4\ntokens: 123"
```

期望后续卡片底部 meta 可展示 model/token。真实 CLI 输出格式可能不同，需要在 E2E 时继续校准解析规则。

模拟混合富文本分段：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "plain\nThinking: inspect plan\nTool: Bash ls\nfinal"
```

期望输出 `stream` 事件，并且 `Segments` 依次包含 `text`、`thought`、`tool`、`text`。单元测试 `TestSegmentOutputSplitsMixedRichSegments` 和 `TestPollSessionOutputRendersMixedRichSegments` 覆盖同一段 pane delta 中的混合分段。

模拟选择题输出：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "请选择:\n1. repo top3\n2. AI only"
```

期望输出 `choice` 事件，并包含选项动作。

模拟恢复候选输出：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "resume session\n1 34ccac3d 0s ago query\n2 def456 1m ago inspect"
```

期望输出 `resume` 事件，包含 `resume_1`、`resume_2` 和 `resume_cancel` 动作。空格分隔的 `1 34ccac3d ...` 格式应被识别为候选项；只有包含编号候选项的 resume/session 输出才会被识别为恢复卡片。

选择题和恢复候选点击写回由以下单测覆盖：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/agent ./internal/bridge -run 'TestDetectResume|TestChoiceActionWritesSelectionToAgent|TestResumeCandidateActionWritesSelectionToAgent'
```

`TestResumeCandidateActionWritesSelectionToAgent` 还会覆盖已点击恢复候选后，同一候选列表被 TUI 重绘再次捕获时不会重复渲染恢复卡片。

模拟 agent/tmux pane 异常：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -capture-error "pane missing"
```

期望输出 `error` 事件，底部状态为 `crashed`，并包含 `restart_session` 动作。真实飞书 E2E 中点击 `Restart session` 后，应在同一 workdir、同一 agent 和上次审批模式下重建 tmux window。

## 命令验证

需要覆盖以下输入：

```text
/help
/claude hello
/codex inspect repo
/sessions
/status
/status codex
/attach
/attach codex
/interrupt
/interrupt codex
/stop
/stop codex
/history
/history codex
/resume claude 34ccac3d
/resume codex --last
/topic status
/topic off
/topic on
```

验证点：

- `/claude` 和 `/codex` 使用全称
- 无前缀时默认 `claude`
- 单聊默认响应
- 群聊未 @ 不响应
- `/attach` 返回 tmux attach 命令
- `/interrupt` 映射到 tmux `C-c`，释放当前 running 状态，并在存在队列时推进下一条输入
- `/stop` 终止当前 window 并把 session 标记为 stopped
- `/status codex`、`/stop codex` 等管理命令能指定非默认 agent
- `/resume` 能指定 agent、session id、`--last`、`--workdir` 和审批模式，启动恢复模式 window 时不写 stdin
- `/status` 返回当前会话 state、queue、history、window、workdir、model/token 和 attach 命令
- `/sessions` 返回每个会话的 agent、chat、thread、state、window、queue、history、approval、last_active、age 和 attach 命令
- `/topic off` 后同一 chat 的 thread id 不再进入 session key；`/topic on` 后恢复 thread/topic 隔离
- agent 命令支持 `--workdir <path>`、`--approval default|auto|full`、`--auto`、`--full`

模拟话题模式：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate \
  -thread topic-a \
  -text "/claude first" \
  -next-text "/topic off" \
  -next-text "/claude second" \
  -next-text "/status"
```

期望第一条 prompt 进入 `claude:chat-demo:thread:topic-a`，关闭 topic 后第二条 prompt 和 `/status` 使用 `claude:chat-demo`。

## 队列验证

使用单元测试覆盖同一会话第二条输入排队：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/session ./internal/bridge
```

验证点：

- 第一条消息使会话进入 running
- 第二条消息进入 queue
- renderer 输出 `reaction` 事件，消息为 `queued`
- prompt 历史包含两条用户输入

使用单元测试覆盖不同会话互相独立：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/bridge -run 'TestDifferentChatsCreateIndependentSessions|TestDifferentTopicsCreateIndependentSessions'
```

验证点：

- 不同 chat 的消息分别进入 `claude:chat-a` 和 `claude:chat-b`
- 同一 chat 不同 topic 的消息分别进入 `claude:chat:thread:topic-a` 和 `claude:chat:thread:topic-b`
- renderer 输出均为 `stream`，不会产生 `queued` reaction
- tmux 创建独立 window，例如 `agent-claude-chat-a`、`agent-claude-chat-b`、`agent-claude-chat-thread-topic-a`、`agent-claude-chat-thread-topic-b`

## 闲置提醒验证

使用单元测试覆盖默认 24 小时闲置提醒和飞书终止会话动作：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/bridge ./internal/card -run 'TestRenderIdleReminders|TestTerminateSessionActions|TestBuildLarkCardIncludesTerminateSessionAction'
```

验证点：

- 默认闲置 24 小时后渲染 `idle_reminder` 事件，且文案使用当前配置的阈值
- 只有 `idle` 状态会话会触发提醒；running 状态和后续非空终端输出会刷新 idle 时钟
- 提醒卡片包含 `terminate_session` 危险按钮
- 仅发送提醒时不会主动执行 `tmux kill-window`
- 点击 `terminate_session` 后执行 `tmux kill-window`，会话状态变为 `stopped`，按钮置灰

真实飞书 E2E 不需要等待 24 小时，可临时缩短提醒阈值和扫描间隔：

```bash
E2E_IDLE_REMINDER_AFTER_SEC=3 E2E_IDLE_CHECK_MS=1000 GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge serve
```

验证步骤：

- 在目标群 @ bot 发起一条短任务，等待任务进入 idle
- 等待最后一次 stream 更新后约 3 秒，确认飞书生成闲置提醒卡片
- 点击「终止会话」，确认 audit 记录 `terminate_session`，tmux window 被清理，卡片按钮置灰

## 审计验证

使用单元测试覆盖审计 JSONL、卡片输出、tmux 调试快照和脱敏：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/security ./internal/audit ./internal/card ./internal/tmux ./cmd/lark-agent-bridge
```

验证点：

- `token`、`secret`、`password`、`Authorization: Bearer` 等敏感字段被替换为 `[REDACTED]`
- serve 审计 recorder 能创建 JSONL 文件
- JSONL 中不出现原始敏感值
- 卡片事件文本和本地 `simulate` 输出的 tmux 命令快照不出现原始敏感值

## 卡片验证

使用 `internal/card` 单测验证长文本分页：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/card
```

使用 `internal/card` 单测验证飞书卡片 payload 中普通文本、思考过程、工具调用和错误输出会渲染为独立 markdown block：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/card -run TestBuildLarkCardFormatsRichSegments
```

使用 `internal/bridge` 单测验证流式输出会按 `CardMaxChars` 分页：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/bridge -run TestPollSessionOutputPaginatesLongStreamEvents
```

本地模拟长流式输出分页：

```bash
E2E_CARD_MAX_CHARS=40 GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "$(printf '%*s' 200 '' | tr ' ' x)"
```

期望输出包含 `page 5/5`，且每页 segment 长度不超过 `E2E_CARD_MAX_CHARS`。

Feishu CardKit 路由复用验证：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/feishu -run TestCardKitRouterRendererReusesSessionForPagedStream
```

期望分页 stream 事件使用同一个 `SessionID` 更新同一张卡片，后续页不需要新的 `ReplyToMessageID`。

后续接入真实 CardKit 后，需要 E2E 检查：

- 流式文本更新
- 思考过程富文本样式
- 工具调用富文本样式
- 底部 model/token/workdir/status
- 停止按钮点击后，原流式卡片尽量更新为 disabled「已停止」，并额外出现一张 terminal「已停止」确认卡
- 长输出分页或折叠

## 飞书 E2E 验证

真实飞书接入完成后，按以下流程验证：

1. 配置 `LARK_APP_ID` 和 `LARK_APP_SECRET`，不要在日志中打印真实值；`LARK_BOT_OPEN_ID` 不必配置，`serve` 会通过 `bot/v3/info` 自动查询。
2. 执行前置检查：`LARK_APP_ID=... LARK_APP_SECRET=... ./scripts/e2e-preflight.sh`。该脚本只验证本地依赖、环境变量存在、长连接 action/CardKit 单测和本地行为证据链，不验证凭据真实性。
3. 启动 bridge：`lark-agent-bridge serve`。当前 `serve` 已接入飞书 SDK 长连接、`card.action.trigger` 按钮入口、CardKit renderer、reaction 分流和 tmux 输出轮询；`E2E_CALLBACK_ADDR` 仅用于本地兼容 HTTP callback 调试。
4. 在单聊发送普通 prompt，确认默认进入 `claude`。
5. 在群聊直接发送消息，确认未 @ 不响应。
6. 查询 bot open_id：使用 bridge app 凭据调用 `bot/v3/info`，只记录 open_id，不打印 token 或 secret。
7. 使用当前全局 `lark-cli` 的用户身份发送真实群聊 @bot 消息。示例：

```bash
set -a
source .lark-agent-bridge/e2e.env
set +a

BOT_OPEN_ID="ou_xxx_fr<FEISHU_MESSAGE_ID>_v3_info"
CONTENT=$(ruby -rjson -e 'bot=ARGV[0]; text=%Q{<at user_id="#{bot}"></at> /codex status}; print({text: text}.to_json)' "$BOT_OPEN_ID")
lark-cli im +messages-send --as user --chat-id "$E2E_E2E_CHAT_ID" --msg-type text --content "$CONTENT" --idempotency-key "lab-codex-status-$(date +%s)"
```

期望返回 `message_id`，bridge audit 记录 `run_input`，并创建/reply CardKit 卡片。该路径已验证可以触发真实 mention 事件；`lark-cli --user-id "$BOT_OPEN_ID"` 仍不适用于 P2P，因为当前 lark-cli app 与 bridge app 不同，会触发 `open_id cross app`。

8. 在群聊 @bot 并发送 `/codex status`，确认启动独立 codex 会话。
9. 开启话题模式，在同一群不同话题中发送消息，确认会话互相独立。
10. 触发 agent 授权请求，确认飞书卡片展示授权按钮，点击后能写回 agent stdin。
11. 触发 agent 选择题，确认飞书卡片展示选项按钮，点击后能写回 agent stdin。
12. 查询中点击「停止」，确认本轮中断，audit 出现 `card_action stop`、原卡 `cardkit_update event=stop_button`、terminal `cardkit_terminal_create`/`cardkit_terminal_reply`，Feishu Web 里只剩 disabled「已停止」按钮，且后台工具进程不再存在。
13. 指定不存在的 `--workdir`，确认飞书卡片展示创建/取消按钮；120 秒不操作时应默认取消，不创建目录。
14. 执行 `/attach`，在终端 attach 到同一 tmux window，手动输入后确认飞书卡片同步显示输出。
15. 执行 `/stop`，确认 agent 进程与 tmux window 清理。
16. 退出 bridge 主进程，确认 `lark-agent-bridge` tmux session 被清理。

未配置 `LARK_APP_ID` 或 `LARK_APP_SECRET` 时，`serve` 应直接失败并提示缺少必要凭据，不应打印任何密钥值。

## 证据链要求

每次交付需要记录：

- `go test ./...` 结果
- `./scripts/verify.sh` 结果
- `./scripts/evidence.sh` 生成的报告路径；默认报告位于 `.cache/evidence/`
- `./scripts/e2e-preflight.sh` 结果；如果未执行，说明缺少哪些环境或外部条件
- `doctor` 输出摘要
- 至少一条 `simulate` 输出摘要
- 若执行真实 tmux，记录创建、输入、停止和清理结果
- 若执行飞书 E2E，记录每个步骤的消息、卡片状态和按钮行为

生成标准证据报告：

```bash
./scripts/evidence.sh
```

该脚本默认执行本地验证、ignore 检查、git 状态收集和资源状态检查，并把真实飞书 E2E preflight 记录为通过或 pending。资源状态检查会确认没有 `lark-agent-bridge serve` 进程残留；若当前环境能访问 tmux socket，也会确认没有固定 bridge/smoke tmux session 残留。报告写入 `.cache/evidence/`，不会进入 git。

如果需要把真实 tmux smoke 纳入报告：

```bash
RUN_TMUX_SMOKE=1 ./scripts/evidence.sh
```

如果需要探测本机真实 agent 交互首屏状态：

```bash
RUN_AGENT_PROBE=1 ./scripts/evidence.sh
```

`scripts/agent-probe.sh` 会用临时 tmux session 启动 `claude` 和 `codex`，捕获首屏并分类为 `ready`、`needs_trust`、`not_logged_in` 或 `unknown`，随后立即清理 session。默认只记录状态；如果要把非 ready 视为失败，执行：

```bash
RUN_AGENT_PROBE=1 REQUIRE_AGENT_READY=1 ./scripts/evidence.sh
```

如果需要强制真实飞书 E2E preflight 通过：

```bash
REQUIRE_E2E=1 LARK_APP_ID=... LARK_APP_SECRET=... ./scripts/evidence.sh
```
