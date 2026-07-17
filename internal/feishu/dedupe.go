package feishu

import (
	"context"
	"sync"
	"time"
)

type MessageDeduper struct {
	mu       sync.Mutex
	ttl      time.Duration
	messages map[string]time.Time
}

func NewMessageDeduper(ttl time.Duration) *MessageDeduper {
	return &MessageDeduper{ttl: ttl, messages: map[string]time.Time{}}
}

func (d *MessageDeduper) SeenBefore(_ context.Context, messageID string, now time.Time) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked(now)
	expireAt, ok := d.messages[messageID]
	if ok && now.Before(expireAt) {
		return true, nil
	}
	d.messages[messageID] = now.Add(d.ttl)
	return false, nil
}

func (d *MessageDeduper) sweepLocked(now time.Time) {
	for messageID, expireAt := range d.messages {
		if !now.Before(expireAt) {
			delete(d.messages, messageID)
		}
	}
}
