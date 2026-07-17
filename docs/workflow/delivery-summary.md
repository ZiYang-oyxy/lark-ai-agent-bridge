# Delivery Summary

## 当前结论

本仓库已经形成一个独立的 Feishu/Lark agent bridge 原型，核心路径围绕交互式终端而不是一次性命令：

- 飞书消息通过长连接进入 bridge，写入 `claude` 或 `codex` 的 tmux window stdin。
- tmux pane 输出被轮询捕获，并同步到飞书 CardKit 卡片。
- 卡片按钮生产路径使用长连接 `card.action.trigger`，不依赖公网 HTTP callback。
- 所有 agent 进程运行在固定 tmux session `lark-agent-bridge` 下，不同会话使用独立 window。
- 群聊默认只响应 @bot；单聊逻辑已设计为全量响应，但真实单聊 E2E 暂未验证。

## 已闭环范围

以下能力已经通过单测、本地模拟、真实 tmux smoke、飞书 E2E preflight 或真实飞书群聊 E2E 覆盖：

- 独立仓库结构、参考仓 ignore、基础 README/架构/测试文档。
- `claude`/`codex` agent adapter、审批级别、恢复命令构造。
- tmux session/window 管理、stdin 写入、pane capture、`/attach` 命令。
- 同会话排队、跨 chat/topic 会话隔离、topic mode。
- CardKit 2.0 create/reply/update、流式输出、长文本分页、底部 meta。
- 授权、选择题、恢复候选的卡片识别、超时默认值和按钮写回。
- 工作目录不存在时的创建/取消确认。
- 查询中的停止按钮、Codex 子工具进程清理、stop 回调响应卡、terminal stop 确认卡。
- agent 崩溃检测和 `Restart session` 按钮。
- idle reminder 和 `Terminate session` 按钮。
- JSONL 审计日志、敏感信息脱敏、CardKit 操作审计。
- `serve` 信号退出清理和资源残留检查。

完整状态以 [tasks.md](../../tasks.md) 的“当前覆盖矩阵”为准。

## 仍需外部条件的事项

- Claude 真实生成校准：`scripts/agent-probe.sh` 当前报告 `claude: ready`，仍需补真实生成、授权和完成检测 E2E。
- 单聊 E2E：当前用户态 `lark-cli` 与 bridge app 不同，直接按 bot open_id 发送 P2P 会触发 `open_id cross app`；需要同 bridge app 的用户 OAuth profile，或手动建立 P2P 后记录 chat_id。
- Codex hooks 配置：当前本机配置已只保留 `[features].hooks = true`，doctor 输出 `ok codex_config_hooks`；如果后续旧 `codex_hooks` 被重新加入，doctor 会以 `warn` 提示。
- `im:message.group_msg` 可选 scope：当前主路径不依赖主动拉群历史；只有后续要补偿漏事件或拉群消息列表时才需要申请。

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
RUN_TMUX_SMOKE=1 REQUIRE_E2E=1 ./scripts/evidence.sh
```

报告写入 `.cache/evidence/`，不会进入 git。最新报告路径记录在 [tasks.md](../../tasks.md) 的“剩余待办/证据报告”条目中。

## 证据报告应包含

最新完整 evidence 报告应至少包含这些 summary：

- `local evidence: passed`
- `tmux smoke: passed`
- `resource status: clean`
- `Feishu E2E preflight: passed`

如果任一项不满足，优先打开报告中的对应 section 查看命令输出。
