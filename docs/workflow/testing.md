# Testing Workflow

> **工作流入口（何时跑什么）见 [`regression.md`](regression.md)：**
> - 提交后默认：`scripts/smoke-local.sh`（秒级，`SMOKE_LOCAL_OK`）
> - 可选上机：容器仓 skill 的 `smoke-on-server.sh --rc <已发布RC>`（`SMOKE_OK`）
> - 发版门禁：`scripts/release-regression.sh --profile <p>`（L0→L3，`RELEASE_REGRESSION_OK`）

## 本地基础验证

执行单元测试：

```bash
GOCACHE=$PWD/.cache/go-build go test ./...
```

执行本地一键验证：

```bash
./scripts/verify.sh
```

`verify.sh` 和 `scripts/release.sh` 会在自身进程中清除 `E2E_PREFERENCE_STORE`、`E2E_REPLY_STORE`、
`E2E_MEDIA_CACHE_DIR` 和 `E2E_SESSION_STORE`。这些变量属于 serve/supervisor 的
durable runtime 路径，不应改变默认路径单测；脚本不会删除或修改变量原本指向的数据。

## 发布 L1 测试凭证

`./scripts/release.sh tag <version>` 会对最终 clean `HEAD` 执行一次发布 L1，并在 git common
dir 的 `release-state/test-evidence/` 原子写入私有 JSON 凭证和测试日志。也可单独执行：

```bash
go run ./cmd/lark-bridge-release test-evidence ensure
```

凭证同时绑定 commit、tree、测试 suite、环境隔离契约，以及 Go launcher、实际选中的
Go binary/compiler/linker SHA256、版本和
关键 `go env`。完全匹配时输出 `TEST_EVIDENCE_REUSED`；缺失、日志 SHA 不符、工具链变化或
契约升级时重新执行并输出 `TEST_EVIDENCE_CREATED`。脏 worktree 直接拒绝，失败运行只保留
诊断日志，不写成功凭证；同一 fingerprint 的并发调用由锁收敛为一次测试。

该脚本覆盖：

- `go test ./...`
- `doctor`
- `/help`、`/new`、`/status`、`/stop` 命令面
- `/cron`、`/timer` 管理命令、自然语言 proposal、确认/取消和作用域鉴权
- `/resume`、`/codex` 等撤回命令不再开放
- 群聊未 @ 过滤
- durable snapshot restore：只恢复 context，清理 pending；`debouncing`/`queued`/`starting` 变为 `cancelled`，`running` 变为 `interrupted`
- duplicate delivery skip，且重复输入不推进 snapshot revision
- 单 scope queue full 拒绝且不记录 receipt
- 同 chat/topic 串行、不同 topic 并行；busy scope 的兼容输入合并为下一批，而不是每条排队输入各跑一次
- 卡片 stop 与文本 `/stop` 的 scope/agent 隔离，以及 stop 和 recall 保留/移除队列输入的对应生命周期
- 普通文本在根 chat 和 topic 中都续接当前 scope，并可在 DM `250ms` / group `600ms` cohort 内合并
- 只有 `/new` 重置当前会话并形成独占 batch boundary
- Claude one-shot 命令构造和 stream-json 解析
- Codex `exec --json`/resume 精确 argv、stdin prompt、`cx*` executable/`CODEX_HOME` 冻结、JSONL 解析、图片 `--image` 和协议漂移 audit
- CardKit 流式更新、标题颜色和 `⏱` 耗时、分栏底部状态栏、折叠面板、停止按钮、工作目录确认按钮
- CardKit 最终 JSON 的 28 KiB / 200-component 容量闸门、UTF-8 多字节测量、分级压缩、静态 emergency fallback，以及 Create/Update 和两种 callback transport 的 prepared-card 边界
- 工作目录创建、取消和超时
- `/new --workdir` 的 Claude 子进程 `pwd` 和 `$PWD`
- 长连接 `card.action.trigger` action 解析
- 未设置飞书凭据时 `serve` 明确拒绝启动
- schedule snapshot 原子持久化、确定性 run ID、重启 catch-up、overlap skip、队列拥塞重试、执行超时和历史清理

若当前环境已经配置飞书应用凭据，可开启严格模式：

```bash
REQUIRE_LARK=1 ./scripts/verify.sh
```

定时功能的聚焦验证：

```bash
GOCACHE=$PWD/.cache/go-build go test ./internal/schedule ./internal/bridge -run 'Schedule|Cron|Timer' -count=1
GOCACHE=$PWD/.cache/go-build go test -race ./internal/schedule ./internal/session ./internal/bridge ./internal/card
```

Agent-facing proposal CLI 必须由 `serve` 注入 `LAB_SCHEDULE_SOCKET` 和单次 `LAB_SCHEDULE_TOKEN`；不要在 shell 中长期配置这两个变量。端到端测试应从飞书发送自然语言请求，核对确认卡的规则与未来三次时间，确认后用 `/timer run <id>` 验证真实 Agent 输出回到原 chat/topic，再重启 bridge 验证任务和运行历史仍可查询。

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
- `E2E_CARD_HEARTBEAT_SEC`
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

模拟空闲会话的文本停止命令：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/stop"
```

期望返回 `当前会话没有正在运行的任务。`，且不启动 Agent。active batch 的取消、topic/agent 隔离和 queued 输入保留由 `TestServiceTextStop*` 覆盖。

active batch 存在时，有效 `/stop` 不回复新的命令卡片；原任务卡片进入灰色 stopped 终态，并在已有正文尾部追加 `已请求停止当前任务；排队输入将继续执行。`。带参数的 `/stop` 仍单独回复用法错误。

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

2026-07-18 最终 integration 版本的串行核心回归位于 `.cache/evidence/dee05c5/core-regression-green/summary.md`:session context restart、queued cancel、running interrupted、DM/group debounce、busy merge、queue full、scope parallel、stop-preserves-queue、recall-state 十个 case 全部 passed。

## 飞书 E2E 前置检查

每位开发者先使用自己的 app、bot 和测试群创建本地命名 profile：

```bash
./scripts/e2e-init.sh --profile personal
./scripts/e2e-real.sh --profile personal --doctor
./scripts/e2e-real.sh --profile personal --preflight-only
```

profile 和 evidence 位于 `/.lark-agent-bridge/`、`/.cache/`，均已 Git ignore。真实 `App ID`、`App Secret`、bot/chat/message ID 和 OAuth 元数据不得写入 tracked 文件。

每个命名 E2E profile 同时绑定独立的 `lark-cli --profile lab-e2e-<name>`；runner 不切换全局默认 profile，避免多个开发者或并行会话互相覆盖 App 配置和用户 OAuth。

`--doctor` 不发送消息、不启动 bridge，检查：

- `LARK_APP_ID`、`LARK_APP_SECRET`
- `lark-cli` 用户 OAuth 与 app identity
- 用户 token 是否实际拥有 `im:message`（`lark-cli im +messages-send` 的实际 scope）；App 未启用该权限时返回 `BLOCKED:user_message_scope_missing`
- bot、测试群；P2P chat 缺失时仅标记 DM/文件用例为 `BLOCKED`，不阻塞群聊主链
- Claude wrapper
- 本地 profile 是否已有 active owner

`--preflight-only` 使用 fake Claude，额外验证真实 group/DM delivery、CardKit callback、recall API/event 和 media image/file 链路。结果分为 `PASS`、`FAIL`、`BLOCKED`、`SKIPPED`；发布门禁使用 `--strict-capabilities`。

不需要配置公网 callback URL。按钮 E2E 依赖飞书后台选择“使用长连接接收回调”，并订阅 `card.action.trigger`。

## 真实飞书 E2E 方法

真实 E2E 已固化为脚本化入口，完整流程见 `docs/workflow/e2e-real.md`：

```bash
./scripts/e2e-real.sh --list-cases
./scripts/e2e-real.sh --profile personal --mode smoke
./scripts/e2e-real.sh --profile personal --mode full --strict-capabilities
```

`e2e-real.sh` 的 bridge readiness 只观察本次启动后 serve 日志新增的 `connected to wss`,不依赖 `/card/callback`。只有选中 stop/config 等 action case 时才注入 `E2E_CALLBACK_ADDR` 并在 gateway 注入 action 前做 callback challenge 探针；非 action-only run 显示 `callback_addr: disabled`。本地 callback 产生的 action 证据属于 `gateway_injected`,不代表已验证飞书 `card.action.trigger` 平台投递。真实飞书 `new_basic` 已验证该 readiness 通常约 2 秒内完成,并能继续走到 CardKit `event=result`。

下面保留手动排查步骤，便于脚本失败时定位。

1. 选择一个不会与其他开发会话共享 bot 的命名 profile。
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
   - 已有正文保持不变，尾部追加 `已请求停止当前任务；排队输入将继续执行。`
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

DM/group debounce、topic scope 与无 @ 过滤已迁到 L1 service/parser tests,不再占用真实飞书回归窗口。

### Media 输入真实 E2E

Media case 必须设置 `E2E_REAL_E2E_FAKE_CLAUDE=1` 和 `E2E_REAL_E2E_P2P_CHAT_ID`。后者是当前测试用户与该 bot 的真实 P2P `chat_id`,用于发送飞书原生 `file` 消息;群聊 `post` 不支持 `{tag:file}`。

```bash
./scripts/e2e-real.sh \
  --case media_attachment_only --case media_images --case media_text_files
```

验收要求:

- JPEG/PNG/WebP/GIF 和 `.txt/.md/.json/.csv` 的 SHA-256 cache path 出现在 fake Claude prompt。
- evidence 目录至少包含 `summary.md`、`audit.jsonl`、`messages.jsonl`、`fake-claude.log` 和 `mget/*.json`。

mixed/partial、forged MIME、oversized、PDF/DOCX/audio/未知二进制拒绝和失败卡隔离均由 L1 media/service tests 覆盖。

### Runtime Config L1

Runtime Config 已迁到 config/service/doctor L1 tests,用 fake renderer 与 fake Claude 确定性校验 form action、持久化、argv、actual model 和 wrapper preflight,不再占用真实飞书窗口。

验收要求:

- `default/sonnet/opus/haiku` 与一个 `E2E_ALLOWED_MODELS` 扩展值均完成 save→persist→下一次 argv 闭环;`default` 不产生对应 CLI flag。
- `default/low/medium/high` 四种 effort 均覆盖;非法 callback 返回错误卡并写 `config_save_failed`,且不得覆盖最后一次有效偏好。
- queued input 使用入队时冻结的 model/effort;后保存的偏好只影响后入队输入。
- `/config reset` 后 snapshot 无 override;restart 后重新读取 `E2E_MODEL`/`E2E_EFFORT` 环境默认。
- 结果卡分别显示 requested、actual、effort;requested/actual 不一致时必须有 `model_requested_actual_mismatch` audit。
- `doctor --strict` 的 wrapper preflight 通过,argv 为 bounded harmless one-shot,输出不含 secret 或 wrapper 返回内容。

2026-07-18 的证据位于 `.cache/evidence/e8cca6b/config-real/summary.md`,五个 case 均为 passed;补充的 `REQUIRE_E2E=1 ./scripts/evidence.sh` 报告为 `.cache/evidence/evidence-20260718-111543.md`。

### Reply Experience 分层

append/clean/latest policy、preview threshold 与 reaction 生命周期已迁到 L1。L2 只保留跨进程 stale CardKit mapping 场景:

```bash
E2E_REAL_E2E_FAKE_CLAUDE=1 \
./scripts/e2e-real.sh \
  --case latest_restart_fallback
```

验收要求:

- `append` 两轮创建两张卡;`append-clean-card` 终态只保留结果;`latest-card` 同 scope 复用同一 card ID 并递增 sequence。
- preview 同时受 interval/min-delta 限制,中间内容截断但终态完整。
- `OneSecond` 和 `Typing` 在任务结束后均被删除;真实链路超过等待阈值时,快速任务允许短暂出现 `OneSecond`,但不允许残留。
- 重启把 queued/starting 终结为 cancelled、running 终结为 interrupted,用户可见卡显示“服务重启,已中断,请重新发送”。
- latest mapping 指向无效 card ID 时必须清除旧 mapping、新建卡并完成 result;飞书 `10002 cardid invalid` 属于 stale mapping,不能当作普通 render failure。

2026-07-18 的最终汇总证据为 `.cache/evidence/dee05c5/reply-final-summary.md`;它链接三段原始 evidence:reply modes/preview、reaction 定向重跑、restart stale fallback 定向重跑。最终六个 case 均为 passed,首轮失败现场未覆盖或删除。

独立 reviewer 后的增量门禁位于 `.cache/evidence/db4e556/reviewer-final-summary.md`:preview 在取得渲染锁后复检 generation/closed,不会晚于 final 覆盖终态;整批 recovery 卡片更新共享 5 秒 context budget,不会无限阻塞 WSS 启动。对应 `preview_thresholds` 与 `latest_restart_fallback` 均重新通过。

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
