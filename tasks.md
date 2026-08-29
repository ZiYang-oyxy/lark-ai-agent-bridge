# Feishu AI Agent Bridge Tasks

## Runtime configuration

- [ ] 将推理深度扩展为 `low / medium / high / xhigh / max`，覆盖全局/群配置、Agent 启动参数、文档与分层验证。

## Production stability

- [ ] P0：隔离 Agent 子进程环境中的 Bridge/飞书/发布凭据。
- [ ] P0：为完成通知、reaction 与 shutdown 增加有界 deadline 和生命周期收敛。
- [ ] P0：修复 E2E capability registry 自检并接入标准质量门禁。
- [ ] P1：将 terminal completion 持久化为可恢复事实，避免保存失败后重启误判 interrupted。
- [ ] P1：为自升级 manifest 增加独立签名信任根。
- [ ] P1：收敛 raw event/full prompt 日志默认值并增加轮转与 retention。
- [ ] P1：增加最小 health/readiness 与关键运行指标。

## Architecture convergence

- [ ] P1：提取 RunCoordinator，隔离 batch 执行、stop、completion 与 schedule 生命周期。
- [ ] P1：提取纯 StreamProjection，分离 Agent 流事件归一化与 reply mode 投影。
- [ ] P2：收敛 MessagePipeline 与 RuntimeDependencies，减少 Service/main 隐式装配规则。
- [ ] P2：建立 legacy/compat 清单与可验证的退役条件。

## CardKit roadmap

- [ ] P3（按需）：结构化表格、图表等展示组件出现明确场景后再引入。
