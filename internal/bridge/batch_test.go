package bridge

import (
	"testing"
	"time"

	"lark-agent-bridge/internal/media"
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
	if got := DebounceFor(Message{Attachments: []media.Ref{{FileKey: "image", Kind: "image"}}}); got != 600*time.Millisecond {
		t.Fatalf("attachment refs = %s", got)
	}
}
