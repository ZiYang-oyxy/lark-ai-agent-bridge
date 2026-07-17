package bridge

import (
	"testing"

	"lark-agent-bridge/internal/agent"
)

func TestParseNewCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/new --workdir /tmp/project inspect repo"}, agent.Claude)
	if cmd.Type != CommandRun {
		t.Fatalf("type = %s, want run", cmd.Type)
	}
	if cmd.Agent != agent.Claude {
		t.Fatalf("agent = %s, want claude", cmd.Agent)
	}
	if cmd.WorkDir != "/tmp/project" {
		t.Fatalf("workdir = %q", cmd.WorkDir)
	}
	if cmd.Text != "inspect repo" {
		t.Fatalf("text = %q", cmd.Text)
	}
	if !cmd.Reset || !cmd.Explicit {
		t.Fatalf("new command should reset explicit session: %#v", cmd)
	}
}

func TestParseNewAllowsEmptyPrompt(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/new --workdir /tmp/project"}, agent.Claude)
	if cmd.Type != CommandRun || cmd.Text != "" || !cmd.Reset {
		t.Fatalf("cmd = %#v, want empty reset run", cmd)
	}
}

func TestParsePlainTextOutsideTopicContinuesScope(t *testing.T) {
	cmd := ParseCommand(Message{Text: "hello"}, agent.Claude)
	if cmd.Type != CommandRun || cmd.Agent != agent.Claude || cmd.Reset {
		t.Fatalf("cmd = %#v, want non-reset claude run", cmd)
	}
}

func TestParsePlainTextInTopicContinuesTopicSession(t *testing.T) {
	cmd := ParseCommand(Message{Text: "hello", ThreadID: "topic-a"}, agent.Claude)
	if cmd.Type != CommandRun || cmd.Reset {
		t.Fatalf("cmd = %#v, want non-reset run", cmd)
	}
}

func TestParseCommandIgnoresGroupWithoutMention(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/new hello", IsGroup: true, Mentioned: false}, agent.Claude)
	if cmd.Type != CommandIgnored {
		t.Fatalf("type = %s, want ignored", cmd.Type)
	}
}

func TestRemovedCommandsAreUnknown(t *testing.T) {
	for _, text := range []string{"/codex inspect repo", "/claude inspect repo", "/resume", "/sessions", "/history", "/topic off", "/attach"} {
		cmd := ParseCommand(Message{Text: text}, agent.Claude)
		if cmd.Type != CommandUnknown {
			t.Fatalf("%s type = %s, want unknown", text, cmd.Type)
		}
	}
}

func TestTargetWorkDirUsesRunOption(t *testing.T) {
	got := TargetWorkDir(Message{Text: "/new --workdir /tmp/project inspect"}, "claude", "/tmp/default")
	if got != "/tmp/project" {
		t.Fatalf("workdir = %q, want /tmp/project", got)
	}
}
