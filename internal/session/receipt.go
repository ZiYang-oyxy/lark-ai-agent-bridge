package session

import "time"

// Receipt records a successfully accepted platform message until its
// expiration, preventing a retried delivery from running again.
type Receipt struct {
	MessageID string    `json:"message_id"`
	ExpiresAt time.Time `json:"expires_at"`
}
