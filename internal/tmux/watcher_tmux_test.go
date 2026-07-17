//go:build tmux

package tmux

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPaneWatcherRealTmuxCapturesExternalInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const session = "lark-agent-bridge-watch-smoke"
	const window = "agent-watch-smoke"
	const input = "manual-attach-input"

	mgr := NewManager(session, nil)
	_ = mgr.KillSession(ctx)
	defer mgr.KillSession(context.Background())

	if err := mgr.NewWindow(ctx, WindowSpec{Name: window, Command: []string{"cat"}}); err != nil {
		t.Fatalf("new window: %v", err)
	}

	watcher := &PaneWatcher{Manager: mgr, WindowName: window, LastLines: 20}
	if _, err := watcher.Poll(ctx); err != nil {
		t.Fatalf("initial poll: %v", err)
	}

	if _, err := (ExecRunner{}).Run(ctx, "tmux", "send-keys", "-t", session+":"+window, "--", input, "C-m"); err != nil {
		t.Fatalf("external send-keys: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		delta, err := watcher.Poll(ctx)
		if err != nil {
			t.Fatalf("poll after external input: %v", err)
		}
		if strings.Contains(delta, input) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("watcher did not capture %q, last pane content: %q", input, watcher.Last)
}
