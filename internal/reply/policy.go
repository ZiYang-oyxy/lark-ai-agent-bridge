package reply

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/session"
)

const latestCardTTL = 14 * 24 * time.Hour

type CardTarget interface {
	NewStreaming(context.Context, string, string) (feishu.ResumableRenderer, error)
	AppendTerminal(context.Context, string, card.Event) error
	Rehydrate(string, session.RenderRef) feishu.ResumableRenderer
}

type Policy struct {
	target CardTarget
	store  *Store
	now    func() time.Time
}

func NewPolicy(target CardTarget, store *Store) *Policy {
	return &Policy{target: target, store: store, now: func() time.Time { return time.Now().UTC() }}
}

type Run struct {
	mu        sync.Mutex
	ctx       context.Context
	mode      config.ReplyMode
	target    CardTarget
	store     *Store
	scope     string
	sessionID string
	replyTo   string
	renderer  feishu.ResumableRenderer
}

func (p *Policy) Begin(ctx context.Context, mode config.ReplyMode, scope, sessionID, replyTo string) (*Run, error) {
	if p == nil || p.target == nil {
		return nil, errors.New("reply card target is unavailable")
	}
	if mode == "" {
		mode = config.ReplyModeAppend
	}
	switch mode {
	case config.ReplyModeAppend, config.ReplyModeAppendCleanCard, config.ReplyModeLatestCard:
	default:
		return nil, fmt.Errorf("unsupported reply mode %q", mode)
	}
	run := &Run{ctx: ctx, mode: mode, target: p.target, store: p.store, scope: scope, sessionID: sessionID, replyTo: replyTo}
	if mode == config.ReplyModeLatestCard && p.store != nil {
		if ref := p.store.GetLatest(scope); ref != nil {
			if p.discardLatestRef(*ref) {
				if err := p.store.SetLatest(scope, nil); err != nil {
					return nil, err
				}
			} else {
				run.renderer = p.target.Rehydrate(sessionID, *ref)
			}
		}
	}
	if run.renderer == nil {
		renderer, err := p.target.NewStreaming(ctx, sessionID, replyTo)
		if err != nil {
			return nil, err
		}
		run.renderer = renderer
	}
	return run, nil
}

func (p *Policy) discardLatestRef(ref session.RenderRef) bool {
	if ref.SequenceUnknown {
		return true
	}
	if ref.CreatedAt.IsZero() {
		return false
	}
	return !p.now().UTC().Before(ref.CreatedAt.UTC().Add(latestCardTTL))
}

func (r *Run) Render(event card.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode == config.ReplyModeAppendCleanCard && terminalEvent(event) {
		event = cleanTerminalEvent(event)
		if err := r.renderer.Render(event); err != nil {
			return r.target.AppendTerminal(r.ctx, r.replyTo, event)
		}
		return nil
	}
	if err := r.renderer.Render(event); err != nil {
		if r.mode != config.ReplyModeLatestCard || !errors.Is(err, feishu.ErrStaleRenderRef) {
			return err
		}
		if r.store != nil {
			if clearErr := r.store.SetLatest(r.scope, nil); clearErr != nil {
				return clearErr
			}
		}
		renderer, newErr := r.target.NewStreaming(r.ctx, r.sessionID, r.replyTo)
		if newErr != nil {
			return newErr
		}
		r.renderer = renderer
		if renderErr := r.renderer.Render(event); renderErr != nil {
			return renderErr
		}
	}
	return r.persistLatest()
}

func (r *Run) RenderRef() session.RenderRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.renderer == nil {
		return session.RenderRef{}
	}
	return r.renderer.RenderRef()
}

func (r *Run) persistLatest() error {
	if r.mode != config.ReplyModeLatestCard || r.store == nil {
		return nil
	}
	ref := r.renderer.RenderRef()
	if ref.CardID == "" {
		return errors.New("reply renderer did not expose a card id")
	}
	return r.store.SetLatest(r.scope, &ref)
}

func terminalEvent(event card.Event) bool {
	switch event.Type {
	case "result", "error", "stopped":
		return true
	default:
		return false
	}
}

func cleanTerminalEvent(event card.Event) card.Event {
	segments := make([]card.Segment, 0, len(event.Segments))
	for _, segment := range event.Segments {
		if segment.Kind == card.SegmentText || segment.Kind == card.SegmentError {
			segments = append(segments, segment)
		}
	}
	event.Segments = segments
	event.Streaming = false
	event.Activity = ""
	event.ThoughtExpanded = false
	event.ToolsExpanded = false
	event.HideAgentPanels = true
	return event
}

var _ card.Renderer = (*Run)(nil)
