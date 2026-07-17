# Delivery Summary

## 当前结论

本仓库已从交互式终端 bridge 收敛为 Claude one-shot CardKit bridge：

- 飞书消息通过长连接进入 bridge。
- bridge 启动一次 Claude CLI 子进程处理请求。
- 执行中和结果使用同一张 CardKit 卡片展示。
- 卡片按钮生产路径使用长连接 `card.action.trigger`，不依赖公网 HTTP callback。
- 第一版只适配 `claude`，`codex` 暂缓。
- 群聊默认只响应 @bot；单聊逻辑默认全量响应，但真实单聊 E2E 暂缓。

## 已闭环范围

以下能力已经通过单测或本地模拟覆盖：

- `/new`、`/help`、`/status` 命令面。
- `/resume`、`/codex` 等撤回入口不再开放。
- Claude one-shot 命令构造，固定 `--dangerously-skip-permissions`。
- Claude stream-json 输出解析：正文、思考、工具调用、model、tokens、session id。
- topic 内普通文本续接保存的 Claude session id。
- `/new` 重置当前 chat/topic 会话。
- 同一 chat/topic 串行排队，不同 topic 并行。
- 工作目录不存在时的创建/取消确认。
- 执行中停止按钮取消 active run，并置灰为“已停止”。
- CardKit 2.0 create/reply/update、富文本正文、折叠思考面板、折叠工具面板和底部 meta。
- JSONL 审计日志和敏感信息脱敏。
- doctor、verify、evidence 本地证据链。

## 仍需真实环境验证

- 真实 Feishu 群聊 @bot 后，bridge 长连接接收消息并回复执行中/结果卡片。
- 真实点击“停止”按钮后，长连接收到 `card.action.trigger`，Claude 子进程被取消，卡片按钮置灰。
- 真实点击工作目录“Create directory”和“Cancel”。
- 真实 Claude 生成结果中的 session id 是否稳定返回，并能用于 topic 内续接。
- 单聊 E2E 暂缓；需要同 bridge app 用户 OAuth profile，或手动建立 P2P 后记录 chat_id。

## 标准验证命令

本地完整验证：

```bash
GOCACHE=$PWD/.cache/go-build go test ./...
./scripts/verify.sh
```

带真实飞书前置检查：

```bash
set -a
source .lark-agent-bridge/e2e.env
set +a
./scripts/e2e-preflight.sh
```

生成交付证据报告：

```bash
set -a
source .lark-agent-bridge/e2e.env
set +a
REQUIRE_E2E=1 ./scripts/evidence.sh
```

报告写入 `.cache/evidence/`，不会进入 git。

## 证据报告应包含

完整 evidence 报告应至少包含：

- `local evidence: passed`
- `resource status: clean`
- `Feishu E2E preflight: passed`

如果任一项不满足，优先打开报告中的对应 section 查看命令输出。
