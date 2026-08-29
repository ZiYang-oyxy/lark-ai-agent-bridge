# 推理深度五档支持设计

## 目标

Bridge 的显式推理深度从 `low / medium / high` 扩展为
`low / medium / high / xhigh / max`。`default` 继续表示不覆盖 Agent 自身配置，
因此持久化层和配置卡实际接受六个值。

## 方案

采用现有统一 effort 配置链路，不引入按 Agent 分支：

- `internal/config` 负责持久化值的最终合法性校验。
- `/config`、`/local-config` 命令与卡片使用同一组六个选项。
- Claude 对非 `default` 值继续生成 `--effort <value>`。
- Codex 对非 `default` 值继续生成
  `-c model_reasoning_effort="<value>"`。

本机当前 Claude CLI 的 `--help` 明确列出五档，Codex 配置透传也接受同名值，
所以无需对 `xhigh` 或 `max` 做降级映射。映射会掩盖用户选择，并造成状态栏与实际执行不一致。

## 兼容性与错误处理

- 已有 `default / low / medium / high` 偏好保持原义，不迁移 schema。
- effort 会继续做 trim 与小写归一化。
- 未知值仍在写盘前拒绝，保留最后一次有效偏好。
- schedule 等冻结执行配置的调用方复用同一偏好值，无需新增字段。

## 验证

- L1：覆盖六个合法值与非法值；断言 Claude/Codex 的 `xhigh`、`max` argv。
- L1：断言配置卡显示六个选项，帮助文案同步更新。
- L2：分别模拟 `/config set effort=xhigh` 与
  `/local-config set effort=max`，核对事件和持久化输出。
- L3：该变更不改变飞书 API、CardKit 时序或真实模型响应解析；L1/L2 已能证明
  配置到 argv 的完整链路，因此默认不启动 Test bot。
