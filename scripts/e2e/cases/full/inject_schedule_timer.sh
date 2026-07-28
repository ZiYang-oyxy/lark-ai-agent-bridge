#!/usr/bin/env bash
# full: inject_schedule_timer (real_agent_only)
#
# 验证 bridge 注入的 feishu-runtime-v3 §自然语言定时意图指令 C2(区分
# cron vs timer)。用户表达"N 小时后/明天下午 3 点"这类一次性定时,真 agent
# 应用 --kind timer 而非 cron。
#
# 观测:proposal 成功 → confirmation 卡片正文里"规则"描述应显现单次执行语义
#   (含具体时间点,不含"每天/每周"重复词)。draft.Description 由 NormalizeProposal
#   根据 kind 生成,timer 类型描述形如具体时间点。
case_inject_schedule_timer() {
  local marker="E2E_${RUN_ID}_INJECT_SCHED_TIMER"
  local msg file
  local audit_mark_before
  audit_mark_before="$(audit_mark)"
  # 明确的一次性定时意图。用"两小时后"避免涉及具体日期歧义。
  msg="$(send_at "/new 两小时后提醒我:回复只包含 ${marker} 这个字符串,不解释、不调工具。")"
  # 同 inject_schedule_propose:timer proposal 终态是 event=schedule_confirmation。
  # 若 agent 误用内建 timer/sleep,不会触发这个事件。
  wait_audit_since "$audit_mark_before" "$msg.*event=schedule_confirmation" \
    || { echo "case inject_schedule_timer FAILED: 未观察到 schedule_confirmation event,agent 可能没走 schedule propose 路径" >&2; return 1; }
  file="$(mget inject_schedule_timer "$msg")"
  # 与 propose case 一致的锚点(共用同一卡片模板;mget 只返回卡片正文,不含按钮 label)。
  assert_file_contains "$file" "请确认定时任务"
  assert_file_contains "$file" "规则"
  # 反向:一次性 timer 不该被 agent 误判成 cron,规则描述不应含重复词。
  # 只锁最直接的重复词,不深挖 cron 表达式格式(容许 agent 变体)。
  if grep -qE '每天|每周|每小时|每分钟|每月|daily|weekly|hourly' "$file"; then
    echo "case inject_schedule_timer FAILED: agent 违反 C2,把一次性 timer 误判为重复 cron" >&2
    grep -E '规则|每' "$file" | head -3 >&2
    return 1
  fi
  # 反向:声称已创建
  if grep -qE '已创建|已启用|已生效' "$file"; then
    echo "case inject_schedule_timer FAILED: agent 违反 C5,声称任务已创建" >&2
    return 1
  fi
  record_message inject_schedule_timer root "$msg" "$file"
}
