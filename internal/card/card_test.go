package card

import (
	"strings"
	"testing"
)

func TestSplitLongText(t *testing.T) {
	pages := SplitLongText("abcdef", 2)
	if len(pages) != 3 {
		t.Fatalf("pages len = %d, want 3", len(pages))
	}
	if pages[0] != "ab" || pages[2] != "ef" {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestSplitLongTextHandlesRunes(t *testing.T) {
	pages := SplitLongText("你好世界", 2)
	if len(pages) != 2 {
		t.Fatalf("pages len = %d, want 2", len(pages))
	}
	if pages[0] != "你好" || pages[1] != "世界" {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestSplitSegmentsPaginatesByRuneBudget(t *testing.T) {
	pages := SplitSegments([]Segment{
		{Kind: SegmentText, Text: "hello"},
		{Kind: SegmentTool, Text: "abcdef"},
	}, 5)
	if len(pages) != 3 {
		t.Fatalf("pages len = %d, want 3: %#v", len(pages), pages)
	}
	if pages[0][0] != (Segment{Kind: SegmentText, Text: "hello"}) {
		t.Fatalf("page 1 = %#v", pages[0])
	}
	if pages[1][0] != (Segment{Kind: SegmentTool, Text: "abcde"}) {
		t.Fatalf("page 2 = %#v", pages[1])
	}
	if pages[2][0] != (Segment{Kind: SegmentTool, Text: "f"}) {
		t.Fatalf("page 3 = %#v", pages[2])
	}
}

func TestLimitEventTruncatesTextFields(t *testing.T) {
	event := LimitEvent(Event{
		Segments: []Segment{{Kind: SegmentText, Text: "abcdefghijklmnopqrstuvwxyz"}},
		Message:  "0123456789abcdefghijklmnopqrstuvwxyz",
	}, 20)
	if got := len([]rune(event.Segments[0].Text)); got > 20 {
		t.Fatalf("segment length = %d, want <= 20", got)
	}
	if got := len([]rune(event.Message)); got > 20 {
		t.Fatalf("message length = %d, want <= 20", got)
	}
	if !strings.Contains(event.Segments[0].Text, "[truncated]") {
		t.Fatalf("segment = %q, want truncation marker", event.Segments[0].Text)
	}
	if !strings.Contains(event.Message, "[truncated]") {
		t.Fatalf("message = %q, want truncation marker", event.Message)
	}
}

func TestLimitEventRedactsSecrets(t *testing.T) {
	event := LimitEvent(Event{
		Segments: []Segment{{Kind: SegmentText, Text: "token=secret-value"}},
		Message:  "password:123",
	}, 100)
	if strings.Contains(event.Segments[0].Text, "secret-value") {
		t.Fatalf("secret leaked in segment: %q", event.Segments[0].Text)
	}
	if strings.Contains(event.Message, "123") {
		t.Fatalf("secret leaked in message: %q", event.Message)
	}
}

func TestWorkDirCreateActions(t *testing.T) {
	actions := WorkDirCreateActions("/tmp/a")
	if len(actions) != 2 {
		t.Fatalf("actions len = %d, want 2", len(actions))
	}
	if actions[0].Value != "/tmp/a" {
		t.Fatalf("value = %q, want path", actions[0].Value)
	}
	if actions[0].Disabled {
		t.Fatalf("default action disabled = true, want false")
	}
	disabled := WorkDirActions("/tmp/a", true)
	if !disabled[0].Disabled || !disabled[1].Disabled {
		t.Fatalf("disabled actions = %#v, want both disabled", disabled)
	}
}
