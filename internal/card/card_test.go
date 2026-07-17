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

func TestAuthorizationActions(t *testing.T) {
	actions := AuthorizationActions()
	if len(actions) != 4 {
		t.Fatalf("actions len = %d, want 4", len(actions))
	}
	if actions[3].Value != "reject" {
		t.Fatalf("last action = %#v, want reject", actions[3])
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
}

func TestResumeActionsAddsCancel(t *testing.T) {
	actions := ResumeActions([]string{"s1"})
	if len(actions) != 2 {
		t.Fatalf("actions len = %d, want 2", len(actions))
	}
	if actions[0].Value != "1" {
		t.Fatalf("first action value = %q, want 1", actions[0].Value)
	}
	if actions[1].ID != "resume_cancel" {
		t.Fatalf("last action = %#v, want cancel", actions[1])
	}
}

func TestChoiceActionsUseNumericValues(t *testing.T) {
	actions := ChoiceActions([]string{"repo top3", "AI only"})
	if len(actions) != 2 {
		t.Fatalf("actions len = %d, want 2", len(actions))
	}
	if actions[0].Label != "repo top3" || actions[0].Value != "1" {
		t.Fatalf("first action = %#v, want label repo top3 and value 1", actions[0])
	}
	if actions[1].Label != "AI only" || actions[1].Value != "2" {
		t.Fatalf("second action = %#v, want label AI only and value 2", actions[1])
	}
}

func TestRestartActions(t *testing.T) {
	actions := RestartActions()
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if actions[0].ID != "restart_session" {
		t.Fatalf("action = %#v, want restart_session", actions[0])
	}
}

func TestTerminateSessionActions(t *testing.T) {
	actions := TerminateSessionActions(true)
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if actions[0].ID != "terminate_session" || !actions[0].Disabled {
		t.Fatalf("action = %#v, want disabled terminate_session", actions[0])
	}
}
