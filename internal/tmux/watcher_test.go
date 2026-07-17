package tmux

import (
	"context"
	"testing"
)

func TestPaneWatcherReturnsOnlyDelta(t *testing.T) {
	runner := NewRecordingRunner()
	key := "tmux capture-pane -p -t lark-agent-bridge:agent-claude-chat -S -300"
	runner.Responses[key] = [][]byte{
		[]byte("hello"),
		[]byte("hello world"),
	}
	watcher := PaneWatcher{
		Manager:    NewManager("lark-agent-bridge", runner),
		WindowName: "agent-claude-chat",
	}
	first, err := watcher.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != "hello" {
		t.Fatalf("first delta = %q, want hello", first)
	}
	second, err := watcher.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second != " world" {
		t.Fatalf("second delta = %q, want world delta", second)
	}
}
