package bridge

import (
	"testing"
	"time"
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
