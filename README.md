# Lark AI Agent Bridge

独立的 Feishu/Lark AI agent bridge，用于让用户在飞书里触发 Claude Code 或 Codex 任务，并用轻量 Markdown CardKit 或完整 CardKit 卡片同步展示执行状态与结果。

当前实现聚焦 CLI one-shot 模式：

- 飞书消息通过 SDK 长连接进入 bridge。
- `/config` 可从 `agents.json` 选择 `claude` / `codex` 及其 wrapper presets（包括 `cx1`～`cx4`）。
- Claude 以 `claude -p --output-format stream-json --dangerously-skip-permissions --effort low` 启动。
- Codex 以 `codex exec --json ... -` 启动，prompt 通过 stdin 传入；Bridge 不额外传 model、effort、sandbox、approval 或 profile 参数。
- 默认使用普通聊天模式：回复进入聊天主消息流，同一 chat 按 Agent 共用 session 并串行执行。
- `/config` 可切换为话题模式：回复进入话题，有 `ThreadID` 时每个 topic 独立 session，不同 topic 可并行执行。
- `/new` 重置当前 conversation scope；普通文本继续该 scope 已保存的 Agent session。
- `append` 每轮新建无标题、单 Markdown 元素的轻量 CardKit 流式回复：隐藏 thinking，工具调用只显示一行安全摘要，终态保留 agent/token footer。
- `append-clean-card` 与 `latest-card` 使用 CardKit：运行中可展示折叠过程，并支持一次性停止按钮；clean/latest 终态只保留最终答案。
- 执行中标题使用蓝色 `正在推理/正在执行工具/正在回复 · ⏱ Ns`，完成绿色，停止灰色，失败红色。
- 底部状态栏使用分割线和两行分栏：agent/model/tokens，以及 user/ip/workdir。
- 工作目录不存在时先发确认卡片；点击创建后确认卡变绿并禁用按钮，Claude 执行另起运行卡片。
- 卡片按钮走长连接 `card.action.trigger`，回调会同步返回终态卡片并保留异步 CardKit update 兜底；HTTP `/card/callback` 只保留为本地兼容调试入口。

## 本地命令

```bash
GOCACHE=$PWD/.cache/go-build go test ./...
./scripts/verify.sh
./scripts/evidence.sh
LARK_APP_ID=... LARK_APP_SECRET=... ./scripts/e2e-preflight.sh
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge doctor
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/new hello"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -thread topic-a -text "/new hello" -next-text "continue"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/new --workdir /tmp/missing-for-test hello" -timeout-now
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action -action stop -session claude:chat-demo:message:local-id
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge serve --default-workdir /tmp/lark-agent-bridge
```

`serve` 需要 `LARK_APP_ID` 和 `LARK_APP_SECRET`。本地未配置时会明确失败，用于验证启动前置条件。

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
- `topic`：回复使用 `reply_in_thread=true`，非空 `ThreadID` 会进入 session key。

该设置与 Reply mode（`append`、`append-clean-card`、`latest-card`）相互独立。保存成功后只影响新接收的消息；已有 session 不迁移、不删除，已经排队或停在 workdir 确认阶段的输入继续使用接收时的 mode。启动环境可用 `E2E_CONVERSATION_MODE=chat|topic` 设置 `/config reset` 恢复的默认值。

## 图片输出

Agent 可在最终回复中显式引用当前 workdir 内的本地图片：

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

`/config` 卡片新增三个下拉，用于选择运行所用的 agent：

- **Agent**：从 `agents.json` 中选择 `claude` 或 `codex`。
- **Agent home**：选 `默认` 时完整继承 executable 的环境；选显式预设时，Claude 注入 `CLAUDE_CONFIG_DIR=<path>`，Codex 注入 `CODEX_HOME=<path>`。
- **Agent bin**：Claude 的主机默认为 `E2E_CLAUDE_BIN`（默认 `claude`），Codex 的主机默认为 `codex`；其他选项直接使用预设路径。

可选项来自工作目录下的 `.lark-agent-bridge/agents.json`（**不引入任何新的 `E2E_*` 环境变量**）。该文件缺失或非法时回退到内置默认（单个 claude、只有「默认」home 和「主机 claude」bin），不阻断启动，`doctor` 的 `agents_config` 项会给出软告警。用户选择随其它偏好一起持久化到 `preferences.json`，`/config reset` 一并恢复默认。

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

> Codex 的 model、reasoning effort、sandbox、approval、profile、plugins、MCP 和 rules 均由所选 `codex` / `cx*` executable 及其环境决定。Bridge 只传 JSONL、resume、image 和 stdin 协议所需参数。Claude 仍保持现有 Bridge 参数策略。

> 当前 Codex 自动验收使用无网络 fake executable；真实飞书 + 真实 `codex`/`cx*` E2E 需在具备凭据和部署授权的环境另行执行。

> 借此可绕开 workspace 的 `bin/cc` wrapper：把 bin 指向裸 `claude` 并配独立 home，即可让 bridge 直接掌控可执行与配置目录，而不受 wrapper profile 静默影响。

详细架构见 `docs/framework/architecture.md`，测试流程见 `docs/workflow/testing.md`，真实飞书 E2E 工作流见 `docs/workflow/e2e-real.md`，当前交付状态与证据链汇总见 `docs/workflow/delivery-summary.md`。
