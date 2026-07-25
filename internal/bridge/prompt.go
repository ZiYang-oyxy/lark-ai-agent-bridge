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
		writeQuotedBlock(&b, input)
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

// writeQuotedBlock renders the message the user replied to as a clearly
// delimited quote so the agent treats it as referenced context rather than the
// user's own new instruction.
func writeQuotedBlock(b *strings.Builder, input session.Input) {
	quoted := strings.TrimSpace(input.QuotedText)
	if quoted == "" {
		return
	}
	if sender := strings.TrimSpace(input.QuotedSender); sender != "" {
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
