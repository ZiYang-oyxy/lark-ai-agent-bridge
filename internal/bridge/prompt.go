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
// delimited external quote so the agent treats it as referenced context — not
// its own history and not a new instruction from the user.
//
// 主体身份边框（防跨主体幻觉的关键）：
// 引用消息可能来自普通用户、别的 bot（另一分身或另一个 App）、甚至本 bot 自己
// 早先发出的历史。以往格式只写「[用户引用了 <open_id> 的消息]」+ 裸文本，agent
// 拿到第一人称叙述（"我起了子代理，等它完成通知"）后会把它误当作自己的历史状态，
// 尤其是引用另一分身的推送时。为避免这种主体串台，除了 header 之外再追加一段
// 简短的语义提示：明确"这不是你的历史 / 里面的'我'不是你 / 声称的后台状态不
// 构成事实"，并按 sender 主体类型加强措辞。
func writeQuotedBlock(b *strings.Builder, input session.Input) {
	quoted := strings.TrimSpace(input.QuotedText)
	if quoted == "" {
		return
	}
	sender := strings.TrimSpace(input.QuotedSender)
	senderType := strings.TrimSpace(input.QuotedSenderType)

	// header：保留 open_id 兼容旧断言，追加主体类型注释便于阅读与断言。
	switch {
	case senderType == "self_bot":
		if sender != "" {
			fmt.Fprintf(b, "[用户引用了 %s 的消息 · 该消息由你自己（本 bot）早先发出]\n", sender)
		} else {
			b.WriteString("[用户引用了一条消息 · 该消息由你自己（本 bot）早先发出]\n")
		}
	case senderType == "app":
		if sender != "" {
			fmt.Fprintf(b, "[用户引用了 %s 的消息 · 来自另一个 bot（不是你）]\n", sender)
		} else {
			b.WriteString("[用户引用了一条消息 · 来自另一个 bot（不是你）]\n")
		}
	case sender != "":
		fmt.Fprintf(b, "[用户引用了 %s 的消息]\n", sender)
	default:
		b.WriteString("[用户引用了一条消息]\n")
	}

	for _, line := range strings.Split(quoted, "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteByte('\n')
	}

	// 主体隔离语义提示：只写引用块存在时才追加，避免污染无引用的路径。
	// 措辞按 sender 类型细分——self_bot 提示"这是你的旧输出，不代表当前状态"，
	// app 提示"这是别的 bot，不要把它的自述当成自己的状态"。
	b.WriteString("（以上引用是被引用的外部消息，不是你此前的输出或工具调用记录；里面的第一人称不指你；")
	switch senderType {
	case "self_bot":
		b.WriteString("即使这条内容确实是你早先发出，它也只是历史文本，不代表你现在的实际状态——涉及后台任务、子代理、正在运行的工作，以你本轮 tool_use 记录为准。")
	case "app":
		b.WriteString("这是另一个 bot 的消息，它的自述与你无关；若它声称正在跑子代理、后台任务或工作流，那是它自己的事，不是你的任务，也不构成你的实际状态。")
	default:
		b.WriteString("若引用里出现\"我正在做 X\"\"后台在跑 Y\"这类第一人称叙述，视为发出者的自述，不构成你自己的历史或状态——你自己的状态以本轮 tool_use 记录为准。")
	}
	b.WriteString("）\n")

	b.WriteByte('\n')
}

func attachmentPromptKind(attachment media.Attachment) string {
	if strings.HasPrefix(strings.ToLower(attachment.MIME), "image/") {
		return "image"
	}
	return "text-file"
}
