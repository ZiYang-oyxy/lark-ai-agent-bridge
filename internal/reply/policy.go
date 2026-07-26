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

type BoundCardTarget interface {
	NewStreamingBound(context.Context, feishu.RenderBinding, string) (feishu.ResumableRenderer, error)
	RehydrateBound(feishu.RenderBinding, session.RenderRef) feishu.ResumableRenderer
}

type Policy struct {
	target   CardTarget
	store    *Store
	now      func() time.Time
	Resolver session.RenderRefSequenceResolver
}

func NewPolicy(target CardTarget, store *Store) *Policy {
	return &Policy{target: target, store: store, now: func() time.Time { return time.Now().UTC() }}
}

type Run struct {
	mu         sync.Mutex
	ctx        context.Context
	mode       config.ReplyMode
	target     CardTarget
	store      *Store
	scope      string
	sessionID  string
	binding    feishu.RenderBinding
	replyTo    string
	renderer   feishu.ResumableRenderer
	pages      []feishu.ResumableRenderer
	pageCache  []string
	refChanged func(session.RenderRef) error
}

func (p *Policy) Begin(ctx context.Context, mode config.ReplyMode, scope, sessionID, replyTo string) (*Run, error) {
	return p.BeginBound(ctx, mode, scope, feishu.RenderBinding{RunCardSessionID: sessionID}, replyTo)
}

func (p *Policy) BeginBound(ctx context.Context, mode config.ReplyMode, scope string, binding feishu.RenderBinding, replyTo string) (*Run, error) {
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
	sessionID := binding.RunCardSessionID
	run := &Run{ctx: ctx, mode: mode, target: p.target, store: p.store, scope: scope, sessionID: sessionID, binding: binding, replyTo: replyTo}
	if mode == config.ReplyModeLatestCard && p.store != nil {
		if ref := p.store.GetLatest(scope); ref != nil {
			if p.discardLatestRef(*ref) {
				if err := p.store.SetLatest(scope, nil); err != nil {
					return nil, err
				}
			} else {
				if bound, ok := p.target.(BoundCardTarget); ok {
					run.renderer = bound.RehydrateBound(binding, *ref)
				} else {
					run.renderer = p.target.Rehydrate(sessionID, *ref)
				}
			}
		}
	}
	if run.renderer == nil {
		var renderer feishu.ResumableRenderer
		var err error
		if bound, ok := p.target.(BoundCardTarget); ok {
			renderer, err = bound.NewStreamingBound(ctx, binding, replyTo)
		} else {
			renderer, err = p.target.NewStreaming(ctx, sessionID, replyTo)
		}
		if err != nil {
			return nil, err
		}
		run.renderer = renderer
	}
	run.pages = []feishu.ResumableRenderer{run.renderer}
	return run, nil
}

// SetActiveRefChanged persists the newest continuation card as the active
// recovery target. It is called only after a second or later card is created.
func (r *Run) SetActiveRefChanged(fn func(session.RenderRef) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refChanged = fn
}

func (p *Policy) discardLatestRef(ref session.RenderRef) bool {
	if ref.SequenceUnknown || (p.Resolver != nil && p.Resolver.RenderRefSequenceUnknown(ref)) {
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
	cleanTerminal := terminalEvent(event) && (r.mode == config.ReplyModeAppendCleanCard || r.mode == config.ReplyModeLatestCard)
	if cleanTerminal {
		event = cleanTerminalEvent(event)
	}
	if r.mode == config.ReplyModeAppendCleanCard && cleanTerminal {
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
		var renderer feishu.ResumableRenderer
		var newErr error
		if bound, ok := r.target.(BoundCardTarget); ok {
			renderer, newErr = bound.NewStreamingBound(r.ctx, r.binding, r.replyTo)
		} else {
			renderer, newErr = r.target.NewStreaming(r.ctx, r.sessionID, r.replyTo)
		}
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
	if len(r.pages) > 0 {
		return r.pages[len(r.pages)-1].RenderRef()
	}
	if r.renderer == nil {
		return session.RenderRef{}
	}
	return r.renderer.RenderRef()
}

// RenderPages renders a bounded sliding window of append continuation cards.
// Existing cards are reused when the logical window moves; new cards are only
// created while the count grows, so Feishu receives at most nine messages.
func (r *Run) RenderPages(events []card.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode != config.ReplyModeAppend {
		return fmt.Errorf("continuation cards require append mode")
	}
	if len(events) == 0 || len(events) > maxContinuationCards {
		return fmt.Errorf("invalid continuation page count %d", len(events))
	}
	for len(r.pages) < len(events) {
		pageIndex := len(r.pages)
		renderer, err := r.newPageRenderer(pageIndex)
		if err != nil {
			return err
		}
		r.pages = append(r.pages, renderer)
		r.pageCache = append(r.pageCache, "")
	}
	if len(r.pageCache) < len(r.pages) {
		r.pageCache = append(r.pageCache, make([]string, len(r.pages)-len(r.pageCache))...)
	}
	offset := len(r.pages) - len(events)
	for i, renderer := range r.pages {
		var event card.Event
		if i < offset {
			event = events[0]
			event.Markdown = "_较早续卡内容已合并到后续卡片_"
		} else {
			event = events[i-offset]
		}
		event.SessionID = r.pageSessionID(i)
		if i != len(r.pages)-1 {
			event.Streaming = false
			event.Activity = ""
			event.StopButton.Visible = false
		}
		signature := continuationPageSignature(event)
		if signature == r.pageCache[i] {
			continue
		}
		if err := renderer.Render(event); err != nil {
			return err
		}
		r.pageCache[i] = signature
	}
	return nil
}

func (r *Run) newPageRenderer(pageIndex int) (feishu.ResumableRenderer, error) {
	binding := r.binding
	binding.RunCardSessionID = r.pageSessionID(pageIndex)
	var renderer feishu.ResumableRenderer
	var err error
	if bound, ok := r.target.(BoundCardTarget); ok {
		renderer, err = bound.NewStreamingBound(r.ctx, binding, r.replyTo)
	} else {
		renderer, err = r.target.NewStreaming(r.ctx, binding.RunCardSessionID, r.replyTo)
	}
	if err != nil {
		return nil, err
	}
	// RenderRef is empty until the first page frame creates/replies the card;
	// persistence therefore happens after RenderPages has rendered it.
	return &refTrackingRenderer{ResumableRenderer: renderer, onFirstRef: func(ref session.RenderRef) error {
		if pageIndex == 0 || r.refChanged == nil {
			return nil
		}
		return r.refChanged(ref)
	}}, nil
}

func (r *Run) pageSessionID(pageIndex int) string {
	if pageIndex == 0 {
		return r.sessionID
	}
	return fmt.Sprintf("%s:page:%d", r.sessionID, pageIndex+1)
}

func continuationPageSignature(event card.Event) string {
	return fmt.Sprintf("%s\x00%s\x00%t\x00%s\x00%s", event.Type, event.Markdown, event.Streaming, event.HeaderTitle, event.HeaderTemplate)
}

type refTrackingRenderer struct {
	feishu.ResumableRenderer
	onFirstRef func(session.RenderRef) error
	tracked    bool
}

func (r *refTrackingRenderer) Render(event card.Event) error {
	if err := r.ResumableRenderer.Render(event); err != nil {
		return err
	}
	if r.tracked || r.onFirstRef == nil {
		return nil
	}
	ref := r.ResumableRenderer.RenderRef()
	if ref.CardID == "" {
		return nil
	}
	if err := r.onFirstRef(ref); err != nil {
		return err
	}
	r.tracked = true
	return nil
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
	// 三段布局(append-clean-card)在终态保留 thought/tool 的「最新一次」以便渲染
	// 「💭 思考推理（已完成 · ×N）」/「🔧 工具调用（×N）」折叠区(默认折叠、不侵入正文);
	// 旧的合并 panel_process 分支(latest-card 等)沿用「终态只留正文」的老语义。
	preservePanels := event.ThreeSectionLayout
	segments := make([]card.Segment, 0, len(event.Segments))
	for _, segment := range event.Segments {
		switch segment.Kind {
		case card.SegmentText, card.SegmentError:
			segments = append(segments, segment)
		case card.SegmentThought, card.SegmentTool:
			if preservePanels {
				segments = append(segments, segment)
			}
		}
	}
	event.Segments = segments
	event.Streaming = false
	event.Activity = ""
	event.ThoughtExpanded = false
	event.ToolsExpanded = false
	event.ProcessExpanded = false
	if !preservePanels {
		event.HideAgentPanels = true
	}
	return event
}

var _ card.Renderer = (*Run)(nil)
