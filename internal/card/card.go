package card

import (
	"strings"
	"sync"

	"lark-agent-bridge/internal/security"
)

type SegmentKind string

const (
	SegmentText    SegmentKind = "text"
	SegmentThought SegmentKind = "thought"
	SegmentTool    SegmentKind = "tool"
	SegmentError   SegmentKind = "error"
)

type Segment struct {
	Kind SegmentKind
	Text string
}

type Meta struct {
	Agent   string
	Model   string
	Tokens  int
	WorkDir string
	Status  string
}

type StopButton struct {
	Visible  bool
	Disabled bool
}

type Action struct {
	ID       string
	Label    string
	Value    string
	Disabled bool
}

type Event struct {
	Type             string
	SessionID        string
	ReplyToMessageID string
	Segments         []Segment
	Meta             Meta
	StopButton       StopButton
	Actions          []Action
	Message          string
}

func WorkDirCreateActions(path string) []Action {
	return []Action{
		{ID: "create_workdir", Label: "Create directory", Value: path},
		{ID: "cancel_workdir", Label: "Cancel", Value: path},
	}
}

type Renderer interface {
	Render(Event) error
}

type LimitRenderer struct {
	Next     Renderer
	MaxChars int
}

func NewLimitRenderer(next Renderer, maxChars int) Renderer {
	if next == nil || maxChars <= 0 {
		return next
	}
	return &LimitRenderer{Next: next, MaxChars: maxChars}
}

func (r *LimitRenderer) Render(e Event) error {
	if r == nil || r.Next == nil {
		return nil
	}
	return r.Next.Render(LimitEvent(e, r.MaxChars))
}

type FakeRenderer struct {
	mu     sync.Mutex
	events []Event
}

func NewFakeRenderer() *FakeRenderer {
	return &FakeRenderer{}
}

func (r *FakeRenderer) Render(e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *FakeRenderer) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]Event, len(r.events))
	copy(cp, r.events)
	return cp
}

func SplitLongText(text string, maxChars int) []string {
	if maxChars <= 0 || len([]rune(text)) <= maxChars {
		return []string{text}
	}
	var pages []string
	for len([]rune(text)) > maxChars {
		cut := maxChars
		prefix := firstRunes(text, maxChars)
		if i := strings.LastIndex(prefix, "\n"); i > len(prefix)/2 {
			cut = len([]rune(prefix[:i+1]))
		}
		page, rest := splitRunes(text, cut)
		pages = append(pages, page)
		text = rest
	}
	if text != "" {
		pages = append(pages, text)
	}
	return pages
}

func SplitSegments(segments []Segment, maxChars int) [][]Segment {
	if maxChars <= 0 {
		return [][]Segment{segments}
	}
	var pages [][]Segment
	var current []Segment
	currentLen := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		pages = append(pages, current)
		current = nil
		currentLen = 0
	}
	for _, segment := range segments {
		if segment.Text == "" {
			continue
		}
		for _, part := range SplitLongText(segment.Text, maxChars) {
			partLen := len([]rune(part))
			if currentLen > 0 && currentLen+partLen > maxChars {
				flush()
			}
			current = append(current, Segment{Kind: segment.Kind, Text: part})
			currentLen += partLen
		}
	}
	flush()
	if len(pages) == 0 {
		return [][]Segment{segments}
	}
	return pages
}

func LimitEvent(e Event, maxChars int) Event {
	for i := range e.Segments {
		e.Segments[i].Text = limitText(e.Segments[i].Text, maxChars)
	}
	e.Message = limitText(e.Message, maxChars)
	return e
}

func limitText(text string, maxChars int) string {
	text = security.Redact(text)
	if maxChars <= 0 || len([]rune(text)) <= maxChars {
		return text
	}
	const suffix = "\n\n[truncated]"
	runes := []rune(text)
	suffixRunes := []rune(suffix)
	if maxChars <= len(suffixRunes) {
		return string(runes[:maxChars])
	}
	return string(runes[:maxChars-len(suffixRunes)]) + suffix
}

func firstRunes(text string, n int) string {
	page, _ := splitRunes(text, n)
	return page
}

func splitRunes(text string, n int) (string, string) {
	runes := []rune(text)
	if n >= len(runes) {
		return text, ""
	}
	return string(runes[:n]), string(runes[n:])
}
