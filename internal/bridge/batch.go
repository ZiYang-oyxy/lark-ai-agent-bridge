package bridge

import (
	"strings"
	"time"
)

// DebounceFor returns the debounce interval for an incoming message.
func DebounceFor(msg Message) time.Duration {
	messageType := strings.ToLower(strings.TrimSpace(msg.MessageType))
	if messageType == "" && strings.TrimSpace(msg.Text) != "" && !msg.HasAttachments && len(msg.Attachments) == 0 {
		messageType = "text"
	}
	if msg.HasAttachments || len(msg.Attachments) > 0 || messageType != "text" {
		return time.Second
	}
	if msg.IsGroup {
		return 600 * time.Millisecond
	}
	return 250 * time.Millisecond
}
