package reply

import (
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/session"
)

type markdownCardRenderer struct {
	next feishu.ResumableRenderer
}

func NewMarkdownCardRenderer(next feishu.ResumableRenderer) feishu.ResumableRenderer {
	return &markdownCardRenderer{next: next}
}

func (r *markdownCardRenderer) Render(event card.Event) error {
	event.Markdown = RenderInlineTimeline(event)
	event.MarkdownLayout = true
	event.InlineTimelineLayout = false
	event.OrderedLayout = false
	return r.next.Render(event)
}

func (r *markdownCardRenderer) RenderRef() session.RenderRef {
	return r.next.RenderRef()
}
