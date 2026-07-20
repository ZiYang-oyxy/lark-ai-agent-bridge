package bridge

import (
	"testing"
	"time"

	"lark-agent-bridge/internal/media"
)

func TestDebounceForMessage(t *testing.T) {
	if got := DebounceFor(Message{MessageType: "text"}); got != 250*time.Millisecond {
		t.Fatalf("dm = %s", got)
	}
	if got := DebounceFor(Message{MessageType: "text", IsGroup: true}); got != 600*time.Millisecond {
		t.Fatalf("group = %s", got)
	}
	if got := DebounceFor(Message{MessageType: "interactive"}); got != time.Second {
		t.Fatalf("interactive = %s", got)
	}
	if got := DebounceFor(Message{MessageType: "image"}); got != time.Second {
		t.Fatalf("image = %s", got)
	}
	if got := DebounceFor(Message{MessageType: "text", HasAttachments: true}); got != time.Second {
		t.Fatalf("attachment = %s", got)
	}
	if got := DebounceFor(Message{MessageType: "text", Attachments: []media.Ref{{FileKey: "image", Kind: "image"}}}); got != time.Second {
		t.Fatalf("attachment refs = %s", got)
	}
	if got := DebounceFor(Message{MessageType: "unknown"}); got != time.Second {
		t.Fatalf("unknown = %s", got)
	}
}
