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
	if len(batch.Inputs) == 1 && len(batch.Inputs[0].Attachments) == 0 {
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
		fmt.Fprintf(&b, "[消息 %d | %s | %s]\n", i+1, input.Time.Format("2006-01-02 15:04:05"), input.Sender)
		if text := strings.TrimSpace(input.Text); text != "" {
			b.WriteString(text)
			b.WriteByte('\n')
		}
		for _, attachment := range input.Attachments {
			fmt.Fprintf(&b, "[%s] %s\n", attachmentPromptKind(attachment), attachment.Path)
		}
	}
	return strings.TrimSpace(b.String())
}

func attachmentPromptKind(attachment media.Attachment) string {
	if strings.HasPrefix(strings.ToLower(attachment.MIME), "image/") {
		return "image"
	}
	return "text-file"
}
