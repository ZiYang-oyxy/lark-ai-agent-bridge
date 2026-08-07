# CardKit Renderer 有界回收设计

## 目标

`CardKitRouterRenderer` 当前按 `RunCardSessionID` 永久保留 renderer。新 run 持续产生新 key，进程内存会随累计运行次数线性增长。

本次只为 router 增加有界回收，不改变 CardKit 创建、更新、sequence、恢复和 action 业务语义。

## 设计约束

- 正在 streaming 的 renderer 不因容量或 TTL 被淘汰。
- 正在处理 CardKit action 的 renderer 不被淘汰。
- 只有成功渲染 `result`、`error` 或 `stopped` 的 renderer 才进入 terminal 候选集。
- terminal renderer 默认最多保留 512 个。
- terminal renderer 默认最长保留 24 小时，与默认 `ActionGrant` TTL 对齐。
- 如果全部 renderer 都处于 active 或 interaction 状态，允许暂时超过容量上限。
- 不新增后台 goroutine；在创建、rehydrate、render 完成和 interaction 结束时同步执行轻量 sweep。
- 不把回收策略泄漏到 `bridge.Service` 或 `reply.Policy`。

## 数据模型

`CardKitRouterRenderer` 的 map value 从裸 `*CardKitRenderer` 收敛为内部 entry，记录：

- renderer 指针；
- 最后使用时间；
- terminal 状态；
- 正在渲染与正在交互的计数；
- entry 级 render mutex，用于保持底层渲染与生命周期完成回调的顺序一致。

测试构造器可注入较小的容量和 TTL；公开生产构造器继续使用固定默认值，避免本轮扩张配置面。

## 回收规则

每次 sweep 分两步：

1. 删除超过 TTL、且当前没有 interaction 的 terminal entry。
2. 如果 terminal 数量仍超过 512，按最后使用时间从旧到新删除，直到回到上限。

容量只约束 terminal entry，不约束 active entry。这样高并发运行不会因 cache 管理而丢失正在写入的卡片。

`BeginCardInteraction` 找到 entry 后先在 router mutex 下增加 interaction 计数并刷新最后使用时间，再释放 router mutex、更新 renderer 的 `interactionDepth`。release callback 先更新 renderer，再更新 entry 并触发 sweep；两类 mutex 不嵌套持有。

sweep 只读取 router entry 的计数，不在持有 router mutex 时等待 renderer mutex。这样慢 CardKit 请求不会阻塞其他 renderer 的创建、交互和回收。

## 渲染语义

- `NewStreamingBound` 和 `RehydrateBound` 创建 active entry。
- `Render` 和直接持有的 resumable renderer 在调用底层网络渲染前增加 entry 的 rendering 计数并临时退出 terminal 集合；完成后减少计数并刷新最后使用时间。
- direct router 路径在释放 router mutex 前完成 rendering pin，避免 lookup 与 pin 之间被并发 sweep 淘汰。
- 同一 entry 的渲染与完成回调共用 render mutex，最终 terminal 状态按实际渲染完成顺序收敛。
- 成功渲染 terminal event 后将 entry 标记 terminal，再执行 sweep。
- 渲染失败不标记 terminal，保留 renderer 供既有失败处理与重试路径使用。
- 被淘汰的旧 action 若缺少 `ReplyToMessageID`，保持现有 router 行为：不能凭空创建替代卡片，而是返回缺少 reply message ID 的错误。

为保证通过 `ResumableRenderer` 直接调用 `Render` 时 router 也能观察生命周期，router 返回一个内部 tracking wrapper；wrapper 在底层渲染前后回调 router 更新 entry 元数据，不改变 `RenderRef`，并继续转发 `RenderContext` 的调用方 deadline。

## 测试策略

严格按 TDD 增加以下确定性测试：

- 超过容量时只淘汰最旧 terminal renderer。
- active renderer 不因容量压力被淘汰。
- interaction 期间 terminal renderer 不被淘汰，release 后可被回收。
- terminal renderer 超过 TTL 后被回收。
- terminal 渲染失败时不进入可回收状态。
- tracking wrapper 保持原有 `RenderRef` 和 sequence 行为。
- `go test -race` 覆盖 render、interaction 与 sweep 并发。

## 明确不做

- 不立即释放所有 terminal renderer。
- 不新增可配置环境变量。
- 不修改持久化 `RenderRef` 的 14 天策略。
- 不处理 Agent 凭据隔离、deadline、completion WAL 或 CI；这些任务只登记到 `tasks.md`。
