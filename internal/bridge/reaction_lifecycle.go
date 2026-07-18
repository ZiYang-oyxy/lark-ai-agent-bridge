package bridge

import (
	"context"
	"sync"
	"time"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/session"
)

const defaultWaitingReactionDelay = 500 * time.Millisecond

// reactionLifecycle owns exactly one Feishu reaction from delayed/immediate
// creation through best-effort deletion. A reaction returned after Close is
// deleted immediately, closing the add-versus-terminal race.
type reactionLifecycle struct {
	mu         sync.Mutex
	sink       feishu.ReactionSink
	audit      *audit.Recorder
	messageID  string
	typeName   feishu.ReactionType
	timer      *time.Timer
	reactionID string
	closed     bool
}

func newReactionLifecycle(sink feishu.ReactionSink, recorder *audit.Recorder, messageID string, typeName feishu.ReactionType, delay time.Duration) *reactionLifecycle {
	if sink == nil || messageID == "" || typeName == "" {
		return nil
	}
	handle := &reactionLifecycle{sink: sink, audit: recorder, messageID: messageID, typeName: typeName}
	if delay > 0 {
		handle.mu.Lock()
		handle.timer = time.AfterFunc(delay, handle.add)
		handle.mu.Unlock()
	} else {
		go handle.add()
	}
	return handle
}

func (h *reactionLifecycle) add() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.timer = nil
	h.mu.Unlock()

	reactionID, err := h.sink.AddReaction(context.Background(), h.messageID, h.typeName)
	if err != nil {
		h.record("reaction_add_failed", err.Error())
		return
	}
	if reactionID == "" {
		h.record("reaction_add_failed", "empty reaction id")
		return
	}

	h.mu.Lock()
	if !h.closed {
		h.reactionID = reactionID
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	h.delete(reactionID)
}

func (h *reactionLifecycle) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	reactionID := h.reactionID
	h.reactionID = ""
	h.mu.Unlock()
	if reactionID != "" {
		go h.delete(reactionID)
	}
}

func (h *reactionLifecycle) delete(reactionID string) {
	if err := h.sink.DeleteReaction(context.Background(), h.messageID, reactionID); err != nil {
		h.record("reaction_delete_failed", err.Error())
	}
}

func (h *reactionLifecycle) record(action, detail string) {
	if h.audit != nil {
		h.audit.Record("system", action, h.messageID, "type="+string(h.typeName)+" "+detail)
	}
}

func (s *Service) startWaitingReaction(messageID string) {
	handle := newReactionLifecycle(s.Reactions, s.Audit, messageID, feishu.ReactionTypeOneSecond, s.reactionDelay)
	if handle == nil {
		return
	}
	s.mu.Lock()
	previous := s.waitingReactions[messageID]
	s.waitingReactions[messageID] = handle
	s.mu.Unlock()
	previous.Close()
}

func (s *Service) closeWaitingReaction(messageID string) {
	if messageID == "" {
		return
	}
	s.mu.Lock()
	handle := s.waitingReactions[messageID]
	delete(s.waitingReactions, messageID)
	s.mu.Unlock()
	handle.Close()
}

func (s *Service) closeBatchWaitingReactions(batch session.Batch) {
	for _, input := range batch.Inputs {
		s.closeWaitingReaction(input.ID)
	}
}

func (s *Service) closeAllWaitingReactions() {
	s.mu.Lock()
	handles := make([]*reactionLifecycle, 0, len(s.waitingReactions))
	for _, handle := range s.waitingReactions {
		handles = append(handles, handle)
	}
	s.waitingReactions = map[string]*reactionLifecycle{}
	s.mu.Unlock()
	for _, handle := range handles {
		handle.Close()
	}
}

func (s *Service) startTypingReaction(messageID string) *reactionLifecycle {
	return newReactionLifecycle(s.Reactions, s.Audit, messageID, feishu.ReactionTypeTyping, 0)
}
