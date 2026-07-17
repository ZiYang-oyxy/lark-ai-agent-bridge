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
- 同 chat/topic 串行排队
- 不同 topic 并行
- topic 普通文本续接内部 Claude session
- `/new` 重置当前会话
- Claude one-shot 命令构造和 stream-json 解析
- CardKit 富文本、折叠面板、停止按钮、工作目录确认按钮
- 工作目录创建、取消和超时
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

期望先输出 `workdir_confirm`，再输出 `action`，最后输出 `stream/result`。

模拟停止按钮：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action \
  -prime-text "/new long" \
  -session claude:chat-demo:message:local-id \
  -action stop
```

实际 session id 需要与执行中卡片的 `SessionID` 一致。单测 `TestServiceStopCancelsActiveOneShotRun` 覆盖 stop action 会取消 active run 并置灰按钮。

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
6. 发送长任务后点击卡片“停止”，预期：
   - 长连接收到 `card.action.trigger`
   - audit 记录 `card_action stop`
   - Claude 子进程被取消
   - 卡片按钮置灰为“已停止”
7. 使用不存在的 `--workdir` 发送 `/new`，点击“Create directory”或“Cancel”，预期目录创建/取消行为与卡片状态一致。

单聊 E2E 暂缓。当前用户态 `lark-cli` 与 bridge app 不同，直接按 bot open_id 发送 P2P 可能触发 `open_id cross app`；后续需要同 bridge app 用户 OAuth profile，或手动建立 P2P 后记录 chat_id。

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
