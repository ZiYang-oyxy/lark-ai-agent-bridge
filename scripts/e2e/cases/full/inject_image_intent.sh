#!/usr/bin/env bash
# full: inject_image_intent
#
# 验证 bridge 注入的 feishu-runtime-v3 §图片交付指令(B2/B4/B8)对真 Claude 的
# 实际影响:用户明确要求"画图并发出"时,真 agent 应在最终回复正文写 markdown
# inline image,bridge 才有可能把它作为飞书图片消息上传。
#
# 观测:真 Claude 写 ![...](./x.png) → bridge output_images 管线上传 → audit
#   出现 output_image_replied 或 output_images_completed(succeeded>=1)。
# 反向标记:此 case real_agent_only,fake claude 模式下 SKIP(见 registry.sh)。
# fake 模式下 bridge 图片管线由现有 media 用例覆盖。
case_inject_image_intent() {
  local marker="E2E_${RUN_ID}_INJECT_IMG"
  local msg
  # 明确图片意图 + 简单可执行的生成任务 + 唯一 marker 便于跨用例定位。
  local audit_mark_before
  audit_mark_before="$(audit_mark)"
  msg="$(send_at "/new 请画一张 3x3 像素的纯红色 PNG 图片(用 python 或任何工具生成到当前工作目录),文件名以 ${marker} 开头,然后把图片发出来。不要只说明或返回路径。")"
  # 直接等 output_images_completed(bridge 图片管线完成)。output_image_* audit 用
  # session_id 而非 message_id,但 case 串行执行且 audit 只增长,所以 audit_mark_before
  # 之后的新 audit 行即本 case 产生;等 completed 出现即证明 bridge 已完成图片上传。
  # 不能等第一个 event=result:agent 通常先渲染 result(sequence=N)再触发图片上传,
  # completed 是 result 之后再打的,过早断言会漏。
  wait_audit_since "$audit_mark_before" 'output_images_completed'
  # bridge 指令 B2/B4/B8 生效的标志:真 Claude 输出了 markdown inline image,
  # bridge output_images 管线上传成功。这是"注入指令改变 AI 行为"的行为层证据。
  local new_audit
  new_audit="$(tail -n "+$((audit_mark_before + 1))" "$AUDIT" 2>/dev/null)"
  if ! grep -qE 'output_images_completed.*succeeded=[1-9]' <<<"$new_audit"; then
    echo "case inject_image_intent FAILED: agent 未按 B4 输出 markdown inline image(未观察到 output_images_completed succeeded>=1)" >&2
    echo "本 case 新增 audit:" >&2
    echo "$new_audit" | grep -E 'output_image|event=result' | head -5 >&2
    return 1
  fi
  record_message inject_image_intent root "$msg" ""
}
