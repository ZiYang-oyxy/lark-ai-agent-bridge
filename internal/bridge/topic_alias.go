package bridge

import "sync"

// SyntheticTopicThreadPrefix is prepended to a Feishu message_id to build a
// per-mention synthetic thread key. Kept as a package constant so the
// sessionKeyForMode router and TopicJoinObserver alias binder always agree.
const SyntheticTopicThreadPrefix = "@bot:"

// TopicAliasStore remembers that a Feishu thread_id (the real omt_* id assigned
// by Feishu when the bot's first CardKit reply lands) is an alias for a
// synthetic "@bot:<msg_id>" thread key we used to run the top-level @bot
// message on. Without this alias, follow-up messages inside the topic (which
// arrive with a real thread_id) would land on a different, empty session and
// lose continuity with the run they replied to.
//
// The store is in-memory. That matches the rest of the session key routing —
// on restart the durable session store already keys by real thread_id, and
// the alias is only needed during the lifetime of the synthetic key.
type TopicAliasStore struct {
	mu      sync.RWMutex
	entries map[topicAliasKey]string
}

type topicAliasKey struct {
	ChatID   string
	ThreadID string
}

// NewTopicAliasStore constructs an empty alias store.
func NewTopicAliasStore() *TopicAliasStore {
	return &TopicAliasStore{entries: make(map[topicAliasKey]string)}
}

// Bind records that (chatID, realThreadID) should resolve to syntheticThread.
// Nil receivers and empty inputs are ignored, so callers can wire this
// unconditionally from CardKit reply callbacks.
func (s *TopicAliasStore) Bind(chatID, realThreadID, syntheticThread string) {
	if s == nil || chatID == "" || realThreadID == "" || syntheticThread == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[topicAliasKey{ChatID: chatID, ThreadID: realThreadID}] = syntheticThread
}

// Resolve returns the synthetic thread previously bound for (chatID,
// realThreadID). Second return is false if no alias is registered.
func (s *TopicAliasStore) Resolve(chatID, realThreadID string) (string, bool) {
	if s == nil || chatID == "" || realThreadID == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.entries[topicAliasKey{ChatID: chatID, ThreadID: realThreadID}]
	return v, ok
}
