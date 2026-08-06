package reply

import (
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/session"
)

type markdownCardRenderer struct {
	next         feishu.ResumableRenderer
	maxRunes     int
	continuation interface {
		RenderPages([]card.Event) error
		RenderRef() session.RenderRef
	}
}

func NewMarkdownCardRenderer(next feishu.ResumableRenderer) feishu.ResumableRenderer {
	return NewMarkdownCardRendererWithLimit(next, defaultCardCandidateMaxRunes)
}

func NewMarkdownCardRendererWithLimit(next feishu.ResumableRenderer, maxRunes int) feishu.ResumableRenderer {
	return &markdownCardRenderer{next: next, maxRunes: maxRunes}
}

func NewMarkdownContinuationRenderer(next interface {
	RenderPages([]card.Event) error
	RenderRef() session.RenderRef
}) feishu.ResumableRenderer {
	return NewMarkdownContinuationRendererWithLimit(next, defaultCardCandidateMaxRunes)
}

func NewMarkdownContinuationRendererWithLimit(next interface {
	RenderPages([]card.Event) error
	RenderRef() session.RenderRef
}, maxRunes int) feishu.ResumableRenderer {
	return &markdownCardRenderer{continuation: next, maxRunes: maxRunes}
}

func (r *markdownCardRenderer) Render(event card.Event) error {
	if r.continuation != nil {
		pages := RenderInlineTimelinePagesWithLimit(event, r.maxRunes)
		events := make([]card.Event, len(pages))
		for i, markdown := range pages {
			page := event
			page.Markdown = markdown
			page.MarkdownLayout = true
			page.InlineTimelineLayout = false
			page.OrderedLayout = false
			events[i] = page
		}
		return r.continuation.RenderPages(events)
	}
	event.Markdown = RenderInlineTimelineWithLimit(event, r.maxRunes)
	event.MarkdownLayout = true
	event.InlineTimelineLayout = false
	event.OrderedLayout = false
	return r.next.Render(event)
}

func (r *markdownCardRenderer) RenderRef() session.RenderRef {
	if r.continuation != nil {
		return r.continuation.RenderRef()
	}
	return r.next.RenderRef()
}
