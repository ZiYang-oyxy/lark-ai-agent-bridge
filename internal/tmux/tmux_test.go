package tmux

import (
	"context"
	"reflect"
	"testing"
)

func TestNewWindowCreatesSessionAndWindow(t *testing.T) {
	runner := NewRecordingRunner()
	manager := NewManager("lark-agent-bridge", runner)
	err := manager.NewWindow(context.Background(), WindowSpec{
		Name:    "agent-claude-chat",
		WorkDir: "/tmp/work",
		Command: []string{"claude", "--dangerously-skip-permissions"},
	})
	if err != nil {
		t.Fatalf("NewWindow returned error: %v", err)
	}
	cmds := runner.Snapshot()
	if len(cmds) != 3 {
		t.Fatalf("recorded %d commands, want 3", len(cmds))
	}
	if cmds[0].Args[0] != "has-session" {
		t.Fatalf("first command = %#v, want has-session", cmds[0])
	}
	if cmds[1].Args[0] != "new-session" {
		t.Fatalf("second command = %#v, want new-session", cmds[1])
	}
	if cmds[2].Args[0] != "new-window" {
		t.Fatalf("third command = %#v, want new-window", cmds[2])
	}
}

func TestAttachCommandIncludesWindow(t *testing.T) {
	manager := NewManager("lark-agent-bridge", NewRecordingRunner())
	got := manager.AttachCommand("agent-claude-chat")
	want := "tmux attach -t lark-agent-bridge:agent-claude-chat"
	if got != want {
		t.Fatalf("attach = %q, want %q", got, want)
	}
}

func TestNewManagerUsesSubmitDelayOnlyForRealRunner(t *testing.T) {
	real := NewManager("lark-agent-bridge", nil)
	if real.SendSubmitDelay <= 0 {
		t.Fatalf("real runner submit delay = %s, want positive", real.SendSubmitDelay)
	}
	recording := NewManager("lark-agent-bridge", NewRecordingRunner())
	if recording.SendSubmitDelay != 0 {
		t.Fatalf("recording runner submit delay = %s, want zero", recording.SendSubmitDelay)
	}
}

func TestRecordingSnapshotRedactsSecrets(t *testing.T) {
	runner := NewRecordingRunner()
	manager := NewManager("lark-agent-bridge", runner)
	if err := manager.SendLine(context.Background(), "agent-claude-chat", "token=secret-value password:123"); err != nil {
		t.Fatalf("send line error: %v", err)
	}
	cmds := runner.Snapshot()
	foundRedacted := false
	for _, cmd := range cmds {
		for _, arg := range cmd.Args {
			if arg == "token=secret-value password:123" {
				t.Fatalf("secret leaked in tmux snapshot: %#v", cmds)
			}
			if arg == "token=[REDACTED] password:[REDACTED]" {
				foundRedacted = true
			}
		}
	}
	if !foundRedacted {
		t.Fatalf("redacted argument not found in %#v", cmds)
	}
}

func TestKillPaneDescendantsPreservesAgentProcess(t *testing.T) {
	runner := NewRecordingRunner()
	runner.Responses["tmux display-message -p -t lark-agent-bridge:agent-codex-chat #{pane_pid}"] = [][]byte{[]byte("100\n")}
	runner.Responses["ps -axo pid=,ppid=,command="] = [][]byte{[]byte(`
100 1 fish -c codex
101 100 /opt/homebrew/bin/codex --dangerously-bypass-approvals-and-sandbox
102 101 /bin/zsh -c sleep 300 && echo LAB
103 102 sleep 300
200 1 sleep 999
`)}
	manager := NewManager("lark-agent-bridge", runner)
	if err := manager.KillPaneDescendants(context.Background(), "agent-codex-chat", "codex"); err != nil {
		t.Fatalf("KillPaneDescendants error: %v", err)
	}
	cmds := runner.Snapshot()
	last := cmds[len(cmds)-1]
	if last.Name != "kill" {
		t.Fatalf("last command = %#v, want kill", last)
	}
	want := []string{"-TERM", "102", "103"}
	if !reflect.DeepEqual(last.Args, want) {
		t.Fatalf("kill args = %#v, want %#v", last.Args, want)
	}
}
