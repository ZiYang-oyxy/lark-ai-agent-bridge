package bridge

import (
	"fmt"
	"strings"

	"lark-agent-bridge/internal/media"
	"lark-agent-bridge/internal/session"
)

// BuildBatchPrompt renders an immutable batch in FIFO order without inlining
// attachment contents. Plain single-message prompts retain their legacy form.
func BuildBatchPrompt(batch session.Batch) string {
	if len(batch.Inputs) == 0 {
		return ""
	}
	if len(batch.Inputs) == 1 && len(batch.Inputs[0].Attachments) == 0 && batch.Inputs[0].QuotedText == "" {
		return strings.TrimSpace(batch.Inputs[0].Text)
	}

	var b strings.Builder
	if len(batch.Inputs) > 1 {
		b.WriteString("以下是按时间顺序合并的一批用户消息。\n")
	}
	for i, input := range batch.Inputs {
		if i > 0 || len(batch.Inputs) > 1 {
			b.WriteByte('\n')
		}
		if len(batch.Inputs) > 1 || len(input.Attachments) > 0 {
			fmt.Fprintf(&b, "[消息 %d | %s | %s]\n", i+1, input.Time.Format("2006-01-02 15:04:05"), input.Sender)
		}
		// 顺序：用户主指令 → 引用块 → 附件。以往把引用块摆在指令前，导致长引用体
		// （告警卡 JSON 等）把真正的指令挤到 prompt 末尾，@ 与命令被稀释后 agent
		// 容易忽视。把用户当轮直接说的话放最前面，是"读到的第一件事就是你被要求
		// 做什么"，引用/转发只是补充上下文。
		if text := strings.TrimSpace(input.Text); text != "" {
			b.WriteString(text)
			b.WriteByte('\n')
			if input.QuotedText != "" {
				b.WriteByte('\n')
			}
		}
		writeQuotedBlock(&b, input)
		for _, attachment := range input.Attachments {
			fmt.Fprintf(&b, "[%s] %s\n", attachmentPromptKind(attachment), attachment.Path)
		}
	}
	return strings.TrimSpace(b.String())
}

// writeQuotedBlock renders the message the user replied to as a clearly
// delimited external quote so the agent treats it as referenced context.
//
// 结构极简：一行中性 header（保留 open_id 便于追溯）+ 逐行 `> ` 引文，之后
// 一个空行结束。不按 sender_type 追加任何后缀说明（"来自另一个 bot" / "由
// 你自己发出"）——这些都是对 agent 的解读性暗示，且判定可能失真（同一 App
// 但 self_bot 识别漏、或 upstream sender_type 不准）。header 只承载纯事实
// (open_id)，agent 自己看 open_id 判断即可。
//
// QuotedSenderType 字段仍保留在 session.Input 上供 audit / 未来路由使用，
// 只是不再进入 prompt 文本。
func writeQuotedBlock(b *strings.Builder, input session.Input) {
	quoted := strings.TrimSpace(input.QuotedText)
	if quoted == "" {
		return
	}
	sender := strings.TrimSpace(input.QuotedSender)
	if sender != "" {
		fmt.Fprintf(b, "[用户引用了 %s 的消息]\n", sender)
	} else {
		b.WriteString("[用户引用了一条消息]\n")
	}

	for _, line := range strings.Split(quoted, "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
}

func attachmentPromptKind(attachment media.Attachment) string {
	if strings.HasPrefix(strings.ToLower(attachment.MIME), "image/") {
		return "image"
	}
	return "text-file"
}
