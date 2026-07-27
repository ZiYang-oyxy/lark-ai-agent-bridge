# Bridge 测试框架

本文说明当前 bridge 的测试体系如何组织、每一层的边界与运行方式，以及它与发布门禁的关系。

真实飞书 E2E 的环境准备与操作细节仍以 [testing.md](testing.md) 为准；`scripts/e2e-real.sh` 的用例矩阵见 [e2e-coverage-matrix.md](e2e-coverage-matrix.md)；发布回归 wrapper 见 [regression.md](regression.md)；测试框架的建设方案保留在 [../plans/2026-07-27-comprehensive-test-framework.md](../plans/2026-07-27-comprehensive-test-framework.md)。

## 结论

当前体系分四层，越往下越贵、越接近真实链路。任一改动都从最低层开始向上验证，直到覆盖到风险边界为止：

- **L1 · Go 单元测试**：`go test ./...` 全仓包内纯逻辑、状态机、渲染契约。
- **L2 · YAML simulate 回归**：`internal/testfw` 里的 runner + `cmd/lark-agent-bridge simulate`/`simulate-action`，本地 fake AgentRunner 秒级完成，覆盖跨命令行为与 prompt 拼装。
- **L3a · self-loop 手工 L3**：`rebuild-test.sh` 换血 Test bot 到本需求 worktree，然后用 `lark-cli` 从 supervisor 用户身份手工发消息、观察 audit + 回读卡片。适合逐个 bug 快速定验证。
- **L3b · e2e-real 自动化 canary**：`scripts/e2e-real.sh` 起独立 bridge server（真 App + 真 wss）+ 真 Claude/Codex wrapper，按用例矩阵批量跑，产出 capabilities & summary。是发布回归的顶层。

`scripts/release-regression.sh` 把 L0（`verify.sh`）+ L1 race + L2 e2e-real full（真飞书 + fake Agent）+ L3b canary（真飞书 + 真 Claude）串成一次完整回归；发布凭证 `test-evidence ensure` 单独把 L1 + testfw regression sidecar 作为 publish 的强制门禁。

```mermaid
flowchart TD
    A[功能改动]:::primary
    B[L1 Go 单元测试]:::success
    C[L2 YAML testfw]:::primary
    D[simulate]:::grey
    E[simulate-action]:::grey
    F[Smoke 套件]:::success
    G[Regression sidecar]:::warning
    H[test-evidence 门禁]:::danger
    I[L3a self-loop 手工]:::warning
    J[L3b e2e-real canary]:::warning
    K[release-regression 全量]:::danger

    A --> B
    A --> C
    C --> D
    C --> E
    D --> F
    E --> F
    C --> G
    B ==> H
    F ==> H
    G ==> H
    A -.真链路快查.-> I
    A --> J
    B --> K
    F --> K
    J ==> K
    H ==> Publish[/publish.sh/]:::danger
    K ==> Release[/正式版发布/]:::danger

    classDef primary fill:#6C9BD2,stroke:#5B8AC1,color:#fff
    classDef success fill:#7EC699,stroke:#6DB588,color:#fff
    classDef warning fill:#F0C27A,stroke:#DFB169,color:#fff
    classDef danger  fill:#E8918C,stroke:#D7807B,color:#fff
    classDef grey    fill:#B0B5BD,stroke:#9FA4AC,color:#fff
```

## L1 — Go 单元测试

`go test ./...` 覆盖 29 个 package 的纯逻辑、状态机、渲染契约、协议解析。运行时钟秒级。发布凭证阶段用它作为核心门禁。

```bash
env -u E2E_PREFERENCE_STORE -u E2E_REPLY_STORE \
    -u E2E_MEDIA_CACHE_DIR  -u E2E_SESSION_STORE \
    GOCACHE=$PWD/.cache/go-build go test ./...

# 本地快速迭代:跳过 smoke integration
env -u E2E_PREFERENCE_STORE -u E2E_REPLY_STORE \
    -u E2E_MEDIA_CACHE_DIR  -u E2E_SESSION_STORE \
    GOCACHE=$PWD/.cache/go-build go test -short ./...
```

`E2E_*` 清理是必需的：这些环境变量是 supervisor serve 时的 durable state 路径，遗留会让默认路径测试读到生产数据。

`internal/testfw/smoke_suite_test.go::TestSmokeSuite` 把 `tests/smoke/` YAML 套件挂进 `go test ./...`——完整模式会执行、`-short` 会跳过。因此单纯跑 `go test ./...` 就已经同时覆盖了 L1 与 L2 smoke。

## L2 — YAML simulate 回归

框架代码在 `internal/testfw`，可执行入口是 `cmd/lark-bridge-test`。runner 调用 `cmd/lark-agent-bridge simulate` / `simulate-action`，捕获其 JSON 输出的 `events` 与 `audit`，用 YAML 里的 assert 条目断言。**不连接飞书、不启动真 Agent**——fake AgentRunner 会把 `BuildBatchPrompt` 输出 echo 到 `result` event 的 text segment 里（前缀 `simulated answer: `），所以断言本质上是在验证 bridge 的**数据模型 + prompt 拼装**。

用例位于 `tests/smoke/`（6 个）和 `tests/regression/`（17 个）。标签以 OR 语义筛选：`--tags smoke,config` = 带 `smoke` 或 `config` 的用例；`--regression` = `--tags smoke,regression`，跑两类并集。

### 运行入口

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-bridge-test --smoke
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-bridge-test --regression
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-bridge-test \
    --tags config,session \
    --report-json /tmp/bridge-test-report.json
```

runner 给每个用例一份临时工作目录，把 `E2E_PREFERENCE_STORE` / `E2E_REPLY_STORE` / `E2E_MEDIA_CACHE_DIR` / `E2E_SESSION_STORE` 指向它，用例之间互不污染。

从同一 YAML 生成 L3 手工清单（不执行真实链路，仅投影为 Markdown 表格）：

```bash
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-bridge-test \
    --regression \
    --emit-l3-checklist /tmp/bridge-l3-checklist.md \
    --checklist-only
```

### 用例格式

一个 YAML 文件 = 一个 `TestCase`。步骤二选一：`input` 走 `simulate`（消息触发），`action` 走 `simulate-action`（卡片按钮触发）。二者并存以 `action` 为准。

```yaml
name: 示例:帮助页与卡片动作
description: 验证消息入口和 action 入口
tags: [smoke, help]
steps:
  - input: /help
    asserts:
      - {type: event_type, expected: help}
      - {type: segment_contains, text: /status}
  - action: help.open_config
    prime_text: /help
    asserts:
      - {type: event_type, expected: config}
      - {type: no_error}
```

**引用消息、群聊、mention、attachment 都可在 YAML 里模拟**：

- `group: true` + `mentioned: false/true` 覆盖群聊未 @ / 已 @ bot 分支。
- `quote_text` / `quote_sender` / `quote_sender_type` 注入被引用消息（内存 fake fetcher，不触发飞书 SDK）。用于验证引用主体身份边框（user / app / self_bot）和用户主指令前置的 prompt 顺序契约。
- `action` 步骤可用 `prime_text` 建会话，`value` / `chat_id` / `open_message_id` / `form_values` / `prime_is_group` / `prime_chat_id` 精确注入卡片上下文。
- `l3.skip: true` + `skip_reason` 显式排除该步骤进入 L3 清单——用于 L2 特有依赖分支（如 simulate 未装配 SessionStore/ScheduleStore 的早退路径），必须写明理由。

### 支持的断言

- **`event_type`**：首个 event 的 `Type`。空闲 `/stop` 的 message 类提示可按 `notice` 断言。
- **`segment_contains`**：**Events[0]** 的可见文本包含指定字符串。多帧场景（stream + result）注意 Events[0] 通常是无 segments 的 stream 头帧，此时应改用下一个。
- **`any_segment_contains`**：遍历**全部** event 的可见文本，任一命中即通过。适合断言 prompt / answer / 思考区。
- **`segments_order`**：把全部 event 的可见文本拼成一段，`texts` 里各元素必须**按序**依次出现（允许中间穿插）。倒序或缺段都失败。用于锁死 prompt 拼装顺序契约，例如"用户主指令必须先于引用块"。
- **`header_title`**：首个 event 的 `HeaderTitle` 包含指定字符串。
- **`has_button`**：`Event.Actions[]` 里存在指定 label 的按钮，或 StopButton 可见。**渲染期才生成的表单按钮**（ConfigForm / AgentModeForm）**不能在 L2 中断言**，改用 `event_type` 断言表单类型。
- **`stop_button_visible`** / **`stop_button_disabled`**：验证停止按钮生命周期。
- **`no_events`**：验证消息被正确过滤（如群未 @）。
- **`audit_empty`**：验证无 audit 事件；只有失败/拒绝/排队才写 audit，成功执行不写。
- **`no_error`**：拒绝 `_failed` / `_denied` / `_rejected` audit + `Kind: error` 的卡片片段。`_ignored` / `_unavailable` 不算错误。

### 诚实断言的边界

框架不会把整个 event 序列化为 JSON 兜底断言，也不会按 struct 是否非 nil 硬注入渲染期文案（"全局配置"、"保存" 等）。历史上这两条兜底导致 `/config` 表单卡在 L2 假通过、L3 才发现。现行契约：**断言只作用于真实承载可见文案的字段**——找不到就诚实失败，不糊弄。

## L3a — self-loop 手工 L3

适用场景：**改动只涉及 bridge 单点行为**（引用主体识别、prompt 顺序、卡片渲染的时序细节），需要在真实链路 + 真 Claude 下验一遍。走 `~/bridge/bridge-self-loop/rebuild-test.sh`：

```bash
bash ~/bridge/bridge-self-loop/rebuild-test.sh ~/bridge/worktrees/<需求 slug>
# 输出 REBUILD_TEST_OK pid=... sha256=...
```

脚本先跑 `l3_sender_preflight`：查 `lark-cli auth status`，硬校验当前 user 身份挂在期望 App 下、scope 含 `im:message.send_as_user`，不满足 fail-closed。然后编译传入的 worktree、停旧 Test、以生产态 env（真 claude wrapper、无 fixture）起新 Test、跑 `doctor --strict`。

之后用飞书能力（`lark-cli im +messages-send` / `+messages-reply` / `+messages-mget` + audit.jsonl）手工发消息 → 观察 audit → 读回复卡。GUIDE.md 里详列了服务器/Mac 各自的 chat_id、Test open_id、audit 位置、mention 规则等。

**关键契约**（引自 self-loop GUIDE.md）：

- **消息由本机 L3 sender 用 `--as user` 身份发**——sender 是**本机具备 `im:message.send_as_user` scope 的那个 App**。两台机器归属反过来：Linux 上 sender = Test App（`cli_fixture_d57513c150e2`），Mac 上 sender = supervisor App。
- **群聊消息必须用动态核验过的 Test open_id @ Test**（`+chat-members-list` 查），不要用 app_id。
- **回读用 `+messages-mget`** 拿真实卡片正文，audit 出现 `cardkit_create` + `cardkit_reply` 即链路通。

## L3b — e2e-real 自动化 canary

适用场景：**发布回归、批量能力覆盖**。`scripts/e2e-real.sh` 起独立 bridge server（真 App、真 wss），批量跑 `case_*` 用例；每个 case 自己发消息、等 audit、读回复、断言。

用例矩阵分三类：

- **`SMOKE_CASES`**：每次真实冒烟都跑（`new_basic`、`streaming_card`、`help`、`status`、`workdir_existing`、`topic_reply_at`、`topic_reply_without_at_negative` 等）。
- **`FULL_EXTRA_CASES`**：完整回归才跑，含重启、队列、媒体、回收、配置、reply mode、`quote_readback`、`wrapper_preflight` 等（30+ 条）。
- **`FEATURE_CASES`**：必须显式 `--case` 选中的能力验证（如 `group_message_intake`），因为会改变共享 bot 行为。

完整用例矩阵见 [e2e-coverage-matrix.md](e2e-coverage-matrix.md)；新增 case 的 SOP 见 [testing.md](testing.md)「新增回归用例 SOP」。

### profile 与运行

`e2e-real.sh` 通过 `--profile <name>` 索引 `~/.lark-agent-bridge/e2e/profiles/<name>.env`（或 `$STATE_ROOT/.lark-agent-bridge/e2e/profiles/`；`STATE_ROOT` 默认是 repo 根，可用 `E2E_STATE_ROOT` 覆盖）。profile 文件权限必须 `0600`、字段限于白名单：

- `LARK_APP_ID` / `LARK_APP_SECRET`：bridge server 起 wss 用的 App。
- `LARK_BOT_OPEN_ID`：bridge 判 @ 是否命中自己 + `send_at` 构造 `<at user_id=...>` 时用的 bot open_id。
- `E2E_E2E_CHAT_ID`：主测试群。
- `E2E_E2E_LARK_CLI_PROFILE`：`lark-cli --profile <name>` 发消息的身份。
- `E2E_REAL_E2E_P2P_CHAT_ID`（可选）：媒体/P2P case 用。

**这台机器的正确姿势（same-app）**：`LARK_APP_ID` 与 `E2E_E2E_LARK_CLI_PROFILE` 都指向 Test App。preflight 的 `oauth_same_app` 契约就是要求"bridge 与 sender 同 App"。**误解为要"两个不同 App"是历史踩坑**——只有 Mac Mike 那种"supervisor 是 sender、Test 是 bot"的天然对称才是双 App 模式；Linux 上没这个对称条件，走 same-app 才对。

**同 bot 在不同 App 视角下 open_id 不同**：Test bot 从 Steve/Mac Mike 视角看是 `ou_3c0a...`，从 Test App 自己视角看是 `ou_d84156fe...`。e2e-real profile 的 `LARK_BOT_OPEN_ID` 必须与 `E2E_E2E_LARK_CLI_PROFILE` 的 App 视角一致——用错视角会让所有消息的 `mention.id` 变成 `app_id`，bridge 的 `mentionsIncludeBot` 匹配不上，全被 `group_message_skipped(mention_required)` 掉。用 `lark-cli --profile <sender> im +chat-members-list <chat>` 动态核验。

### 运行入口

```bash
# 先停 Test 让出 App wss(same-app 下 Test 与 e2e-real bridge 争同一条连接)
kill -TERM "$(cat ~/.cache/lark-bridge-test/service.pid)"

# preflight only:静态检查 profile / scope / group 可读
E2E_STATE_ROOT="$HOME" ./scripts/e2e-real.sh --profile <name> --preflight-only

# 单 case
E2E_STATE_ROOT="$HOME" ./scripts/e2e-real.sh --profile <name> --case new_basic

# 完整回归(真飞书 + fake Claude,可加 --strict-capabilities 让 BLOCKED 立即失败)
E2E_STATE_ROOT="$HOME" ./scripts/e2e-real.sh --profile <name> --mode full --strict-capabilities

# 跑完恢复 Test
bash ~/bridge/bridge-self-loop/rebuild-test.sh ~/bridge/lark-ai-agent-bridge
```

每次运行产出结构化 summary：`~/.cache/e2e/<profile>/real-<run_id>/`，含 `summary.md`、`capabilities.json`、`audit.jsonl`、各 case 的 log 与 mget 卡片。

### capabilities 语义

preflight 会评估一组 capabilities（`credentials` / `oauth_same_app` / `lark_cli_auth` / `bot_identity` / `test_group` / `p2p_chat` / `wrapper` / `exclusive_runtime` / `card_action` / `media_image` / `media_file` / `recall_event_delivery` / …）。每个 capability 有 `PASS` / `BLOCKED` / `FAIL` / `SKIPPED` 四态。`BLOCKED` 表明外部前置缺失（如 P2P chat 未配置），`FAIL` 表明 canary 真的失败了。`--strict-capabilities` 让任何 `BLOCKED` 都视作硬失败。

## release-regression.sh — 全量回归编排

`scripts/release-regression.sh --profile <name>` 把上面各层串起来，作为发正式版前的完整回归：

1. **L0/L1**：`REQUIRE_LARK=1 ./scripts/verify.sh`——文档契约 + `go test ./...` + doctor + 命令面 simulate + smoke 套件 + 会话行为 + 卡片/长连接 action 等。
2. **L0/L1 race**：`go test -race ./internal/{schedule,session,bridge,card}`——重点 package 的并发正确性。
3. **L2**：`./scripts/e2e-real.sh --profile <name> --mode full --strict-capabilities`——真飞书 + fake Claude 完整能力矩阵。
4. **L3**：`env -u E2E_REAL_E2E_FAKE_CLAUDE ./scripts/e2e-real.sh --profile <name> --case new_basic`——真 Claude canary，默认 warn-only。加 `--strict-l3` 转硬 gate。

顶层输出 `RELEASE_REGRESSION_OK release=<ver> l0l1=passed l2=passed l3=<passed|warn>`。任一硬 gate 失败即非 0 退出。

**它不是 publish.sh 的强制门禁**——publish 只要求 `test-evidence`（下一节）。`release-regression.sh` 是转正式版前的额外保护网。

## 发布凭证 test-evidence 与 publish 门禁

`cmd/lark-bridge-release test-evidence ensure` 是 publish 的**强制门禁**，被 `publish.sh` 与 `release-bridge.sh` 自动调用：

- 在隔离环境跑完整 `go test ./...`。
- 随后跑 `lark-bridge-test --tags regression --report-json <sidecar>`，产出原子写入的 JSON sidecar。
- 记录 sidecar 的路径和 SHA-256、绑定 commit/tree、Go binary SHA、`go env` 关键项。
- 只接受 `all_passed: true`。哈希不匹配、sidecar 缺失、`all_passed=false` 都会让凭证失效，publish 拒绝执行。

这使 publish 自动覆盖 L1 + smoke integration + regression 三层，无需人工干预。**publish 不自动覆盖 L3**——L3 由 self-loop（L3a）或 release-regression.sh（L3b）另行执行。

## 新增或修改用例

1. **先确定风险层级**。纯逻辑 → L1 Go 单测；跨命令 / 卡片数据模型 / prompt 拼装 → L2 YAML；真实飞书 / 真 AI / CardKit 时序 → L3a（快查）或 L3b（进 e2e-real case 库）。
2. **smoke vs regression**：核心、高频、发布前必须秒级发现的入 `tests/smoke/` 带 `smoke` tag；广覆盖、边界、管理命令入 `tests/regression/` 带 `regression` tag。别只靠目录名，runner 以 tag 为准。
3. **顺序契约用 `segments_order`**，不要串起多条 `any_segment_contains` 来暗示顺序——那样倒序也会通过。
4. **卡片按钮**优先用 `action` 步骤，并为相同权限与输入契约的动作保持 `/action <id>` 消息等价入口。只在卡片上下文专有字段时才用 action 注入。
5. **写完 e2e-real case 必须**：加进且只加进一个清单（`SMOKE_CASES` / `FULL_EXTRA_CASES` / `FEATURE_CASES`）；使用 `case_prerequisites` 复用现有 capability 名，无额外需求走默认分支；`configure_callback_for_cases` 只对需要 callback gateway 的 case 加白名单；跑 `--profile <name> --case <name> --strict-capabilities` 单验。完整 SOP 见 [testing.md](testing.md)。
6. **变更收口**：跑 `go test ./...`（含 smoke integration）；涉及发布证据的另跑 regression；涉及真链路的按 self-loop GUIDE 决定 L3。

## 已知限制

- **L2 断言的是 bridge 产生的卡片数据模型**，不是飞书最终渲染后的 UI。表单提交按钮、卡片样式细节等渲染期元素必须靠 L3。
- **simulate 未装配全部 store**：目前只装了 Preference、DevMode、Workspaces。Updates、Schedules、Access 仍未装配——相关 YAML 只能断言当前可观察行为，不能虚构本地模拟没有的状态。用 `l3.skip` 显式排除并留原因。
- **L3 checklist 只投影消息型 `input` 步骤**，跳过 `action` 步骤——它不证明真实的 `card.action.trigger` 投递。带 `form_values` 的动作需另行执行真实卡片点击验收。
- **release-regression.sh L2/L3 需要独占同 App 的 wss**：跑之前必须停 supervisor 或 Test。同一 App 建两条 wss 会话，飞书 gateway 不保证投递到哪一条，audit 会不稳定。
- **`scripts/lib/simulate-suite.sh:21` 断言 SessionID 格式为 `chat-demo:message:...` 与真实输出不符**（现在带 `thread:@bot:local-...` 段）——`verify.sh` 因此在 origin/main HEAD 直接失败。这是既有 bug，与本轮改动无关，发正式版前需先修。

## 相关代码

**testfw runner**：

- `cmd/lark-bridge-test/main.go`：CLI 参数、用例加载、report/checklist 输出。
- `internal/testfw/types.go`：YAML 与 simulate JSON 的契约（含 `Segment` / `Assert.Texts`）。
- `internal/testfw/runner.go`：隔离运行器、标签筛选、CLI 调用、报告。
- `internal/testfw/assert.go`：10 种断言的语义与可见文本抽取（`extractVisibleText`）。
- `internal/testfw/smoke_suite_test.go`：smoke 挂进 `go test` 的连接点。
- `internal/testfw/report.go` / `checklist.go`：发布 sidecar 与 L3 清单投影。

**发布门禁**：

- `cmd/lark-bridge-release/evidence.go`：`test-evidence ensure`，publish 门禁核心。

**顶层脚本**：

- `scripts/verify.sh`：L0/L1 结构化 verify（文档契约 + go test + doctor + smoke + 会话/卡片/回收）。
- `scripts/release-regression.sh`：L0→L3 全量编排。
- `scripts/e2e-real.sh`：真飞书 + 真/假 Agent 的 case-based canary，含 preflight capabilities。
- `scripts/lib/e2e-profile.sh`：profile 加载与安全字段/权限校验。
- `scripts/lib/simulate-suite.sh` / `scripts/lib/assert.sh`：命令面 simulate 断言集。

**self-loop L3a**：

- `~/bridge/bridge-self-loop/rebuild-test.sh` (Linux) / `rebuild-test-macos.sh` (Mac)：Test bot 换血 + `l3_sender_preflight` 硬校验。
- `~/bridge/bridge-self-loop/GUIDE.md`：L3 手工验证步骤、chat_id / open_id / audit 位置、发布收口协议。
