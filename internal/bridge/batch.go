package bridge

import (
	"fmt"
	"strings"
	"time"

	"lark-agent-bridge/internal/session"
)

// DebounceFor returns the debounce interval for an incoming message.
func DebounceFor(msg Message) time.Duration {
	if msg.IsGroup || msg.HasAttachments {
		return 600 * time.Millisecond
	}
	return 250 * time.Millisecond
}

// BuildBatchPrompt renders a batch of inputs in arrival order.
func BuildBatchPrompt(inputs []session.Input) string {
	if len(inputs) == 0 {
		return ""
	}
	if len(inputs) == 1 {
		return strings.TrimSpace(inputs[0].Text)
	}

	var b strings.Builder
	b.WriteString("以下是按时间顺序合并的一批用户消息。\n")
	for i, input := range inputs {
		fmt.Fprintf(&b, "\n[消息 %d | %s | %s]\n%s\n", i+1, input.Time.Format("2006-01-02 15:04:05"), input.Sender, strings.TrimSpace(input.Text))
	}
	return strings.TrimSpace(b.String())
}
