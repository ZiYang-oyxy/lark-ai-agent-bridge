#!/usr/bin/env bash
# smoke: quote_readback

# case_quote_readback 真实链路验证 quote 内容完整送达 agent。
# 触发过 2026-07-27 那次"quote 告警卡 + @ + 帮我分析" 被误回 HEARTBEAT_OK 的现场:
# 顺序契约 + prompt 结构靠 L1 unit test + testfw smoke/regression 的 segments_order
# 锁死;本 case 走真 Feishu + 真 Claude,证明 bridge 侧真的把引用体读进了 prompt、
# agent 拿到、并能在回复里复现。步骤:
#   1) send_at 发一条含独一无二 QMARK 的锚点消息(带 @Test,群里可见);
#   2) reply_quote 引用该锚点 + @Test + 明确指令"请只回复引用中出现的以 E2E_ 开头
#      的完整字符串,不要解释、不要调用工具";
#   3) wait_audit 到 event=result,mget 引用回复的卡片;
#   4) assert_file_contains 验证 QMARK 出现在回复正文——真通路里 quote 数据丢失
#      / prompt 顺序颠倒导致 agent 忽略指令,都会让此断言失败。
# marker 用 $RUN_ID 保证幂等:多次 canary 各自的 marker 相互隔离,不受历史消息干扰。
case_quote_readback() {
  local qmark="E2E_${RUN_ID}_QMARK"
  local anchor_msg reply_msg file
  anchor_msg="$(send_at "canary anchor: ${qmark}")"
  # 给锚点一点时间被服务端索引;不 wait_audit 是因为群里未 @ 的锚点会走
  # group_message_skipped(mention_required),这是预期的负向路径,不产生 result。
  sleep 2
  reply_msg="$(reply_quote "$anchor_msg" "/new 请只回复引用消息里出现的以 E2E_ 开头的完整字符串本身,不要解释、不要调用工具。")"
  wait_audit "$reply_msg.*event=result"
  file="$(mget quote_readback "$reply_msg")"
  assert_file_contains "$file" "$qmark"
  record_message quote_readback anchor "$anchor_msg" ""
  record_message quote_readback reply "$reply_msg" "$file"
}
