# `/local-config` Agent mode 设计

## 目标

允许管理员在群聊的 `/local-config` 中为当前群覆盖全局 Agent mode。未设置本群覆盖时继续继承全局 `/agent-mode`；切换只影响后续新进入队列的消息。

## 方案

- 复用现有 `ChatOverride.Agent`、`AgentHome`、`AgentBin`，不修改持久化 schema。
- 只在本群配置表单中展示 `Agent mode` 下拉；全局 `/config` 仍通过独立 `/agent-mode` 修改 Agent。
- 本群 Agent mode 与全局不同才持久化 `Agent` 指针；相同则保持 `nil`，继续实时继承全局。
- 切换或恢复继承 Agent mode 时，如果本次没有显式指定 home/bin，则同时清理旧的本群 home/bin preset。切到与全局不同的 Agent 时写入空 preset 覆盖，避免继承全局中属于另一 Agent 的 preset。
- 保存继续走现有 revision CAS 和 `PreferenceStore` catalog 校验；非法、未配置 Agent 或不属于目标 Agent 的 preset 原子拒绝。
- `/local-config` 概览增加 Agent mode 行，并把该字段计入覆盖数量。

## 验证

- 单测覆盖本群表单渲染、卡片保存、命令式 set/inherit、跨 Agent preset 清理、非法 Agent 拒绝和概览展示。
- L1 运行全仓 `go test ./...`。
- L2 用 `simulate` / `simulate-action` 断言本群卡片与保存事件。
- 该改动改变真实 CardKit 表单，因此 L3 换血 Mac Test，发送真实 `/local-config` 并回读卡片确认 Agent mode 可见；保存语义由 L1/L2 精确断言。
