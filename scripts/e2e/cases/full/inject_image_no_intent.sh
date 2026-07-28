#!/usr/bin/env bash
# full: inject_image_no_intent (real_agent_only)
#
# 反向验证 bridge 注入的 feishu-runtime-v3 §图片交付指令 B1(仅明确图片意图时
# 才交付)。用户没有图片意图时,即使工作目录里有 PNG,真 agent 也不应主动写
# markdown inline image。这个断言只对真 Claude 有意义——fake claude 强制吐固定
# JSON,无法证明"AI 抑制了本可发图的冲动"。因此 registry 里标 real_agent_only。
#
# 观测:audit 里不出现 output_image_replied / output_images_completed(succeeded>=1)。
#   允许 output_image_rejected(bridge 校验拒绝)或完全无 output_image_* 事件。
case_inject_image_no_intent() {
  local marker="E2E_${RUN_ID}_INJECT_NOIMG"
  local msg
  local audit_mark_before
  audit_mark_before="$(audit_mark)"
  # 让 agent 先在工作目录准备一个图片文件(诱发它有"图可发"的条件),
  # 然后普通任务不含任何图片意图。若 agent 遵从 B1,不应发图。
  msg="$(send_at "/new 用一行 python 生成一个 3x3 纯蓝色 PNG 到当前目录文件名 ${marker}.png,然后简短告诉我它的字节数是多少。回复正文最多 20 字。")"
  wait_audit "$msg.*event=result"
  # output_images pipeline 会在首个 event=result 之后异步 upload+reply,再打
  # output_images_completed。反向断言前 sleep 3s 让潜在的违规发图有时间落 audit。
  sleep 3
  # 反向断言:本 case 新增的 audit 不应有 output_images_completed 且 succeeded>=1。
  # case 串行 + audit 追加写,mark_before 之后的行即本 case 产生。
  local new_audit
  new_audit="$(tail -n "+$((audit_mark_before + 1))" "$AUDIT" 2>/dev/null)"
  if grep -qE 'output_images_completed.*succeeded=[1-9]|output_image_replied' <<<"$new_audit"; then
    echo "case inject_image_no_intent FAILED: agent 违反 B1,主动发了图片" >&2
    echo "$new_audit" | grep -E 'output_image' | head -5 >&2
    return 1
  fi
  record_message inject_image_no_intent root "$msg" ""
}
