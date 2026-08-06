# Post Topic Guide ID Design

## Goal

For a top-level `post` message in topic mode, keep the text reply that creates
the real Feishu topic and turn it into useful diagnostic context. The final
message must expose the complete `omt_*` topic ID without creating, recalling,
or replacing another message.

## User-Visible Flow

1. Reply in thread with `🧵 正在创建话题…`.
2. Read the real `thread_id` from the reply response.
3. Update that same text message to:

   ```text
   🧵 话题已创建 · Topic ID: omt_xxx
   AI 回复将在本话题持续更新。
   ```

If Feishu returns an empty `thread_id`, update the same message to:

```text
⚠️ 未获取到 Topic ID，已降级处理
Message ID: om_xxx
AI 回复仍会继续生成。
```

## Architecture

- Add an optional `feishu.MessageUpdater` interface with
  `UpdateTextMessage(ctx, messageID, text) error`.
- Implement it in `SDKSender` with the Feishu `UpdateMessage` API
  (`PUT /open-apis/im/v1/messages/:message_id`). This API edits text and post
  messages sent by the bot; the CardKit `PatchMessage` API is not used.
- `precreateTopicForPost` type-asserts the notifier to `MessageUpdater` after
  the initial reply. Existing senders that do not implement message updates
  continue to work and only produce an audit failure.
- Topic alias binding and the returned `thread_id` remain independent of the
  cosmetic message update. A failed update must never roll back a valid topic.

## Failure Handling

- Initial reply failure: preserve the existing synthetic-thread fallback.
- Empty `thread_id`: update the guide to the explicit degraded message and
  continue through the existing fallback.
- Update API failure or missing updater: retain the original creating message,
  write `topic_precreate_guide_update_failed`, and continue.
- Never call `DeleteMessage` and never send a second diagnostic message.

## Observability

- `topic_precreate_ok` continues to carry the root message, real topic,
  synthetic topic, and guide message IDs.
- `topic_precreate_guide_updated` records the guide message and real topic ID.
- `topic_precreate_guide_update_failed` records the guide message, intended
  state, and error without affecting the Agent run.

## Validation

- Unit tests cover successful update, empty-thread degraded update, missing
  updater, API failure, alias preservation, and zero message deletion.
- L2 simulation asserts the `topic_precreate_guide_updated` state, the complete
  topic ID in `topic_precreate_ok`, and topic-routed CardKit events. Unit tests
  own the exact guide text contract.
- L3 sends a real top-level `post` to Mac Test, then reads the same guide
  message back and asserts `updated=true`, `deleted=false`, and the complete
  `omt_*` value matching the CardKit reply topic.
