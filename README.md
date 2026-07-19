# Lark AI Agent Bridge

独立的 Feishu/Lark AI agent bridge，用于让用户在飞书里触发 Claude 单次任务，并用 CardKit 卡片同步展示执行中、结果、停止和工作目录确认状态。

当前实现聚焦 Claude one-shot 模式：

- 飞书消息通过 SDK 长连接进入 bridge。
- 第一版只适配 `claude`，暂不适配 `codex`。
- Claude 以 `claude -p --output-format stream-json --dangerously-skip-permissions --effort low` 启动。
- 默认使用普通聊天模式：回复进入聊天主消息流，同一 chat 共用 Claude session 并串行执行。
- `/config` 可切换为话题模式：回复进入话题，有 `ThreadID` 时每个 topic 独立 session，不同 topic 可并行执行。
- `/new` 重置当前 conversation scope；普通文本继续该 scope 已保存的 Claude session。
- CardKit 卡片流式展示正文、折叠思考过程、折叠工具调用、分栏底部状态栏和一次性停止按钮。
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

## Conversation mode

`/config` 的 **Conversation mode** 控制回复位置和 session scope：

- `chat`（默认）：CardKit/文本回复使用 `reply_in_thread=false`，session key 为 `{Agent, ChatID}`。
- `topic`：回复使用 `reply_in_thread=true`，非空 `ThreadID` 会进入 session key。

该设置与 Reply mode（`append`、`append-clean-card`、`latest-card`）相互独立。保存成功后只影响新接收的消息；已有 session 不迁移、不删除，已经排队或停在 workdir 确认阶段的输入继续使用接收时的 mode。启动环境可用 `E2E_CONVERSATION_MODE=chat|topic` 设置 `/config reset` 恢复的默认值。

## Agent 选择

`/config` 卡片新增三个下拉，用于选择运行所用的 agent：

- **Agent**：agent 类型。目前仅暴露 `claude`；`codex` 已在内部预留（`agent.Codex`、`CODEX_HOME` env 映射、agents.json schema），但 argv/流式解析尚未实现，暂不进 UI。
- **Agent home**：agent 的配置目录。选 `默认` 表示不注入 config-dir 环境变量（沿用宿主默认，与本功能之前的行为一致）；选预设则对 Claude 注入 `CLAUDE_CONFIG_DIR=<path>`。
- **Agent bin**：可执行路径。选 `主机 claude` 回退到 bridge 配置的默认可执行（`E2E_CLAUDE_BIN`，默认 `claude`）；选预设则使用其绝对路径。

可选项来自工作目录下的 `.lark-agent-bridge/agents.json`（**不引入任何新的 `E2E_*` 环境变量**）。该文件缺失或非法时回退到内置默认（单个 claude、只有「默认」home 和「主机 claude」bin），不阻断启动，`doctor` 的 `agents_config` 项会给出软告警。用户选择随其它偏好一起持久化到 `preferences.json`，`/config reset` 一并恢复默认。

每个 `home` / `bin` 支持可选 `desc` 字段（作用描述）。`/config` 卡片的下拉每项显示为 `名称 · 作用`，例如 `ark4 · 方舟 豆包 seed-2-1-pro`；下拉的 `value`（即持久化到 preferences 的 label）仍是纯名称，`desc` 只影响显示。

`agents.json` 示例（列全 workspace 里 claude 系的 wrapper bin）：

```json
{
  "schema_version": 1,
  "agents": [
    {
      "kind": "claude",
      "label": "Claude Code",
      "homes": [
        { "label": "demo .claude-home", "path": "/data/.../lark-agent-workspace.demo/.claude-home", "desc": "workspace 隔离配置目录" }
      ],
      "bins": [
        { "label": "ark1", "path": "/data/.../bin/ark1", "desc": "方舟 deepseek-v4-flash[1m]" },
        { "label": "ark4", "path": "/data/.../bin/ark4", "desc": "方舟 豆包 seed-2-1-pro" },
        { "label": "cc4", "path": "/data/.../bin/cc4", "desc": "claude-opus-4-8" },
        { "label": "cc5", "path": "/data/.../bin/cc5", "desc": "claude-fable-5[1m]" }
      ]
    }
  ]
}
```

> `codex` agent 类型在 schema、常量与 env 映射（`CODEX_HOME`）层已预留，但 argv/流式解析未实现，UI 暂不暴露；因此 `cx*` 系 wrapper 目前不建议放入 claude agent 的 bins。

> 借此可绕开 workspace 的 `bin/cc` wrapper：把 bin 指向裸 `claude` 并配独立 home，即可让 bridge 直接掌控可执行与配置目录，而不受 wrapper profile 静默影响。

详细架构见 `docs/framework/architecture.md`，测试流程见 `docs/workflow/testing.md`，真实飞书 E2E 工作流见 `docs/workflow/e2e-real.md`，当前交付状态与证据链汇总见 `docs/workflow/delivery-summary.md`。
