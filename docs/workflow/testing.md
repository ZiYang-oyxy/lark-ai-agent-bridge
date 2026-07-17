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

该脚本覆盖：

- `go test ./...`
- `doctor`
- `/help`、`/new`、`/status` 命令面
- `/resume`、`/codex` 等撤回命令不再开放
- 群聊未 @ 过滤
- durable snapshot restore：只恢复 context，清理 pending；`debouncing`/`queued`/`starting` 变为 `cancelled`，`running` 变为 `interrupted`
- duplicate delivery skip，且重复输入不推进 snapshot revision
- 单 scope queue full 拒绝且不记录 receipt
- 同 chat/topic 串行、不同 topic 并行；busy scope 的兼容输入合并为下一批，而不是每条排队输入各跑一次
- stop 和 recall 保留/移除队列输入的对应生命周期
- 普通文本在根 chat 和 topic 中都续接当前 scope，并可在 DM `250ms` / group `600ms` cohort 内合并
- 只有 `/new` 重置当前会话并形成独占 batch boundary
- Claude one-shot 命令构造和 stream-json 解析
- CardKit 流式更新、标题颜色和 `⏱` 耗时、分栏底部状态栏、折叠面板、停止按钮、工作目录确认按钮
- 工作目录创建、取消和超时
- `/new --workdir` 的 Claude 子进程 `pwd` 和 `$PWD`
- 长连接 `card.action.trigger` action 解析
- 未设置飞书凭据时 `serve` 明确拒绝启动

若当前环境已经配置飞书应用凭据，可开启严格模式：

```bash
REQUIRE_LARK=1 ./scripts/verify.sh
```

## Doctor

执行：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge doctor --default-workdir /tmp/lark-agent-bridge
```

doctor 应检查：

- `claude` 是否存在
- `LARK_APP_ID` 是否设置
- `LARK_APP_SECRET` 是否设置
- 默认 agent
- 默认工作目录是否存在且是目录
- 审计日志路径是否可创建和写入
- `E2E_CALLBACK_ADDR` 是否设置；未设置也允许通过，表示按钮走长连接
- `E2E_CARD_UPDATE_MS`
- `E2E_INTERACTION_TIMEOUT_SEC`
- `E2E_CARD_MAX_CHARS`

审计日志默认写入 `<workdir>/.lark-agent-bridge/audit.jsonl`，可通过 `E2E_AUDIT_LOG=/path/to/audit.jsonl` 覆盖。日志为 JSONL 格式，`detail` 字段会先脱敏。

## 本地模拟消息

不连接飞书，仅模拟一条飞书消息：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/new hello"
```

输出 JSON 包含：

- `events`：卡片事件
- `audit`：审计事件

模拟群聊未 @ 机器人：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -group=true -mentioned=false -text "hello"
```

期望没有事件输出。

模拟 topic 内续接：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate \
  -thread topic-a \
  -text "/new first" \
  -next-text "second"
```

期望第二条消息进入同一个 `claude:chat-demo:thread:topic-a` 会话。

模拟同一会话排队：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/new first" -next-text "/new second"
```

期望第二条消息输出 `reaction` 事件，`Message` 为 `queued`。

模拟工作目录不存在：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate \
  -text "/new --workdir /tmp/missing-for-test hello"
```

期望输出 `workdir_confirm` 事件，并包含 `create_workdir` 和 `cancel_workdir` 动作；此时不应启动 runner。

模拟工作目录确认超时：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate \
  -text "/new --workdir /tmp/missing-for-timeout hello" \
  -timeout-now
```

期望先输出 `workdir_confirm`，再输出 `action` 事件 `workdir creation timed out: cancelled`；此时不应创建目录，也不应启动 runner。

模拟点击创建目录并恢复原请求：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action \
  -prime-text "/new --workdir /tmp/lark-agent-bridge-confirm-test hello" \
  -action create_workdir \
  -value /tmp/lark-agent-bridge-confirm-test
```

期望先输出 `workdir_confirm`，再输出绿色 `workdir_created` 终态卡片且两个按钮 disabled；随后使用独立 run card 输出 `stream/result`，运行中卡片 header 为 blue，最终卡片 header 为 green。确认卡片不应被运行卡片覆盖。

模拟点击取消创建目录：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action \
  -prime-text "/new --workdir /tmp/lark-agent-bridge-cancel-test hello" \
  -action cancel_workdir \
  -value /tmp/lark-agent-bridge-cancel-test
```

期望先输出 `workdir_confirm`，再输出灰色 `workdir_cancelled` 终态卡片且两个按钮 disabled；目录不应被创建，也不应启动 runner。

模拟停止按钮：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action \
  -prime-text "/new long" \
  -session claude:chat-demo:message:local-id \
  -action stop
```

实际 session id 需要与执行中卡片的 `SessionID` 一致。单测 `TestServiceStopCancelsActiveOneShotRun` 覆盖 stop action 会取消 active run，并把同一卡片更新为灰色 stopped 状态；action 返回值也应包含同步终态 card payload。

本地单测还应覆盖：

- running 标题类似 `🧠 正在推理 · ⏱ 3s`，completed/stopped/failed 标题不再包含“已执行”或“总耗时”。
- 底部状态栏先出现分割线，再出现两行 `column_set`。
- 第一行包含 `🤖 Claude`、model、`🔢 tokens: ▶ 本轮 / ∑ 累计`，列权重为 `10:14:18`。
- 第二行包含 `👤 user`、`🖥️ ip`、`📁 workdir`，列权重为 `10:12:30`。
- 底部状态栏不包含 `agent=`、`model=`、`workdir=`、`status=`。
- completed/failed/stopped 的停止按钮均为灰色 disabled，文案分别是“已完成”“已结束”“已停止”。
- `workdir_created` 和 `workdir_cancelled` 的两个工作目录按钮均为 disabled，且不再携带 callback behavior。
- `/new --workdir <path>` 下 fake Claude 进程看到的 `pwd` 和 `$PWD` 都等于 `<path>`。
- 同一会话排队输入 dequeue 后仍使用各自输入携带的 workdir。
- 最终 result 只替换自身带回来的正文/思考/工具分区，保留流式阶段已解析到但最终 result 缺失的思考或工具内容。

## Durable Session 与队列验证

`E2E_SESSION_STORE` 默认位于 `<default-workdir>/.lark-agent-bridge/sessions.json`。本地验证必须覆盖以下恢复边界：

- restart 仅恢复 context（例如 Claude session id），不自动重放旧命令。
- pending 不会恢复到 scheduler：`debouncing`、`queued`、`starting` 恢复为 `cancelled`，`running` 恢复为 `interrupted`。
- 同一 scope 的运行时队列可按 debounce 合并，因而“第二条已排队”不代表会获得独立 Claude 子进程。
- 个人版没有全局 semaphore/FIFO/公平性；验证只要求同 scope 串行及不同 scope 并行。
- `/resume` 必须保持禁用；恢复后的后续普通消息由内部 Claude `--resume <stored session id>` 续接。

`./scripts/verify.sh` 选择精确 Go 测试覆盖上述 snapshot restore、duplicate skip、queue full、busy merge、scope 串/并行和 `/resume` 禁用行为。真实飞书生命周期覆盖见下节与 `docs/workflow/e2e-real.md`；本地脚本检查不会连接飞书。

## 飞书 E2E 前置检查

载入真实配置：

```bash
set -a
source .lark-agent-bridge/e2e.env
set +a
./scripts/e2e-preflight.sh
```

该脚本检查：

- `LARK_APP_ID`、`LARK_APP_SECRET`
- doctor
- 长连接 action 和 CardKit 单测
- `REQUIRE_LARK=1 ./scripts/verify.sh`

不需要配置公网 callback URL。按钮 E2E 依赖飞书后台选择“使用长连接接收回调”，并订阅 `card.action.trigger`。

## 真实飞书 E2E 方法

真实 E2E 已固化为脚本化入口，完整流程见 `docs/workflow/e2e-real.md`：

```bash
./scripts/e2e-real.sh --list-cases
./scripts/e2e-real.sh --mode smoke
./scripts/e2e-real.sh --mode full
```

下面保留手动排查步骤，便于脚本失败时定位。

1. 载入 `.lark-agent-bridge/e2e.env`。
2. 启动 bridge：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge serve --default-workdir /tmp/lark-agent-bridge-e2e
```

3. 用 bridge app 的 `bot/v3/info` 查询 bot open_id，或从 serve 日志/audit 中确认。
4. 用本地 `lark-cli --as user` 作为用户身份向目标群发送 raw `<at>` 消息：

```bash
lark-cli im +messages-send --as user \
  --chat-id "$E2E_E2E_CHAT_ID" \
  --msg-type text \
  --content '{"text":"<at user_id=\"BOT_OPEN_ID\"></at> /new 请回复 E2E_E2E_HELLO"}'
```

5. 预期 bridge 长连接收到消息，飞书中出现执行中卡片，完成后同一卡片更新为结果。
   - audit 中应先出现 `cardkit_create`/`cardkit_reply`，再出现一个或多个 `cardkit_update event=stream`，最后出现 `cardkit_update event=result`。
   - 执行中卡片标题应为蓝色 `正在推理/正在执行工具/正在回复 · ⏱ Ns`。
   - 最终卡片标题应为绿色 `已完成 · ⏱ Ns`。
   - 底部状态栏应以分割线开头，分两行展示 agent/model/tokens 和 user/ip/workdir，不包含 status。
   - 最终 completed 卡片的停止按钮应变为灰色 disabled “已完成”。
6. 发送长任务后点击卡片“停止”，预期：
   - 长连接收到 `card.action.trigger`
   - audit 记录 `card_action stop`
   - Claude 子进程被取消
   - 同一卡片标题更新为灰色 `⏹ 已停止 · ⏱ Ns`
   - 卡片按钮置灰为“已停止”，且不会额外发送新的停止结果卡片
7. 使用不存在的 `--workdir` 发送 `/new`，点击“Create directory”或“Cancel”，预期目录创建/取消行为与卡片状态一致：
   - create 后确认卡绿色、按钮 disabled，随后出现独立运行卡片。
   - cancel 后确认卡灰色、按钮 disabled，不创建目录，不启动 Claude。
   - 运行卡片底部 `📁` 显示指定 workdir。
8. 验证话题续聊时，使用用户态 lark-cli 对原始 `/new` 消息做 thread reply，并在群话题内继续 @bot：

```bash
lark-cli im +messages-reply --as user \
  --message-id "<root_message_id>" \
  --reply-in-thread \
  --text '<at user_id="BOT_OPEN_ID"></at> 请继续当前话题会话'
```

当前飞书事件权限下，群话题内不 @bot 的普通文本不会推送到 bridge；可作为负向验证。带 @ 的话题回复应进入 `chat_id + thread_id` 对应会话，并创建新的执行卡片。

`debounce_dm` 会使用 `lark-cli im +messages-send --as user --user-id "$BOT_OPEN_ID"` 并发发送两条真实 P2P 普通消息。若当前用户态 `lark-cli` 与 bridge app 的 open_id 域不兼容，case 必须非零失败并保留 `dm-pair-*.err`，不得回退到群聊冒充 DM；此时需改用同 bridge app 的用户 OAuth profile 或有效 P2P user id 后重跑。

## 证据报告

生成本地证据报告：

```bash
./scripts/evidence.sh
```

带真实飞书前置检查：

```bash
set -a
source .lark-agent-bridge/e2e.env
set +a
REQUIRE_E2E=1 ./scripts/evidence.sh
```

报告写入 `.cache/evidence/`，不会进入 git。summary 至少应包含：

- `local evidence: passed`
- `resource status: clean`
- `Feishu E2E preflight: passed`，如果设置了 `REQUIRE_E2E=1`
