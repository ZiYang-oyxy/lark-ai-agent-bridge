package bridge

import (
	"time"
)

// DebounceFor returns the debounce interval for an incoming message.
func DebounceFor(msg Message) time.Duration {
	if msg.IsGroup || msg.HasAttachments || len(msg.Attachments) > 0 {
		return 600 * time.Millisecond
	}
	return 250 * time.Millisecond
}
