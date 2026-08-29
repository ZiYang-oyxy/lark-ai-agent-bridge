# 引用消息发送者姓名补全设计

## 目标与边界

当用户引用飞书消息时，Agent prompt 中的引用头从仅展示 `open_id` 改为优先展示
`姓名(open_id)`，例如 `李俊Bot-Mike(ou_example)`。稳定 ID 必须始终保留，姓名查询失败
不能阻断正文回查或 Agent 执行。

本需求只改引用消息的发送者展示，不改变正文中的 mention 展开、消息路由、权限模型或
sender type 判定。Bridge 运行时不调用本机 `lark-cli`，避免依赖 CLI profile；直接复用
当前 App 的 Feishu SDK 凭据和 IM 权限。

## 方案选择

- 推荐：使用 `im.v1.chatMembers.get`，按引用消息的 `chat_id` 和 sender `open_id` 查群成员
  显示名。该接口与 `lark-im +chat-members-list` 使用同类能力，Bot 在当前会话已有可见性，
  不额外依赖通讯录权限。
- 不选 Contact v3：它可跨会话查用户，但不少 Bot 没有用户通讯录权限，且对 App/Bot
  发送者不如会话成员列表可靠。
- 不选 shell-out `lark-cli`：CLI 的授权 profile 属部署机状态，不应成为 Bridge 生产链路
  依赖，也不利于单元测试和多环境部署。

## 数据流

1. `SDKSender.FetchMessage` 从 `message.Get` 结果保留 `chat_id`、sender ID 和正文。
2. `ChatMemberNameResolver` 用 `chat_id + open_id` 分页查询成员，成功和失败分别做 TTL
   缓存，并限制缓存总量。
3. `quotedMessageFetcher` 将解析出的姓名作为可选字段映射到 bridge domain。
4. `session.Input` 分开保存 `QuotedSender` 和 `QuotedSenderName`；prompt 层仅负责显示格式。

姓名为空、接口失败、成员不在列表或缺少 `chat_id` 时，prompt 保持旧格式
`[用户引用了 <open_id> 的消息]`。sender ID 为空时仍使用通用引用头。

## 验证

- L1：覆盖消息 `chat_id` 提取、成员分页、缓存命中/过期、API 失败、成员缺失，以及
  `姓名(open_id)`/裸 ID/通用头三种 prompt。
- L2：simulate 注入 quote sender name，断言 Agent prompt 可见完整身份。
- L3：Mac Test 真飞书群聊中引用用户或 Bot 消息，确认 audit 完成并从
  `agent-requests.jsonl` 断言引用头包含真实姓名和同一个 open_id。

## 风险与约束

- 成员名可能变更，因此成功缓存使用小时级 TTL；短暂查询失败只做秒级负缓存。
- 大群需要分页，设置合理页大小并受调用 context 控制。
- 身份补全是 best effort，不改变引用正文获取的成功语义。
