package bridge

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/media"
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

func TestParseAttachmentOnlyMessageRunsWithEmptyText(t *testing.T) {
	cmd := ParseCommand(Message{Attachments: []media.Ref{{MessageID: "m1", FileKey: "image", Kind: "image"}}}, agent.Claude)
	if cmd.Type != CommandRun || cmd.Text != "" || cmd.Agent != agent.Claude {
		t.Fatalf("cmd = %#v, want attachment-only run", cmd)
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

func TestParseConfigCommandInDirectAndMentionedGroupMessages(t *testing.T) {
	for _, msg := range []Message{
		{Text: "/config"},
		{Text: "/config", IsGroup: true, Mentioned: true},
	} {
		cmd := ParseCommand(msg, agent.Claude)
		if cmd.Type != CommandConfig || cmd.Text != "" {
			t.Fatalf("ParseCommand(%#v) = %#v, want config", msg, cmd)
		}
	}
	reset := ParseCommand(Message{Text: "/config reset"}, agent.Claude)
	if reset.Type != CommandConfig || reset.Text != "reset" {
		t.Fatalf("config reset = %#v", reset)
	}
}

func TestHelpTextIncludesConfigCommand(t *testing.T) {
	if text := HelpText(); !strings.Contains(text, "/config") {
		t.Fatalf("help text = %q, want /config", text)
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

func TestParseNewCwdAliasMatchesWorkdir(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/new --cwd /tmp/project inspect repo"}, agent.Claude)
	if cmd.Type != CommandRun || cmd.WorkDir != "/tmp/project" || cmd.Text != "inspect repo" {
		t.Fatalf("cmd = %#v, want run with workdir /tmp/project", cmd)
	}
}

func TestParseNewWorkdirMissingValueIsTreatedAsLiteralText(t *testing.T) {
	// A trailing --workdir with no following token must not consume anything;
	// it falls through to the prompt text and leaves WorkDir empty so the
	// default workdir applies rather than silently dropping the flag.
	cmd := ParseCommand(Message{Text: "/new inspect --workdir"}, agent.Claude)
	if cmd.Type != CommandRun || cmd.WorkDir != "" {
		t.Fatalf("cmd = %#v, want run with empty workdir", cmd)
	}
	if cmd.Text != "inspect --workdir" {
		t.Fatalf("text = %q, want literal trailing flag", cmd.Text)
	}
}

func TestParseStatusCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/status"}, agent.Claude)
	if cmd.Type != CommandStatus || cmd.Agent != agent.Claude {
		t.Fatalf("cmd = %#v, want status", cmd)
	}
}

func TestParseHelpCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/help"}, agent.Claude)
	if cmd.Type != CommandHelp {
		t.Fatalf("cmd = %#v, want help", cmd)
	}
}

func TestParseResumeReturnsNotImplementedDegradation(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/resume"}, agent.Claude)
	if cmd.Type != CommandUnknown {
		t.Fatalf("type = %s, want unknown", cmd.Type)
	}
	if !strings.Contains(cmd.Text, "/resume") || !strings.Contains(cmd.Text, "/new") {
		t.Fatalf("resume degradation text = %q, want mention of /resume and /new", cmd.Text)
	}
}

func TestParseUnknownCommandReturnsFormattedMessage(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/bogus"}, agent.Claude)
	if cmd.Type != CommandUnknown {
		t.Fatalf("type = %s, want unknown", cmd.Type)
	}
	if cmd.Text != "unknown command /bogus" {
		t.Fatalf("unknown text = %q, want \"unknown command /bogus\"", cmd.Text)
	}
}
