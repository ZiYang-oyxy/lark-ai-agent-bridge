# Lark AI Agent Bridge

独立的 Feishu/Lark AI agent bridge，用于让用户在飞书里触发 Claude 单次任务，并用 CardKit 卡片同步展示执行中、结果、停止和工作目录确认状态。

当前实现聚焦 Claude one-shot 模式：

- 飞书消息通过 SDK 长连接进入 bridge。
- 第一版只适配 `claude`，暂不适配 `codex`。
- Claude 以 `claude -p --output-format stream-json --dangerously-skip-permissions` 启动。
- 同一 chat/topic 会话串行执行，不同 topic 可并行执行。
- topic 内普通消息继续当前 Claude session；`/new` 重置当前 topic 会话。
- 非 topic 普通消息默认创建新 Claude 会话。
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

详细架构见 `docs/framework/architecture.md`，测试流程见 `docs/workflow/testing.md`，当前交付状态与证据链汇总见 `docs/workflow/delivery-summary.md`。
