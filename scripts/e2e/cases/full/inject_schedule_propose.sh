#!/usr/bin/env bash
# full: inject_schedule_propose (real_agent_only)
#
# 验证 bridge 注入的 feishu-runtime-v3 §自然语言定时意图指令(C1/C3/C5)对真
# Claude 的实际影响。用户用自然语言表达重复定时时,真 agent 应:
#   C1: 只用 "$LAB_SCHEDULE_CLI schedule propose",不用内建 CronCreate/at/sleep
#   C3: 用 --kind cron(重复) + cron 表达式,只调一次
#   C5: 措辞为"待确认"而非"已创建",承认 proposal ≠ 已启用
#
# 观测:proposal 成功创建 → bridge 渲染 schedule_confirmation 卡片(header
#   "⏰ 确认定时任务",正文含 "请确认定时任务"、"规则")。
# 反向标记 real_agent_only:fake claude 不会真的去调 LAB_SCHEDULE_CLI,无从谈起。
# 副作用:e2e-real 的 default-workdir 隔离到 /tmp/lark-agent-bridge-real-$RUN_ID,
#   schedule store/socket 都在隔离目录里,run 结束整体清理,不污染 supervisor。
case_inject_schedule_propose() {
  local marker="E2E_${RUN_ID}_INJECT_SCHED_CRON"
  local msg file
  local audit_mark_before
  audit_mark_before="$(audit_mark)"
  # 明确的重复定时意图 + 唯一 marker 便于卡片回读定位。
  msg="$(send_at "/new 每天早上 9 点提醒我做一件事:回复只包含 ${marker} 这个字符串,不解释、不调工具。")"
  # 关键行为断言:真 agent 走了 schedule propose,bridge 用 FinishTransformed
  # 渲染 schedule_confirmation 卡片(专用 event type,不是 result)。等到这条即证明
  # agent 走对了路径;若 agent 用 CronCreate/at/sleep 内建定时,不会触发这个事件。
  wait_audit_since "$audit_mark_before" "$msg.*event=schedule_confirmation" \
    || { echo "case inject_schedule_propose FAILED: 未观察到 schedule_confirmation event,agent 可能没走 schedule propose 路径" >&2; return 1; }
  file="$(mget inject_schedule_propose "$msg")"
  # 卡片正文来自 scheduleConfirmationEvent 模板。mget 只返回 <card>...</card>
  # 文本正文,不含 Actions[].Label(按钮 label 不进 mget content)。
  assert_file_contains "$file" "请确认定时任务"
  # 规则关键词:agent 应把"每天 9 点"翻译成描述里含"9"或"09"。
  # 不锁死具体格式(cron 表达式细节容许 agent 变体),只锁"规则"字段存在。
  assert_file_contains "$file" "规则"
  # 反向:不应出现"已创建/已启用"这类越权措辞。
  if grep -qE '已创建|已启用|已生效|scheduled successfully' "$file"; then
    echo "case inject_schedule_propose FAILED: agent 违反 C5,声称任务已创建" >&2
    return 1
  fi
  record_message inject_schedule_propose root "$msg" "$file"
}
