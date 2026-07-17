# Delivery Summary

## 当前结论

本仓库已从交互式终端 bridge 收敛为 Claude one-shot CardKit bridge：

- 飞书消息通过长连接进入 bridge。
- bridge 启动一次 Claude CLI 子进程处理请求。
- 执行中、流式输出、结果、停止和错误使用同一张 CardKit 卡片展示。
- 卡片按钮生产路径使用长连接 `card.action.trigger`，同步返回终态卡片并保留异步 CardKit update 兜底，不依赖公网 HTTP callback。
- 第一版只适配 `claude`，`codex` 暂缓。
- 群聊默认只响应 @bot；单聊逻辑默认全量响应，但真实单聊 E2E 暂缓。

## 已闭环范围

以下能力已经通过单测或本地模拟覆盖：

- `/new`、`/help`、`/status` 命令面。
- `/resume`、`/codex` 等撤回入口不再开放。
- Claude one-shot 命令构造，固定 `--dangerously-skip-permissions --effort low`。
- Claude stream-json 输出解析：正文、思考、工具调用、model、tokens、session id，并支持增量更新卡片。
- topic 内普通文本续接保存的 Claude session id。
- `/new` 重置当前 chat/topic 会话。
- 同一 chat/topic 串行排队，不同 topic 并行。
- 工作目录不存在时的创建/取消确认；create/cancel 后确认卡片进入绿色/灰色终态并禁用按钮，create 后 Claude 执行另起运行卡片。
- `/new --workdir` 会传递到 Claude 子进程的 `cmd.Dir` 和 `PWD`，排队输入也保留各自 workdir。
- 执行中停止按钮取消 active run，并把同一卡片置灰为“已停止”。
- CardKit 2.0 create/reply/update、蓝/绿/灰/红 header、`状态 · ⏱ Ns` 标题、富文本正文、折叠思考面板、折叠工具面板和底部分栏状态栏。
- 底部状态栏先用分割线隔开，再用两行分栏展示 agent/model/tokens 和 user/ip/workdir；tokens 使用 `🔢 tokens: ▶ 本轮 / ∑ 累计`，不展示 status。
- 停止按钮在 running 可点击，在 stopped/completed/failed 均为灰色 disabled，文案分别是 `已停止`、`已完成`、`已结束`。
- JSONL 审计日志和敏感信息脱敏。
- doctor、verify、evidence 本地证据链。

## 真实环境验证状态

- 已验证真实 Feishu 群聊 @bot 后，bridge 通过长连接接收消息并回复执行中/结果卡片。
- 已验证真实卡片流式更新：audit 中出现 `cardkit_update event=stream`，最终同一卡片更新为 `event=result`。
- 已验证真实点击“停止”按钮后，长连接收到 `card.action.trigger`，Claude 子进程被取消，卡片更新为灰色终态且按钮 disabled。
- 已验证真实点击工作目录“Create directory”和“Cancel”后，确认卡片分别进入绿色/灰色终态并禁用按钮；create 后出现独立 Claude 运行卡片。
- 已验证 Codex Chrome 插件可以直接操作当前已登录的飞书 Web 标签页，完成真实 `Create directory` 点击和后续 workdir 继承查询。
- 自写 Chrome/CDP full 路径已移除；真实撤回场景改为通过 `lark-cli im messages delete --as user` 自动验证，按钮类历史证据仍保留在 audit/evidence 中。
- 已验证群话题内 @bot 续聊进入 `thread:<thread_id>` 会话；当前飞书权限下，群话题内不 @bot 的消息不会推送到 bridge。
- 单聊 E2E 仍暂缓；需要同 bridge app 用户 OAuth profile，或手动建立 P2P 后记录 chat_id。

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
