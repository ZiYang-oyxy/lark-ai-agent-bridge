# Lark AI Agent Bridge

独立的 Feishu/Lark AI agent bridge，用于让用户在飞书里触发 Claude Code 或 Codex 任务，并用 CardKit 卡片同步展示执行状态与结果。

## 使用 AI Agent 安装

在目标 workspace 中打开 AI 编程助手，将仓库或发布链接填入下面的提示词并发送：

```text
请根据这个仓库或发布链接，将 Lark AI Agent Bridge 安装并接入当前
workspace：
<链接>

请自动完成环境检查、下载或构建、配置、后台启动和验证；只在必须由我
完成的飞书授权步骤时引导我操作。不要让我手动执行命令，安装成功后告诉
我如何在飞书中开始使用。
```

推荐使用发布目录中的 `stable/AI_INSTALL.md`；需要固定版本时，使用对应版本目录中的 `AI_INSTALL.md`。用户只需根据 AI 提示完成必要的飞书授权。

当前实现聚焦 CLI one-shot 模式：

- 飞书消息通过 SDK 长连接进入 bridge。
- `/config` 可从 `agents.json` 选择 `claude` / `codex` 及其 wrapper presets（包括 `cx1`～`cx4`）。
- Claude 以 `claude -p --output-format stream-json --dangerously-skip-permissions --effort low` 启动。
- Codex 以 `codex exec [-c model_reasoning_effort="<value>"] --json ... -` 启动，prompt 通过 stdin 传入；非 `default` effort 由 Bridge 显式覆盖，`default` 继承所选 executable/home，model、sandbox、approval 和 profile 仍不由 Bridge 覆盖。
- Codex 的 `exec --json` 会将过程 commentary 与最终回答都输出为 `agent_message`；Bridge 会立即将最新消息显示在答案区，后续活动再将它保留到 thinking 折叠区。直到下一条 `agent_message` 到来前，答案区始终可见最新内容，不依赖 reasoning summary 配置。
- 默认使用普通聊天模式：回复进入聊天主消息流，同一 chat 按 Agent 共用 session 并串行执行。
- `/config` 可切换为话题模式：回复进入话题，有 `ThreadID` 时每个 topic 独立 session，不同 topic 可并行执行。
- `/local-config` 让每个群覆盖全局默认的执行类偏好（逐字段继承），从而不同群可用不同方式（如 A 群 `topic`、B 群 `chat`）；访问控制永远全局。
- `/new` 重置当前 conversation scope；普通文本继续该 scope 已保存的 Agent session。
- `/stop` 只停止当前 agent/chat/topic scope 的 active batch；命中 active batch 时不回复新卡片，而在原任务 stopped 卡尾部显示 `已请求停止当前任务；排队输入将继续执行。`；后续 queued 输入保留并继续调度，空闲时安全提示无运行任务。
- `/resume` 列出当前 Agent 与 workdir 最近使用的 10 个 Bridge Session；`/resume <session-id>` 切换后由下一条普通消息继续目标 Session。
- 自然语言定时同时支持重复任务和一次性任务；Agent 只生成规则提案，用户确认后 Bridge 才持久化并启用。
- `/config` 的三种回复模式是：`Coder`（按顺序展示回复与工具进展）、`Worker`（思考、正文、工具分区展示）、`Singleton`（维持单个卡片更新，搭配 pin 使用）。底层兼容 key 分别为 `append`、`append-clean-card`、`latest-card`，仅用于已有配置、环境变量和 API 兼容，不作为用户界面文案。
- 执行中标题使用蓝色 `正在推理/正在执行工具/正在回复 · ⏱ Ns`，完成绿色，停止灰色，失败红色。完成态默认显示 `✅ 已完成 · ⏱ Ns`；管理员可用 `/config set completion_status_text=🎉任务完成` 改为最多 32 个字符的单行标题前缀，`/config set completion_status_text=` 恢复默认。该偏好在消息入队时冻结，只影响之后的新任务。
- 执行中卡片连续 5 秒没有正常流式更新时会自动刷新耗时；无正文的等待阶段同时显示“任务仍在运行…”。每次正常更新会重新计时，完成、失败或停止后立即取消刷新。
- 底部状态栏使用分割线和两行分栏：agent/model/tokens，以及 user/ip/workdir。Claude 从 stream 事件、Codex 从当前 session transcript 读取已生效的实际模型；尚未取得运行期记录时显示“同步中”，不会把请求配置伪装为实际模型。
- 工作目录不存在时先发确认卡片；点击创建后确认卡变绿并禁用按钮，Claude 执行另起运行卡片。
- 卡片按钮走长连接 `card.action.trigger`，回调会同步返回终态卡片并保留异步 CardKit update 兜底；HTTP `/card/callback` 只保留为本地兼容调试入口。
- `/help` 显示当前 Bridge 版本；配置 HTTPS update manifest 后会提示新版本、在卡片内展示 Release note，并允许 owner/admin 一键原子升级。
- `/help`、`/status`、`/resume` 等大型命令卡片提供“关闭”按钮，点击后删除当前卡片消息；配置类卡片的关闭仍受管理员权限约束。

## 本地命令

```bash
GOCACHE=$PWD/.cache/go-build go test ./...
./scripts/verify.sh
./scripts/evidence.sh
LARK_APP_ID=... LARK_APP_SECRET=... ./scripts/e2e-preflight.sh
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge doctor
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/new hello"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/stop"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -thread topic-a -text "/new hello" -next-text "continue"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/new --workdir /tmp/missing-for-test hello" -timeout-now
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action -action stop -session claude:chat-demo:message:local-id
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge serve --default-workdir /tmp/lark-agent-bridge
```

运行中卡片的 idle heartbeat 默认是 5 秒，可用 `E2E_CARD_HEARTBEAT_SEC` 设置正整数秒数。`E2E_CARD_UPDATE_MS` 仍只控制已有 Agent stream event 的内容刷新节流，两者用途不同。

`serve` 需要 `LARK_APP_ID` 和 `LARK_APP_SECRET`。本地未配置时会明确失败，用于验证启动前置条件。

## 一键升级与发布物

运行端至少需要配置 stable manifest URL；若希望在开发者模式下接收 rc 版本，另配 prerelease manifest URL：

```bash
# 必配：stable 通道，普通用户升级路径
export LAB_UPDATE_MANIFEST_URL=https://updates.example.com/lark-ai-agent-bridge/stable/manifest.json

# 可选：prerelease 通道，供开发者模式（/.devel 1）接收 rc 版本
export LAB_UPDATE_PRERELEASE_MANIFEST_URL=https://updates.example.com/lark-ai-agent-bridge/prerelease/manifest.json
```

只配 stable 时，即使发布了新的 rc 版本，`/help` 也不会展示——rc 只能在开发者模式（发送 `/.devel 1`）+ prerelease URL 已配置的前提下升级。若 stable URL 配了、prerelease URL 未配，Bridge 启动时会在 stderr 打印一条 `[warn] LAB_UPDATE_PRERELEASE_MANIFEST_URL 未配置` 提示，避免"发了 rc 但 /help 看不到"这类反复追根因的场景。

**Linux 服务器 supervisor 推荐用 systemd user unit 托管**，避免手工 `nohup` 启动时遗漏环境变量。unit 与 EnvironmentFile 应由部署仓库维护，不要把真实凭据提交到本仓库。手工临时启动示例：

```bash
export LARK_APP_ID=... LARK_APP_SECRET=... \
       LAB_DEFAULT_WORKDIR=/path/to/workspace LAB_CLAUDE_BIN=/path/to/claude \
       LAB_UPDATE_MANIFEST_URL=https://.../stable/manifest.json \
       LAB_UPDATE_PRERELEASE_MANIFEST_URL=https://.../prerelease/manifest.json
nohup ~/bin/lark-agent-bridge-<name> serve --default-workdir "$LAB_DEFAULT_WORKDIR" &
```

`/help` 会以 10 分钟内存缓存检查版本。所有授权用户都能查看卡片内 Release note；只有 bot owner/admin 能确认升级。升级前会重新获取 manifest，下载当前平台 binary，严格校验 size 与 SHA-256；存在 active/queued 任务时拒绝升级。通过校验后 Bridge 在当前 binary 同目录保留 `.previous`、原子替换，再以原 argv/env 启动新版本。systemd 环境使用专用退出码把最终重启交给 service manager，因此 `Restart=on-failure` 和 `Restart=always` 都能接管；非 systemd 环境保持 detached child 的原有行为。替换或新进程启动失败会恢复旧 binary；新版本已经启动后再崩溃不自动回滚。

首版只支持：

- `linux/amd64`
- `darwin/arm64`

开发构建的版本为 `dev`，可显示帮助但禁用自升级。正式 binary 与静态发布目录通过以下流程生成：

```bash
./scripts/release.sh prepare v1.2.3
# 编辑并提交 docs/releases/v1.2.3.md
./scripts/release.sh tag v1.2.3
./scripts/release.sh bundle v1.2.3 \
  --base-url https://updates.example.com/lark-ai-agent-bridge
./tests/release-bundle-smoke.sh dist v1.2.3
```

`bundle` 使用 Go `1.26.3`、`CGO_ENABLED=0`，在同一发布机交叉构建 Linux x86-64 与 macOS ARM64，输出 `dist/v1.2.3/` 和 `dist/stable/manifest.json`。仓库不内置托管平台或凭证；外部上传命令必须先上传并回读校验完整的 versioned 目录，最后才替换 stable manifest。

人工回退时停止对应 Bridge 实例，把 `<binary>.previous` 原子恢复为 `<binary>`，再通过该部署目标唯一的启动入口恢复服务；不得宽泛 `pkill` 或混用不同环境的部署事务。

## 自然语言定时任务

用户可以直接发送“每天上午 9 点总结昨天进展”或“明天下午 3 点提醒我评审方案”。Bridge 让 Agent 把自然语言转换成标准五段 cron 或带时区的绝对时间，并返回包含自然语言规则、未来执行时间和确认/取消按钮的卡片。只有原请求用户在同一会话中点击“确认”或发送精确文本 `确认` 后任务才会生效；待确认规则 10 分钟后过期。

管理命令：

- `/cron`、`/timer`：列出当前 chat/topic 内对应任务。
- `/cron add <自然语言>`、`/timer add <自然语言>`：显式发起规则提案。
- `/cron info <id>`、`/timer info <id>`：查看规则、冻结配置和最近运行状态。
- `/cron run <id>`、`/timer run <id>`：立即执行一次。
- `/cron enable|disable|del <id>` 和对应 `/timer` 命令：启停或删除任务。

执行配置在提案时冻结，包括 Agent、model、effort、binary/home、workdir、reply mode、conversation mode 和 chat/topic 目标，后续全局配置变化不会静默改变已有任务。每个 occurrence 先以确定性 run ID 持久化，再进入现有 durable session queue；重复投递会命中 receipt 去重。同一任务不允许重叠执行，队列满时在 5 分钟补偿窗口内重试，单次执行默认 30 分钟超时。重启后只补偿窗口内最近一次 cron occurrence；过期任务会明确记录为 `missed`，不会无界追赶。

状态默认保存在 `<workdir>/.lark-agent-bridge/schedules.json`，文件损坏或 schema 不兼容会阻止 `serve` 启动。可用 `E2E_SCHEDULE_STORE`、`E2E_SCHEDULE_DRAFT_TTL_MIN`、`E2E_SCHEDULE_CATCHUP_MIN`、`E2E_SCHEDULE_TIMEOUT_MIN`、`E2E_SCHEDULE_RETENTION_DAYS` 覆盖。`schedule propose` 及其 Unix socket/token 是 Agent 与 Bridge 的内部协议，不是用户管理入口。

## 访问控制

真实 `serve` 默认 fail-closed：私聊仅允许应用 owner、`allowed_users` 和管理员；群聊仅允许应用 owner、管理员和 `allowed_chats` 中的群。owner 从 Feishu `application/v6` API 启动时读取并每 30 分钟刷新，刷新失败时保留已缓存 owner。

owner 或管理员可在飞书中管理名单：

- `/invite user @某人`、`/remove user @某人`：管理私聊用户。
- `/invite admin @某人`、`/remove admin @某人`：管理管理员。
- `/invite group`、`/remove group`：在当前群授权或撤销。
- `/invite all group`：授权 bot 当前所在的全部群（最多读取 5 页，每页 100 个）。

名单保存在 `<workdir>/.lark-agent-bridge/access.json`，可用 `E2E_ACCESS_STORE` 覆盖路径。文件不存在表示空名单；文件损坏或 schema 不兼容会阻止 `serve` 启动。`/config reset` 不会清空访问控制，`/config` 的折叠面板只展示名单，修改仍通过上述命令完成。

## Conversation mode

`/config` 的 **Conversation mode** 控制回复位置和 session scope：

- `chat`（默认）：CardKit/文本回复使用 `reply_in_thread=false`，session key 为 `{Agent, ChatID}`。
- `topic`：回复使用 `reply_in_thread=true`，非空 `ThreadID` 会进入 session key；P2P 或群聊主会话中，只有正文里显式可见的 `@bot` 才创建独立 topic session，未显式 `@bot` 的消息继续使用 chat root session。

该设置与回复模式（Coder、Worker、Singleton）相互独立。保存成功后只影响新接收的消息；已有 session 不迁移、不删除，已经排队或停在 workdir 确认阶段的输入继续使用接收时的 mode。启动环境可用 `E2E_CONVERSATION_MODE=chat|topic` 设置 `/config reset` 恢复的默认值。

## Session 恢复

- `/resume`：按最近使用时间倒序列出当前 Agent、当前 workdir 下最多 10 个 Bridge 管理的 Session，并标记当前 Session。
- `/resume <session-id>`：精确恢复列表中的完整 Session ID；命令本身不会发送给 Agent，下一条普通消息才执行 Claude `--resume` 或 Codex `exec resume`。

恢复不会扫描 Claude/Codex 在终端或其他客户端创建的历史。当前 conversation scope 有 active batch 或 queued input 时会拒绝切换，避免中断任务或把旧队列发送到另一个 Session。历史目录保存在 Session store 同目录的 `session-catalog.json`；文件损坏或 schema 不兼容会阻止 `serve` 启动。

## 本群偏好覆盖（`/local-config`）

`/config` 配置的是**全局默认**；`/local-config` 让每个群按需**覆盖**其中的执行类偏好，从而不同群可用不同方式（例如 A 群走 `topic`、B 群走 `chat`）。

- **作用域路由：** `/config` 始终写全局默认；`/local-config` 只在群里生效，写当前群的覆盖。私聊里发 `/local-config` 会引导改用 `/config`。
- **逐字段继承：** 覆盖是逐字段的——群里只存显式改过、且与全局不同的字段，其余字段实时继承全局。改了全局默认，未覆盖该字段的群立即跟随。
- **可覆盖项：** model、effort、agent、agent_home、agent_bin、reply_mode、conversation_mode、group_message_mode、respond_to_bots。
- **访问控制永远全局：** allowed_users / allowed_chats / admins 不可 per-chat，`/local-config` 表单不展示、也不接受这些字段，管理仍走 `/invite`、`/remove`。
- **重置：** `/local-config reset` 只清当前群的覆盖，全部回到继承全局；不影响全局默认，也不影响其它群。全局 `/config reset` 不会清空任何群覆盖。
- **可解释性：** 群里 `/status` 会以 `local_overrides=<字段列表>` 标出本群覆盖了哪些字段，其余继承全局。
- 覆盖存放在与全局同一份 `<workdir>/.lark-agent-bridge/preferences.json` 的 `chat_overrides` 表中；旧快照无此字段时按空覆盖表平滑加载。保存后只影响新接收的消息。

## 群消息接收

`/config` 的 **Group message intake** 是一个互斥选择，默认保持最小接收范围：

- `mention_only`（默认）：只处理明确 `@bot` 的群消息。
- `participated_topics`：除明确 `@bot` 外，还处理 bot 已参与话题中的后续消息；参与状态持久化，重启后仍有效。
- `all_group_messages`：处理已授权群中的所有消息，无需 `@bot`。

**Respond to bot/app senders** 是独立开关，默认关闭；关闭时会忽略其他 bot/app 发送的消息，避免机器人之间互相触发。Bridge 自身消息始终忽略，即使打开此开关也不会自循环。开启扩展接收模式时，Bridge 会检查 tenant 是否已有 `im:message.group_msg` 权限；缺失时发送带授权链接的卡片，授权完成后再次确认才报告权限生效。权限状态无法确认时不会伪报授权成功；切回 `mention_only` 即可回滚本地 intake 行为，不会主动撤销 tenant scope。

启动默认值可由 `E2E_GROUP_MESSAGE_MODE=mention_only|participated_topics|all_group_messages` 和 `E2E_RESPOND_TO_BOTS=true|false` 配置；参与话题记录默认位于 `<workdir>/.lark-agent-bridge/participated-topics.json`，可用 `E2E_PARTICIPATED_TOPICS_STORE` 覆盖。记录采用最多 10,000 条的确定性 LRU；文件损坏时 `serve` 会 fail-closed，避免悄悄丢失参与边界。`/config reset` 会恢复启动默认值。

## 图片输出

用户直接用自然语言表达明确的图片意图即可，例如：

```text
画一张当前系统架构图发给我
把刚才生成的趋势图发出来
```

Claude 和 Codex 会收到同一份由 Bridge 管理的图片能力说明，并在最终回复中生成内部图片引用；用户不需要输入 Markdown，也不需要了解上传协议。普通任务即使 workdir 中存在图片也不会自动发送；“刚才那张图”存在多个合理候选时，Agent 应先询问用户。

能力说明使用 Agent 原生 instruction channel 注入：Claude 使用 `--append-system-prompt-file`，Codex 使用 `developer_instructions`。Bridge 在 session 首次运行时固定 instruction version，普通 resume 保持不变，`/new` 才切换到当前版本；内容不会逐轮拼入 user prompt 或重复写入会话历史。

以下 Markdown 是 Agent → Bridge 的内部交接协议，也可用于排障：

```markdown
![趋势图](./output/chart.png)
```

Bridge 会按引用顺序把图片作为独立的飞书图片消息发送，并在同一结果卡中把引用更新为“已作为图片发送”或安全的失败原因。Bridge 不扫描目录，不从 thinking 或 tool output 提取图片，也不会下载并转发公网图片。

首版限制：

- 支持 PNG、JPEG、WebP、GIF，按文件内容识别格式。
- 单张必须大于 0 且不超过 10 MiB；每轮最多发送 5 张唯一图片。
- 相对路径和绝对路径最终都必须解析到当前 workdir 内；越界路径与 symlink 逃逸会被拒绝。
- 不支持 HTTP(S)、`data:`、`file://`、HTML `<img>` 或 reference-style Markdown 图片。
- 单张失败不影响其他图片，也不会把已完成的 Agent run 改成失败状态。

## Agent 选择

使用 `/agent-mode` 选择后续消息使用的 Agent；`/config` 只展示当前 mode 的 home/bin 与其它运行偏好：

- **Agent mode**：从 `agents.json` 中选择 `claude` 或 `codex`。也支持 `/agent-mode claude`、`/agent-mode codex` 直接切换。
- **Agent home**：选 `默认` 时完整继承 executable 的环境；选显式预设时，Claude 注入 `CLAUDE_CONFIG_DIR=<path>`，Codex 注入 `CODEX_HOME=<path>`。
- **Agent bin**：Claude 的主机默认为 `E2E_CLAUDE_BIN`（默认 `claude`），并兼容旧部署变量 `LAB_CLAUDE_BIN`；两者同时存在时 `E2E_CLAUDE_BIN` 优先。Codex 的主机默认为 `codex`；其他选项直接使用预设路径。

可选项来自工作目录下的 `.lark-agent-bridge/agents.json`（**不引入任何新的 `E2E_*` 环境变量**）。该文件缺失或非法时回退到内置默认（单个 claude、只有「默认」home 和「主机 claude」bin），不阻断启动，`doctor` 的 `agents_config` 项会给出软告警。Agent mode、home、bin 与其它偏好一起持久化到 `preferences.json`，`/config reset` 一并恢复默认。

Bridge 拥有主 Codex invocation 的顶层 `developer_instructions`。显式 Codex home 的 `config.toml` 不应再定义同名顶层键；`doctor` 会把这种冲突报告为失败，避免静默覆盖 persona 或业务规则。subagent 配置中的同名字段不受影响。

每个 `home` / `bin` 支持可选 `desc` 字段（作用描述）。`/config` 卡片的下拉每项显示为 `名称 · 作用`，例如 `ark4 · 方舟 豆包 seed-2-1-pro`；下拉的 `value`（即持久化到 preferences 的 label）仍是纯名称，`desc` 只影响显示。

**path 支持三种写法，可保持 workspace 可搬**：

- **绝对路径**（`/data/.../bin/ark4`）：直接使用。
- **`~/...`**：展开为 `$HOME/...`，跨机器只要用户名对上就好。
- **相对路径**（`bin/ark4`、`./state/claude-home`）：以 agents.json 所在的 workdir（即 `--default-workdir` 或进程 CWD）为基。**推荐用这种**——只要整个 workspace 目录整体搬到别的机器，agents.json 无需改动。

`agents.json` 示例（同时配置 Claude 和 Codex wrapper）：

```json
{
  "schema_version": 1,
  "agents": [
    {
      "kind": "claude",
      "label": "Claude Code",
      "homes": [
        { "label": "workspace .claude-home", "path": ".claude-home", "desc": "workspace 隔离配置目录" }
      ],
      "bins": [
        { "label": "ark1", "path": "bin/ark1", "desc": "方舟 deepseek-v4-flash[1m]" },
        { "label": "ark4", "path": "bin/ark4", "desc": "方舟 豆包 seed-2-1-pro" },
        { "label": "cc4", "path": "bin/cc4", "desc": "claude-opus-4-8" },
        { "label": "cc5", "path": "bin/cc5", "desc": "claude-fable-5[1m]" }
      ]
    },
    {
      "kind": "codex",
      "label": "Codex CLI",
      "homes": [
        { "label": "workspace .codex-home", "path": ".codex-home", "desc": "workspace 配置目录" }
      ],
      "bins": [
        { "label": "cx1", "path": "bin/cx1", "desc": "Codex profile 1" },
        { "label": "cx2", "path": "bin/cx2", "desc": "Codex profile 2" },
        { "label": "cx3", "path": "bin/cx3", "desc": "Codex profile 3" },
        { "label": "cx4", "path": "bin/cx4", "desc": "Codex profile 4" }
      ]
    }
  ]
}
```

> Codex 的 `default` reasoning effort 以及 model、sandbox、approval、profile、plugins、MCP 和 rules 由所选 `codex` / `cx*` executable 及其环境决定；`/config` 选择 `low` / `medium` / `high` 时，Bridge 通过 `model_reasoning_effort` 显式覆盖本轮推理深度。Claude 仍保持现有 Bridge 参数策略。

> 当前 Codex 自动验收使用无网络 fake executable；真实飞书 + 真实 `codex`/`cx*` E2E 需在具备凭据和部署授权的环境另行执行。

> 借此可绕开 workspace 的 `bin/cc` wrapper：把 bin 指向裸 `claude` 并配独立 home，即可让 bridge 直接掌控可执行与配置目录，而不受 wrapper profile 静默影响。

详细架构见 `docs/framework/architecture.md`，测试流程见 `docs/workflow/testing.md`，真实飞书 E2E 工作流见 `docs/workflow/e2e-real.md`，当前交付状态与证据链汇总见 `docs/workflow/delivery-summary.md`。

## Security

请不要通过公开 Issue 报告漏洞或疑似凭据泄露。请使用 GitHub 的
[Private vulnerability reporting](https://github.com/ZiYang-oyxy/lark-ai-agent-bridge/security/advisories/new)。

## History rewrite notice

仓库历史于 2026 年 8 月因公开发布前的信息清理而重写。基于旧历史的 clone
请重新克隆，不要把旧分支 merge 或 push 回当前仓库。

## License

本项目使用 [MIT License](LICENSE)。
