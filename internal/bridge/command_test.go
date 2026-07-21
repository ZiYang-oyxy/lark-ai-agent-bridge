package bridge

import (
	"regexp"
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

func TestParseCommandOnlyParsesSyntaxAfterIntake(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/new hello", IsGroup: true, Mentioned: false}, agent.Claude)
	if cmd.Type != CommandRun {
		t.Fatalf("type = %s, want run", cmd.Type)
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

func TestParseAgentModeCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/agent-mode"}, agent.Claude)
	if cmd.Type != CommandAgentMode || cmd.Text != "" {
		t.Fatalf("agent mode = %#v", cmd)
	}
	cmd = ParseCommand(Message{Text: "/agent-mode codex"}, agent.Claude)
	if cmd.Type != CommandAgentMode || cmd.Text != "codex" {
		t.Fatalf("agent mode codex = %#v", cmd)
	}
}

func TestHelpTextIncludesConfigCommand(t *testing.T) {
	if text := HelpText(); !strings.Contains(text, "/config") {
		t.Fatalf("help text = %q, want /config", text)
	}
}

func TestParseScheduleCommands(t *testing.T) {
	tests := []struct {
		text     string
		typeWant CommandType
		body     string
	}{
		{text: "/cron", typeWant: CommandCron, body: ""},
		{text: "/cron add 每天九点总结", typeWant: CommandCron, body: "add 每天九点总结"},
		{text: "/timer del abcd1234", typeWant: CommandTimer, body: "del abcd1234"},
	}
	for _, test := range tests {
		cmd := ParseCommand(Message{Text: test.text}, agent.Claude)
		if cmd.Type != test.typeWant || cmd.Text != test.body {
			t.Fatalf("ParseCommand(%q) = %#v", test.text, cmd)
		}
	}
}

func TestHelpTextIncludesScheduleCommands(t *testing.T) {
	text := HelpText()
	if !strings.Contains(text, "/cron") || !strings.Contains(text, "/timer") {
		t.Fatalf("help text = %q", text)
	}
}

func TestRemovedCommandsAreUnknown(t *testing.T) {
	for _, text := range []string{"/codex inspect repo", "/claude inspect repo", "/sessions", "/history", "/topic off", "/attach"} {
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

func TestParseStopCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/stop"}, agent.Claude)
	if cmd.Type != CommandStop || cmd.Agent != agent.Claude || cmd.Text != "" {
		t.Fatalf("cmd = %#v, want exact claude stop", cmd)
	}

	withArgs := ParseCommand(Message{Text: "/stop all"}, agent.Codex)
	if withArgs.Type != CommandStop || withArgs.Agent != agent.Codex || withArgs.Text != "all" {
		t.Fatalf("stop with args = %#v, want retained args", withArgs)
	}
}

func TestHelpTextIncludesStopQueueSemantics(t *testing.T) {
	text := HelpText()
	if !strings.Contains(text, "/stop") || !strings.Contains(text, "queued") {
		t.Fatalf("help text = %q, want /stop and queued semantics", text)
	}
}

func TestParseHelpCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/help"}, agent.Claude)
	if cmd.Type != CommandHelp {
		t.Fatalf("cmd = %#v, want help", cmd)
	}
}

func TestParseResumeCommand(t *testing.T) {
	for _, tc := range []struct {
		text   string
		target string
	}{
		{text: "/resume"},
		{text: "/resume abc-123", target: "abc-123"},
	} {
		cmd := ParseCommand(Message{Text: tc.text}, agent.Claude)
		if cmd.Type != CommandResume || cmd.Agent != agent.Claude || cmd.Text != tc.target {
			t.Fatalf("ParseCommand(%q) = %#v", tc.text, cmd)
		}
	}
}

func TestParseResumeRejectsMultipleArguments(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/resume one two"}, agent.Claude)
	if cmd.Type != CommandUnknown || cmd.Text != "用法：/resume 或 /resume <session-id>" {
		t.Fatalf("resume arguments = %#v", cmd)
	}
}

func TestHelpTextIncludesResumeForms(t *testing.T) {
	text := HelpText()
	if !strings.Contains(text, "/resume") || !strings.Contains(text, "/resume <session-id>") {
		t.Fatalf("help text = %q", text)
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

// TestHelpCardDataMatchesHelpText is a two-source drift gate: every slash
// command that appears in the sectioned /help card (HelpCardData, Chinese) must
// also appear in the legacy plain-text help (HelpText, English). It compares
// only the slash-command tokens, not the surrounding descriptions, so the two
// surfaces can keep their own wording while staying in sync on command coverage.
func TestHelpCardDataMatchesHelpText(t *testing.T) {
	helpText := HelpText()
	tokenRE := regexp.MustCompile(`/[a-z][a-z-]*`)
	seen := map[string]bool{}
	for _, group := range HelpCardData().Groups {
		for _, line := range group.Lines {
			for _, token := range tokenRE.FindAllString(line, -1) {
				if seen[token] {
					continue
				}
				seen[token] = true
				if !strings.Contains(helpText, token) {
					t.Fatalf("command %q is in HelpCardData but missing from HelpText; keep the two in sync", token)
				}
			}
		}
	}
	// Sanity: the card should surface the core commands, so the gate is not
	// vacuously passing on an empty token set.
	for _, want := range []string{"/new", "/status", "/stop", "/resume", "/agent-mode", "/config", "/local-config", "/cron", "/timer", "/invite", "/remove"} {
		if !seen[want] {
			t.Fatalf("HelpCardData missing expected command %q", want)
		}
	}
}
