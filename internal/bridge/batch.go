package bridge

import (
	"time"
)

// DebounceFor returns the debounce interval for an incoming message.
func DebounceFor(msg Message) time.Duration {
	if msg.IsGroup || msg.HasAttachments {
		return 600 * time.Millisecond
	}
	return 250 * time.Millisecond
}
