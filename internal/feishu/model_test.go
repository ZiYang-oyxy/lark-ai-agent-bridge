package feishu

import (
	"context"
	"testing"
	"time"
)

func TestBuildEventCopiesMentionsAndInfersKind(t *testing.T) {
	msg := InboundMessage{ChatID: "chat", MessageID: "m1", MentionsBot: true, Mentions: []Mention{{Key: "@bot", OpenID: "bot"}}}
	event := BuildEvent(msg)
	if event.Kind != EventKindMessageMention {
		t.Fatalf("kind = %s, want mention", event.Kind)
	}
	msg.Mentions[0].OpenID = "changed"
	if event.Mentions[0].OpenID != "bot" {
		t.Fatalf("mentions were not copied: %#v", event.Mentions)
	}
}

func TestMessageDeduper(t *testing.T) {
	d := NewMessageDeduper(time.Minute)
	now := time.Now()
	seen, err := d.SeenBefore(context.Background(), "m1", now)
	if err != nil || seen {
		t.Fatalf("first seen=%v err=%v, want false nil", seen, err)
	}
	seen, err = d.SeenBefore(context.Background(), "m1", now.Add(time.Second))
	if err != nil || !seen {
		t.Fatalf("second seen=%v err=%v, want true nil", seen, err)
	}
	seen, err = d.SeenBefore(context.Background(), "m1", now.Add(2*time.Minute))
	if err != nil || seen {
		t.Fatalf("expired seen=%v err=%v, want false nil", seen, err)
	}
}

func TestIsOldMessage(t *testing.T) {
	startedAt := time.Unix(10, 0).UTC()
	for _, test := range []struct {
		name      string
		createdAt time.Time
		want      bool
	}{
		{name: "more than two seconds old", createdAt: startedAt.Add(-3 * time.Second), want: true},
		{name: "exactly two seconds old", createdAt: startedAt.Add(-2 * time.Second), want: false},
		{name: "zero timestamp", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsOldMessage(test.createdAt, startedAt); got != test.want {
				t.Fatalf("IsOldMessage(%s, %s) = %t, want %t", test.createdAt, startedAt, got, test.want)
			}
		})
	}
}
