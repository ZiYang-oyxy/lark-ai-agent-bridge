package bridge

import (
	"testing"

	"lark-agent-bridge/internal/agent"
)

func TestParseCommandUsesFullAgentNames(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/codex inspect repo"}, agent.Claude, agent.ApprovalDefault)
	if cmd.Type != CommandRun {
		t.Fatalf("type = %s, want run", cmd.Type)
	}
	if cmd.Agent != agent.Codex {
		t.Fatalf("agent = %s, want codex", cmd.Agent)
	}
	if cmd.Text != "inspect repo" {
		t.Fatalf("text = %q", cmd.Text)
	}
}

func TestParseCommandDefaultsSingleChatToRun(t *testing.T) {
	cmd := ParseCommand(Message{Text: "hello"}, agent.Claude, agent.ApprovalDefault)
	if cmd.Type != CommandRun || cmd.Agent != agent.Claude {
		t.Fatalf("cmd = %#v, want claude run", cmd)
	}
}

func TestParseCommandIgnoresGroupWithoutMention(t *testing.T) {
	cmd := ParseCommand(Message{Text: "hello", IsGroup: true, Mentioned: false}, agent.Claude, agent.ApprovalDefault)
	if cmd.Type != CommandIgnored {
		t.Fatalf("type = %s, want ignored", cmd.Type)
	}
}

func TestParseCommandRunOptions(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/claude --workdir /tmp/project --approval full inspect"}, agent.Claude, agent.ApprovalDefault)
	if cmd.WorkDir != "/tmp/project" {
		t.Fatalf("workdir = %q", cmd.WorkDir)
	}
	if cmd.ApprovalMode != agent.ApprovalFull {
		t.Fatalf("approval = %q, want full", cmd.ApprovalMode)
	}
	if cmd.Text != "inspect" {
		t.Fatalf("text = %q, want inspect", cmd.Text)
	}
}

func TestParseAgentScopedManagementCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/status codex"}, agent.Claude, agent.ApprovalDefault)
	if cmd.Type != CommandStatus {
		t.Fatalf("type = %s, want status", cmd.Type)
	}
	if cmd.Agent != agent.Codex {
		t.Fatalf("agent = %s, want codex", cmd.Agent)
	}
}

func TestParseTopicCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/topic off"}, agent.Claude, agent.ApprovalDefault)
	if cmd.Type != CommandTopic {
		t.Fatalf("type = %s, want topic", cmd.Type)
	}
	if cmd.Text != "off" {
		t.Fatalf("text = %q, want off", cmd.Text)
	}
}

func TestParseResumeCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/resume codex --approval full --workdir /tmp/project --last"}, agent.Claude, agent.ApprovalDefault)
	if cmd.Type != CommandResume {
		t.Fatalf("type = %s, want resume", cmd.Type)
	}
	if cmd.Agent != agent.Codex {
		t.Fatalf("agent = %s, want codex", cmd.Agent)
	}
	if cmd.ApprovalMode != agent.ApprovalFull {
		t.Fatalf("approval = %s, want full", cmd.ApprovalMode)
	}
	if cmd.WorkDir != "/tmp/project" {
		t.Fatalf("workdir = %q, want /tmp/project", cmd.WorkDir)
	}
	if !cmd.ResumeLast {
		t.Fatal("resume last = false, want true")
	}
}

func TestParseResumeCommandWithTarget(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/resume claude 34ccac3d"}, agent.Codex, agent.ApprovalDefault)
	if cmd.Type != CommandResume {
		t.Fatalf("type = %s, want resume", cmd.Type)
	}
	if cmd.Agent != agent.Claude {
		t.Fatalf("agent = %s, want claude", cmd.Agent)
	}
	if cmd.ResumeTarget != "34ccac3d" {
		t.Fatalf("target = %q, want 34ccac3d", cmd.ResumeTarget)
	}
}

func TestTargetWorkDirUsesRunOption(t *testing.T) {
	got := TargetWorkDir(Message{Text: "/codex --workdir /tmp/project inspect"}, "claude", "/tmp/default")
	if got != "/tmp/project" {
		t.Fatalf("workdir = %q, want /tmp/project", got)
	}
}
