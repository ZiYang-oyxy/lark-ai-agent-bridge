# Lark Agent Bridge

独立的 Feishu/Lark agent bridge，用于把飞书聊天与交互式 AI agent 终端同步起来。

当前首轮实现聚焦核心底座：

- `claude`、`codex` agent 抽象
- 固定 tmux session `lark-agent-bridge`
- 会话 key、队列、内存 prompt 历史
- 本地模拟工具
- doctor 就绪检查
- 卡片事件抽象和 fake renderer
- 飞书 SDK 长连接入口、消息归一化、普通回复/反应接口
- CardKit HTTP client、CardKit renderer
- tmux 输出轮询、ready 自动出队、runtime model/token 元信息提取
- 长连接 `card.action.trigger` 卡片按钮入口，`/card/callback` 仅保留为本地兼容入口

## 本地命令

```bash
GOCACHE=$PWD/.cache/go-build go test ./...
./scripts/verify.sh
./scripts/evidence.sh
LARK_APP_ID=... LARK_APP_SECRET=... ./scripts/e2e-preflight.sh
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge doctor
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/claude hello"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/resume codex --last"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/claude first" -next-text "/claude second"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate -text "/claude --workdir /tmp/missing-for-test hello" -timeout-now
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-action -action stop -session claude:chat-demo
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "Tool permission required\nAllow once\nReject"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -output "done\n>" -next-text "/claude second"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge simulate-output -capture-error "pane missing"
GOCACHE=$PWD/.cache/go-build go run ./cmd/lark-agent-bridge serve
./scripts/tmux-smoke.sh
```

`serve` 需要 `LARK_APP_ID` 和 `LARK_APP_SECRET`。本地未配置时会明确失败，用于验证启动前置条件。

详细架构见 `docs/framework/architecture.md`，测试流程见 `docs/workflow/testing.md`，当前交付状态与证据链汇总见 `docs/workflow/delivery-summary.md`。
