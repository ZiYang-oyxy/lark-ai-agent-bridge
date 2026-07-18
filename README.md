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

详细架构见 `docs/framework/architecture.md`，测试流程见 `docs/workflow/testing.md`，真实飞书 E2E 工作流见 `docs/workflow/e2e-real.md`，当前交付状态与证据链汇总见 `docs/workflow/delivery-summary.md`。
