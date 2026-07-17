package bridge

import (
	"testing"
	"time"

	"lark-agent-bridge/internal/session"
)

func TestDebounceForMessage(t *testing.T) {
	if got := DebounceFor(Message{}); got != 250*time.Millisecond {
		t.Fatalf("dm = %s", got)
	}
	if got := DebounceFor(Message{IsGroup: true}); got != 600*time.Millisecond {
		t.Fatalf("group = %s", got)
	}
	if got := DebounceFor(Message{HasAttachments: true}); got != 600*time.Millisecond {
		t.Fatalf("attachment = %s", got)
	}
}

func TestBuildBatchPromptEmpty(t *testing.T) {
	if got := BuildBatchPrompt(nil); got != "" {
		t.Fatalf("empty prompt = %q", got)
	}
}

func TestBuildBatchPromptSingleTrimsText(t *testing.T) {
	if got := BuildBatchPrompt([]session.Input{{Text: "  hello  "}}); got != "hello" {
		t.Fatalf("single prompt = %q", got)
	}
}

func TestBuildBatchPromptExactFormatAndOrder(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	inputs := []session.Input{
		{Sender: "alice", Text: "  first  ", Time: time.Date(2026, 7, 18, 9, 10, 11, 0, loc)},
		{Sender: "bob", Text: "\nsecond\n", Time: time.Date(2026, 7, 18, 9, 10, 12, 0, loc)},
	}
	want := "以下是按时间顺序合并的一批用户消息。\n\n[消息 1 | 2026-07-18 09:10:11 | alice]\nfirst\n\n[消息 2 | 2026-07-18 09:10:12 | bob]\nsecond"
	if got := BuildBatchPrompt(inputs); got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	if inputs[0].Text != "  first  " || inputs[1].Text != "\nsecond\n" {
		t.Fatal("inputs were modified")
	}
}
