# CardKit 能力评估与演进路线

> 状态：P0 容量保护与 recovery 收尾已完成；P2 原生文本流式停在真实端点证据 gate。
>
> 更新时间：2026-07-18（依据真实代码核实修订：优先级重排，纠正两处过时现状）

## 结论

当前 bridge 已经覆盖 CardKit 2.0 在 AI Agent 运行卡场景中的核心展示能力：同一卡片创建与更新、状态标题、Markdown 正文、思考与工具折叠面板、停止和工作目录操作按钮、分栏元数据以及 `card.action.trigger` 回调。

当前已消除容量与重启恢复两个生产可靠性缺口；剩余原生文本流式属于受真实端点证据约束的性能优化：

1. **容量保护已完成**：所有整卡与 callback 卡片都经过 28 KiB / 200-component prepared boundary、分级压缩与静态 emergency fallback；client 在 token、限流、重试和 HTTP 前执行最终硬闸。
2. 正文仍通过节流后的全卡替换更新，没有使用按 `element_id` 更新的原生文本流式接口。用户已能看到打字机式增量，因此这属于**网络开销优化，不是用户可感知的新能力**。
3. 卡片引用持久化与重启恢复**已完成收尾**：`RenderRef` 保存 `CreatedAt` 与 sequence 安全字段；遗留 running 卡片收敛为 interrupted；14 天到期与 sequence unknown 引用在新请求中安全换卡。

因此当前只继续 P2 原生文本流式的证据与安全实现链。普通 streaming 卡已预留唯一空 `answer` target，且 `NativeReady` 只对真实可更新的 streaming payload 开放；真实 raw HTTP probe 已提供但默认 SKIP。由于本机没有显式导出的真实飞书凭据，尚未冻结元素端点的 encoded-body 上限与“明确未应用”错误码，禁止提前启用 production native PUT。访问控制及其下游敏感交互统一放入 P3，本轮不实施；展示组件同样暂缓。

## 评估范围

本备忘评估以下三套实现：

- 自研主线：`lark-ai-agent-bridge/`
- 飞书专精参考：`lark-coding-agent-bridge/`
- 多 Agent、多平台参考：`cc-connect/`

评估只关注 CardKit 卡片的构建、发送、更新、交互和可靠性。会话持久化、访问控制、媒体输入等只在直接影响卡片生命周期时提及。

## CardKit 2.0 能力边界

CardKit 2.0 的主要能力可分为五类：

- 展示与布局：标题、Markdown、图片、表格、图表、人员列表、分栏、折叠面板、图标以及精细化间距和对齐。
- 交互与表单：按钮、链接、输入框、选择器、checker、日期时间选择、表单提交和 Toast。
- 动态更新：全卡更新、配置更新、组件增删改、组件属性更新、批量更新和指定文本组件的流式更新。
- 模板与复用：`template_id`、变量、版本、多语言和权限管理。
- 投放场景：聊天消息、群置顶、链接预览、通知、审批和 AI 问答。

与当前 bridge 直接相关的官方约束包括：

- 卡片 JSON 体积上限约为 30 KB。
- 一张 JSON 2.0 卡片最多支持 200 个组件和元素。
- 卡片实体有效期为 14 天。
- 同一卡片的 OpenAPI 操作要求 `sequence` 严格递增。
- 流式模式要求 `update_multi=true`。
- 用户交互期间不能并发执行流式更新。
- `card.action.trigger` 需要在 3 秒内响应；耗时处理应先快速响应，再异步更新。

官方资料：

- [飞书卡片概述](https://open.feishu.cn/document/feishu-cards/feishu-card-overview)
- [新版卡片说明](https://open.feishu.cn/document/feishu-cards/feishu-card-cardkit/cardkit-upgraded-version-card-release-notes?lang=zh-CN)
- [更新卡片](https://open.feishu.cn/document/uAjLw4CM/ukzMukzMukzM/feishu-cards/update-feishu-card)
- [卡片回传交互回调](https://open.feishu.cn/document/feishu-cards/card-callback-communication?lang=zh-CN)
- [新增组件 API](https://open.feishu.cn/document/cardkit-v1/card-element/create)
- [更新卡片实体配置 API](https://open.feishu.cn/document/cardkit-v1/card/settings?lang=zh-CN)

## 自研 bridge 现状

### 已使用的 CardKit 2.0 能力

卡片 JSON 构建位于 `internal/card/lark_card.go`，明确声明 `schema: "2.0"`，当前使用以下组件和配置：

- `header`：按运行中、完成、停止、失败和工作目录状态切换标题和颜色。
- `markdown`：展示回答、提示信息、思考、工具调用和元数据。
- `collapsible_panel`：分别承载思考推理与工具调用。
- `button`：停止、创建工作目录和取消操作，终态按钮置为 disabled。
- `column_set` / `column`：分两行展示 agent、model、tokens、user、IP 和 workdir。
- `hr`、`plain_text`、`standard_icon`。
- `summary`、`update_multi`、`streaming_mode` 和 `streaming_config`。

CardKit 客户端位于 `internal/feishu/cardkit_client.go`，当前生产路径使用：

- `POST /open-apis/cardkit/v1/cards` 创建卡片实体。
- 回复消息时使用 `{type:"card", data:{card_id}}` 引用卡片实体。
- `PUT /open-apis/cardkit/v1/cards/{card_id}` 全量更新卡片。
- 递增 `sequence` 和稳定 `uuid` 保证顺序和幂等。
- 租户 token 缓存、串行限速、可重试错误重试和审计记录。

按钮使用新版长连接 `card.action.trigger`。回调同步返回终态卡片，让客户端立即更新按钮状态；异步 CardKit update 作为兜底和审计证据。

### 当前“流式更新”的准确含义

当前卡片设置了 `streaming_mode` 和 `streaming_config`，bridge 也会按 `E2E_CARD_UPDATE_MS` 节流更新同一张卡片。但是每一帧调用的是全卡更新接口，而不是：

```text
PUT /open-apis/cardkit/v1/cards/{card_id}/elements/{element_id}/content
```

因此当前实现属于“节流全卡刷新”，不是 CardKit 的原生文本组件流式更新。用户已经能够看到增量结果，但每次都会重新提交标题、折叠面板、按钮和元数据，网络开销和失败面大于局部文本更新。

### 当前容量与恢复能力

已有能力：

- `E2E_CARD_MAX_CHARS` 默认限制为 12000 字符。
- 长内容支持截断或分页。
- CardKit client 对部分网络错误和服务端错误进行重试。

尚缺能力：

- 最终序列化 JSON 的字节数检查。
- 组件和元素数量检查。
- reasoning、tool 和 answer 分区的分级压缩策略。
- CardKit oversize、expired、not-found、invalid-sequence、interaction-in-progress 等错误的类型化分类。

已具备能力（此前评估偏保守，实测已落地）：

- `card_id`、reply message ID、最后成功 `sequence` 已持久化：封装为 `session.RenderRef{CardID, ReplyMessageID, Version}`，随 `Batch` 通过 `SaveSnapshot` 原子写盘（`session/store.go`）。
- 重启后已能 rehydrate：`CardTarget.Rehydrate` 用持久化的 `CardID/ReplyMessageID/Version` 重建 renderer 并恢复 `sequence`（`bridge/service.go:245`、`cardkit_renderer.go:84-93`）。
- 已有单卡串行分配 `sequence`：每 session 一个 `CardKitRenderer`，`renderer.mu` 与上层 `agentCardStream.renderMu` 双层锁保证顺序，无需再新造独立 coordinator（`cardkit_renderer.go:172-186`、`stream_card.go`）。

遗留缺口（属重启恢复的收尾，非新工程）：

- 遗留 running 卡片恢复后仅恢复了 `sequence`，尚未主动关闭 `streaming_mode`、置灰按钮并收敛为 interrupted。
- `ActiveBatch` 结束清空后，对应 `RenderRef` 不再随快照保留；以及 14 天过期后应新建卡。

`CardKitClient.UpdateSettings` 已定义，但当前没有生产调用方，不能视为已落地能力。

### 覆盖度判断

以下比例是工程估算，不是官方指标：

- 对 AI Agent 运行卡的核心体验，当前覆盖约 65%～75%。
- 对 CardKit 2.0 完整平台能力，当前覆盖约 25%～30%。

前一个比例较高，是因为回答、思考、工具、状态、停止和元数据已经形成闭环；后一个比例较低，是因为表单、选择器、图片、图表、模板、组件级更新等大量平台能力没有使用。

## 参考项目评估

### `lark-coding-agent-bridge`

该仓与自研 bridge 的业务模型最接近，主要参考价值在卡片交互和生命周期。

已使用的 CardKit 2.0 能力：

- AI 运行卡展示回答、reasoning、工具状态、idle timeout、error 和 interrupted。
- reasoning 和工具使用独立折叠面板；工具失败使用红色边框。
- 工具调用较多时聚合成摘要，避免完整 input/output 撑爆卡片。
- 展示 model、input/output token、cached token 和上下文剩余比例。
- 使用停止按钮和新版 callback behavior。
- 使用 `form`、`select_static`、`input`、`column_set` 和 submit/cancel 实现 `/config` 与 `/account`。
- 正确从 CardKit 2.0 回调的 `form_value` 获取表单值。
- `card_id` 引用消息发送失败时降级为 raw card；分别支持按 `card_id` 和 `message_id` 更新。
- 支持追加每轮回复、复用最新卡片、运行卡完成后只保留最终答案等显示策略。
- 卡片流失败时发送最终兜底回复。
- 回调前校验访问权限、active run、scope 和签名 token。

值得借鉴：

- `/config` 表单结构和 `form_value` 处理。
- managed card 的 `card_id` / raw card 双路径降级。
- 工具调用摘要和卡片体积控制思路。
- latest-card、append-clean-card 等回复显示策略。
- 回调身份、scope 和 active run 绑定。

不建议照搬：

- 进程内 `message_id -> card_id` 映射，重启后仍会丢失。
- 帮助、workspace 列表等旧格式卡片；自研已经统一使用 JSON 2.0，无需重新引入 1.0 双轨。

### `cc-connect`

该仓同时存在通用旧格式交互卡和 AI CardKit 2.0 富卡。整体架构面向多 Agent、多平台，对当前自研体量偏重，但底层可靠性实现参考价值高。

AI CardKit 2.0 路径已经使用：

- `card_id` 卡片实体创建与引用发送。
- 带 `element_id` 的 Markdown 正文。
- `PUT .../elements/{element_id}/content` 原生文本流式更新。
- 全卡更新和文本流式更新共享同一递增 `sequence`。
- 原生流式不可用时降级为全卡更新或消息 Patch。
- reasoning/tools 折叠面板、标准图标、状态标题和小字号状态栏。
- `streaming_mode`、`update_multi` 和 `enable_forward_interaction`。
- 28 KB 软上限和多级压缩策略。
- 超限时优先保留最近 reasoning/tool 步骤，最终降级为紧凑 Markdown。
- Markdown 表格数量控制、标题兼容、图片引用和 URL 清理。
- 对 rate limit、table limit、oversize 等错误做差异化处理。

值得借鉴：

- 原生文本流式端点的调用和 fallback。
- 全卡更新与组件更新共享 `sequence` 的并发控制。
- 28 KB 字节上限、最近步骤保留和非空降级策略。
- CardKit API 错误分类和可降级错误处理。
- Markdown 进入 CardKit 前的兼容性清理。

不建议照搬：

- 通用 `core.Card` 和完整平台能力接口；除非确定进入多平台阶段。
- JSON 1.0 通用交互卡渲染器。
- 与当前单 Agent、单平台无关的插件注册和 build tag 体系。

## 推荐路线

> 优先级已按「发生概率高、验证成本低、影响范围大」重排。原 P0「原生文本流式更新」因用户已能看到增量、且实现最复杂、最易引入乱序回归，降级为 P2 性能优化；原 P1「持久化恢复」因代码已基本落地，降级为 P0 收尾。

### P0：JSON 容量保护与错误降级（唯一线上炸点，先做）

**状态：已完成（2026-07-18）。** `internal/card` 负责最终 JSON 的 28 KiB / 200-component 测量、不可伪造的 `PreparedLarkCard` 与分级压缩；`internal/feishu` 在 Create/Update 前做最后校验并发送精确预检字节。长连接 callback 与 HTTP compatibility callback 都使用同一 prepared factory，因此同步返回卡片也受保护。无法保留可用调用方内容时，使用不含 session、workdir、model、tool output 或 action 的静态 emergency 卡。

目标：在发送前确定卡片不会因为体积或组件数量超限而中断整个回复。

已实现方案：

1. 以最终 `json.Marshal` 后的字节数为准，设置约 28 KB 软上限。插入点即 `cardkit_client.go` 中 `UpdateCard`/`CreateCard` 现有 `json.Marshal(req.Card)` 之后。
2. 统计组件和元素数量，预留终态按钮和 footer 的空间。
3. 按以下顺序压缩：旧工具完整输出、旧思考内容、旧工具摘要、正文末尾。
4. 无正文时必须保留最近工具或思考摘要，禁止降级成空白卡片。
5. 错误类型化**先只做 oversize 一类**；expired、not-found、invalid-sequence、interaction-in-progress 等目前无消费方，属过度设计，留到真遇到再加。
6. transient error 保留映射并重试；明确 stale/expired 后才清理映射并创建新卡。

验收条件：

- 超长 reasoning/tool 组合不会生成超过软上限的 payload。
- 压缩后仍可识别当前状态、最近工具和最终回答。
- 卡片超限错误不会导致 Agent run 失败或用户无最终回复。
- 单测覆盖每一级压缩和最终 fallback（用现有 `fakeCardKitClient` 即可，不依赖真飞书）。

### P0 收尾：重启恢复的两个缺口

**状态：已完成（2026-07-18）。** `RenderRef.CreatedAt` 在 CardKit create 成功时捕获、reply 绑定成功后提交且后续不可刷新；latest-card 在 14 天到期或 sequence unknown 时先持久清理旧映射再为当前请求建卡；重启 recovery 只更新原卡，unknown 引用仅记录 `recovery_card_update_skipped_sequence_unknown`，不续写也不创建替代卡。

目标：补齐已落地的持久化恢复能力，而非新建存储。`RenderRef` 落盘与 `Rehydrate` 已实现（见「当前容量与恢复能力」），此项只补两个边界。

建议方案：

1. 在现有 `Rehydrate` 路径上，对遗留 running 卡片补一次终态全卡更新：关闭 `streaming_mode`、置灰按钮、标题收敛为 interrupted。
2. 处理 `ActiveBatch` 结束后 `RenderRef` 不再随快照保留的边界，以及 14 天过期后自动新建卡。

验收条件：

- 重启后不会复用低于历史值的 `sequence`（现已满足，回归保护）。
- 遗留 running 卡片可更新为 interrupted。
- 超过 14 天或服务端明确返回 stale 时自动创建新卡。
- 网络或 5xx 错误不会误删有效映射。

### P1：现有 CardKit 配置表单收口（已完成，不新增能力）

当前 `/config` CardKit 2.0 表单、`form_value` 解析和全局 model/effort/reply mode 偏好持久化已经实现。此处不再作为待开发功能；在 P3 访问控制落地前，维持“仅部署在个人可控 chat/tenant”的既有适用边界，不继续扩大配置表单能力或投放范围。

目标：在需要时用 `/config` 卡片承载 model、effort、回复模式等少量运行偏好。

建议仅引入当前确有业务入口的组件：

- `form`
- `select_static`
- `input`
- submit/cancel button
- `form_value`

后续若进入多人或不可信 chat/tenant，表单必须纳入 P3 的访问控制与 callback capability 校验，不能继续以“卡片可见即可提交”作为授权依据。

P3 安全收口的验收条件：

- 表单值只从可信的 `form_value` 读取。
- 点击者通过 owner/admin/access policy 校验。
- action 绑定 scope、active run 或一次性 nonce，不能跨会话重放。
- 提交后同步返回成功或失败终态，并禁用重复提交。
- secret 不预填、不回显、不进入 audit。

### P2：原生文本流式更新（性能优化，非功能补齐）

> 当前进度：已完成稳定 `answer` target、opaque prepared accessor 边界、`NativeReady` 严格资格判断和脱敏 opt-in raw probe。当前 no-go 是缺少真实端点证据；`UpdateElementContent`、durable journal 与 interaction fence 尚未实现或启用。

> 原列为 P0。降级理由：用户借由现有「节流全卡刷新 + `streaming_mode`」已能看到打字机式增量，本项优化的是**网络开销**而非用户可感知能力；且它是整份 roadmap 里实现最复杂、最易引入乱序 / `invalid sequence` 回归的一项（要引入 element 级接口、处理全卡与文本流式共享 `sequence` 的竞争、以及「交互进行中不能并发流式」的官方限制）。收益/风险比最差，应等真实 E2E 观测到全卡刷新造成明显限流或卡顿再做。

目标：正文增量输出只更新指定 Markdown 组件，结构或状态变化才全量更新卡片。前置依赖（单卡串行分配 `sequence`）已具备，主要风险在竞争处理。

建议方案：

1. 复用回答正文已有的稳定 `element_id`（`answer`，见 `card/lark_card.go`）。
2. 在 CardKit client 增加文本流式更新接口（`PUT .../cards/{card_id}/elements/{element_id}/content`）。
3. 复用现有单卡串行锁统一分配 `sequence`，全卡与文本更新共享同一序号，不各自维护。
4. answer delta 只进入文本更新；思考、工具、header、按钮和 footer 变化进入全卡更新。
5. 完成、失败、停止必须通过全卡更新关闭 `streaming_mode` 并收敛按钮终态。
6. 原生文本流式失败时回退到当前全卡更新，不中断 Agent run。
7. 用户交互进行中收到错误时，跳过中间帧并等待终态更新，不与回调同步换卡竞争。

验收条件：

- 正常回答至少出现一次 `elements/{element_id}/content` 调用。
- 全卡与文本更新的 `sequence` 全局严格递增。
- 流式端点失败后仍能得到完整最终答案。
- stop、error、result 终态与当前行为一致。
- 真实飞书 E2E 能观察到原生打字机效果，audit 能区分 text stream 和 full update。

### P2：回复显示与反馈体验

已完成能力：

- `append`：每轮新卡片。
- `latest-card`：每个 scope 复用最新结果卡。
- `append-clean-card`：运行中展示过程，完成后只保留最终答案。

暂缓到 P3 的能力：

- 完成后增加点赞、点踩或“继续处理”按钮。
- 通过组件级更新删除停止按钮、添加反馈区。

这些新增 action 依赖可靠的 card ref 持久化（已落地）以及 P3 的访问控制、服务端 action 绑定和防重放能力。P3 前不新增反馈或“继续处理”按钮。

### P3：访问控制与敏感 CardKit 交互

本阶段统一承接访问控制模型及其所有下游敏感 action，本轮明确不实施。

目标能力：

- 消息入口按 owner、admin、allowed users 和 allowed chats 做 fail-closed 授权。
- `/config`、`/config reset` 和 `config.save` 只允许 owner/admin。
- stop、workdir、反馈和“继续处理” action 绑定可信 actor、scope、run/card、action、过期时间和一次性 nonce。
- callback capability 持久化并原子消费，进程重启后仍能拒绝重放；策略变化后旧 capability 失效。
- HTTP callback 若保留生产用途，先做可信 transport/签名校验，再进入与长连接相同的授权管线。
- “继续处理”只能从服务端保存的会话上下文创建新输入，不能把客户端任意 `value` 直接作为 prompt。

首版推荐采用本地显式 owner/admin/allowlist + signed nonce capability；飞书 owner 自动发现和 `/invite` 管理命令作为后续增强，不作为首版前置。

### P3：按真实需求引入展示组件

以下能力只在出现明确场景时实施：

- 图片组件：Agent 需要在结果卡中直接展示生成图或截图。
- 表格组件：Markdown 表格无法满足结构化结果展示。
- 图表：出现监控、统计或数据分析输出需求。
- 人员列表：需要审批、通知或负责人展示。
- 模板、变量、多语言：需要企业级通知复用或国际化。

## 明确不做

在没有新需求前，不做以下工作：

- 不为兼容历史参考代码引入 CardKit JSON 1.0。
- 不为了 CardKit 抽象提前改造成多平台 `core.Card`。
- 不为展示技术能力而加入图表、模板、循环容器等无实际消费方的组件。
- 不让 CardKit 网络或渲染失败回滚已经持久化的 Agent 执行状态。
- P3 访问控制落地后，不在 CardKit action 中信任客户端提交的 session、scope、workdir 或配置值，必须用服务端状态和一次性 capability 校验。

## 实施依赖与风险

### `sequence` 竞争

原生文本流式、全卡更新、配置更新和回调后异步更新都操作同一张卡片。若各自维护序号，会产生乱序或 `invalid sequence`。当前已有单卡串行分配（每 session 一个 renderer + `renderer.mu`/`renderMu` 双层锁），新增更新路径必须复用这一序号来源，不新造第二套序号。

### 回调与流式更新竞争

官方限制用户交互进行中不能同时流式更新。回调路径应在 3 秒内同步返回终态或空响应；后台更新器需要识别 interaction-in-progress，并允许跳过非终态帧。

### 卡片实体有效期

`card_id` 仅在 14 天内可更新。持久化映射必须保存创建时间，并仍以服务端 stale/expired 错误为最终依据。

### 完整性与性能的取舍

思考和工具内容越完整，卡片越容易超限。飞书卡片只承担用户可读摘要，完整工具参数和输出应保留在受控日志或 audit 中，不应强行塞进卡片。

### 旧客户端兼容

CardKit 2.0 需要较新的飞书客户端。当前主线已经选择 2.0，不新增 1.0 双轨；如果真实用户存在旧客户端问题，应先通过实际用户和客户端版本证据重新决策。

## 后续实施前检查

每次从本 roadmap 选取功能进入开发前，应重新确认：

- 该能力解决的真实用户问题是什么。
- 是否依赖尚未完成的会话持久化、访问控制或 media 输入。
- 是否能通过 fake CardKit client 做确定性单测。
- 是否需要真实飞书 schema smoke 或 E2E。
- 是否会改变现有执行卡、按钮或撤回消息的关键用户路径。
- 是否需要同步更新 `docs/framework/architecture.md`、`docs/workflow/testing.md` 和 `tasks.md`。

本 roadmap 只记录方向。具体功能进入实施时，仍需形成独立设计、实现计划、测试用例和迁移方案。
