# Claude system prompt 正文注入设计

## 目标

Bridge 调用 Claude Code 时直接通过 `--append-system-prompt <content>` 注入固定版本的
Feishu runtime instructions，不再把内嵌内容物化到操作系统临时目录。这样长期运行的
Bridge 不会因 macOS 清理临时文件而导致后续所有 Claude 请求启动失败。

## 已明确

- 保留 Claude Code 默认 system prompt，只追加 Bridge instructions。
- session 继续固定 `BridgeInstructionsVersion`；fresh、resume 和 fork 每次运行都按该版本取正文。
- Codex 继续使用现有 `developer_instructions` 配置覆盖，行为不变。
- 用户 prompt 继续通过 stdin 传递，不回退到 argv。
- 删除不再需要的 instruction 临时目录、路径查询和清理生命周期。

## 裁撤方案

- 不把 instruction 文件迁到 workspace 持久目录：仍需处理权限、原子更新和陈旧文件。
- 不在临时文件缺失时自愈：仍保留不必要的文件生命周期和清理竞态。
- 不使用 `--system-prompt`：它会替换 Claude Code 默认 prompt，而不是追加 Bridge 能力说明。

## 实现

`bridgeinstructions.Runtime` 只保存按版本索引的内嵌正文。`CLIExecRunner` 为 Claude 和
Codex 解析同一份正文：Claude 写入 `OneShotConfig.ClaudeSystemPrompt`，Codex 写入
`DeveloperInstructions`。Claude 命令构造器校验正文不含 NUL 后，将其作为
`--append-system-prompt` 的单个参数传入。

当前 v3 正文约 4.5 KiB，显著低于 macOS/Linux 单参数限制。若未来正文增长，应通过测试
设置明确上限；本次不引入 stdin 多路协议，因为 stdin 已用于用户 prompt。

## 验证

- 单元测试锁定 fresh/resume 命令只包含一次 `--append-system-prompt` 和完整正文。
- 原生通道测试断言 Claude 收到正文参数，Codex 仍收到 developer instructions。
- 全仓 `go test ./...`、`go vet ./...` 与 `scripts/verify.sh` 通过。
- L2 simulate 验证消息到卡片主链无回归。
- L3 使用 Mac ephemeral Test 和真实 Claude 验证指令确实生效且 Supervisor PID 不变。
