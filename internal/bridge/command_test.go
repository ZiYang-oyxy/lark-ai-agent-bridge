package bridge

import (
	"reflect"
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

func TestParseTodoCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/.go 给 /help 卡片加个版本号"}, agent.Claude)
	if cmd.Type != CommandTodo {
		t.Fatalf("type = %s, want todo", cmd.Type)
	}
	if cmd.Text != "给 /help 卡片加个版本号" {
		t.Fatalf("text = %q", cmd.Text)
	}
}

func TestParseTodoCommandEmpty(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/.go"}, agent.Claude)
	if cmd.Type != CommandTodo {
		t.Fatalf("type = %s, want todo", cmd.Type)
	}
	if cmd.Text != "" {
		t.Fatalf("text = %q, want empty", cmd.Text)
	}
}

func TestTodoCommandHiddenFromHelp(t *testing.T) {
	if strings.Contains(HelpText(), ".go") {
		t.Fatalf("/.go must stay hidden from /help")
	}
	for _, g := range HelpCardData().Groups {
		for _, line := range g.Lines {
			if strings.Contains(line, ".go") {
				t.Fatalf("/.go must stay hidden from help card: %q", line)
			}
		}
	}
}

func TestTodoCommandIsAdminOnly(t *testing.T) {
	s := &Service{}
	if !s.adminCommand(CommandTodo) {
		t.Fatalf("/.go must be admin-only")
	}
}

func TestParseUpgradeCommandAndAdminGate(t *testing.T) {
	for _, tc := range []struct {
		text string
		arg  string
	}{
		{text: "/upgrade"},
		{text: "/upgrade stable", arg: "stable"},
		{text: "/upgrade rc", arg: "rc"},
	} {
		cmd := ParseCommand(Message{Text: tc.text}, agent.Claude)
		if cmd.Type != CommandUpgrade || cmd.Text != tc.arg {
			t.Fatalf("ParseCommand(%q) = %#v", tc.text, cmd)
		}
	}
	if !(&Service{}).adminCommand(CommandUpgrade) {
		t.Fatal("/upgrade must be admin-only")
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

func TestParseConfigSetCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/config set reply_mode=latest-card"}, agent.Claude)
	if cmd.Type != CommandConfig || cmd.Text != "set reply_mode=latest-card" {
		t.Fatalf("config set = %#v", cmd)
	}
}

func TestParseMkdirCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/mkdir projects/demo"}, agent.Claude)
	if cmd.Type != CommandMkdir || cmd.Text != "projects/demo" {
		t.Fatalf("mkdir = %#v", cmd)
	}
	if !(&Service{}).adminCommand(CommandMkdir) {
		t.Fatal("mkdir must be admin-only")
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
	text := HelpText()
	for _, want := range []string{"/config set", "/local-config set", "/mkdir"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help text = %q, want %s", text, want)
		}
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

func TestParseCommandCd(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/cd /work/proj"}, agent.Claude)
	if cmd.Type != CommandCd || cmd.Agent != agent.Claude || cmd.WorkDir != "/work/proj" {
		t.Fatalf("cmd = %#v, want cd with workdir /work/proj", cmd)
	}

	empty := ParseCommand(Message{Text: "/cd"}, agent.Claude)
	if empty.Type != CommandCd || empty.WorkDir != "" {
		t.Fatalf("cmd = %#v, want cd with empty workdir", empty)
	}
}

func TestParseCommandWs(t *testing.T) {
	tests := []struct {
		text     string
		subWant  string
		nameWant string
	}{
		{text: "/ws", subWant: "list"},
		{text: "/ws list", subWant: "list"},
		{text: "/ws save proj", subWant: "save", nameWant: "proj"},
		{text: "/ws use proj", subWant: "use", nameWant: "proj"},
		{text: "/ws remove proj", subWant: "remove", nameWant: "proj"},
	}
	for _, test := range tests {
		cmd := ParseCommand(Message{Text: test.text}, agent.Claude)
		if cmd.Type != CommandWs || cmd.WsSub != test.subWant || cmd.WsName != test.nameWant {
			t.Fatalf("ParseCommand(%q) = %#v, want ws sub=%q name=%q", test.text, cmd, test.subWant, test.nameWant)
		}
	}
}

func TestHelpTextIncludesCdAndWsCommands(t *testing.T) {
	text := HelpText()
	if !strings.Contains(text, "/cd") || !strings.Contains(text, "/ws") {
		t.Fatalf("help text = %q, want /cd and /ws", text)
	}
}

// TestHelpCardStatusAndStopOnSeparateLines locks the requirement that /status
// and /stop each occupy their own line in the /help card (they used to share
// one line). Each command must appear bold and no single line may carry both.
func TestHelpCardStatusAndStopOnSeparateLines(t *testing.T) {
	var statusLines, stopLines int
	for _, group := range HelpCardData().Groups {
		for _, line := range group.Lines {
			hasStatus := strings.Contains(line, "**`/status`**")
			hasStop := strings.Contains(line, "**`/stop`**")
			if hasStatus && hasStop {
				t.Fatalf("/status and /stop must not share a line: %q", line)
			}
			if hasStatus {
				statusLines++
			}
			if hasStop {
				stopLines++
			}
		}
	}
	if statusLines != 1 || stopLines != 1 {
		t.Fatalf("want /status and /stop each on exactly one line, got status=%d stop=%d", statusLines, stopLines)
	}
}

func TestParseDevelCommand(t *testing.T) {
	for _, tt := range []struct {
		text string
		want string
	}{
		{"/.devel", ""},
		{"/.devel 1", "1"},
		{"/.devel 0", "0"},
		{"/.devel on", "on"},
	} {
		cmd := ParseCommand(Message{Text: tt.text}, agent.Claude)
		if cmd.Type != CommandDevel {
			t.Fatalf("%q type = %s, want devel", tt.text, cmd.Type)
		}
		if cmd.Text != tt.want {
			t.Fatalf("%q text = %q, want %q", tt.text, cmd.Text, tt.want)
		}
	}
}

// TestDevelCommandHiddenFromHelp guards the "hidden" contract: /.devel must not
// appear in the /help card or the plain help text.
func TestDevelCommandHiddenFromHelp(t *testing.T) {
	if strings.Contains(HelpText(), ".devel") {
		t.Fatal("/.devel must not appear in HelpText")
	}
	for _, group := range HelpCardData().Groups {
		for _, line := range group.Lines {
			if strings.Contains(line, ".devel") {
				t.Fatalf("/.devel must not appear in help card: %q", line)
			}
		}
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

func TestParseCompactCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/compact"}, agent.Codex)
	if cmd.Type != CommandCompact || cmd.Agent != agent.Codex || cmd.Text != "" {
		t.Fatalf("cmd = %#v, want exact codex compact", cmd)
	}

	withArgs := ParseCommand(Message{Text: "/compact now"}, agent.Claude)
	if withArgs.Type != CommandCompact || withArgs.Agent != agent.Claude || withArgs.Text != "now" {
		t.Fatalf("compact with args = %#v, want retained args", withArgs)
	}
}

func TestHelpIncludesCompactCommand(t *testing.T) {
	if text := HelpText(); !strings.Contains(text, "/compact") {
		t.Fatalf("help text = %q, want /compact", text)
	}
	var found bool
	for _, group := range HelpCardData().Groups {
		for _, line := range group.Lines {
			found = found || strings.Contains(line, "**`/compact`**")
		}
	}
	if !found {
		t.Fatal("help card must expose /compact")
	}
}

func TestParseGroupAccessCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/group-access selected"}, agent.Claude)
	if cmd.Type != CommandGroupAccess || cmd.Text != "selected" {
		t.Fatalf("cmd = %#v", cmd)
	}
	if help := HelpText(); !strings.Contains(help, "/group-access all|selected") || !strings.Contains(help, "member @user") {
		t.Fatalf("help text missing group access commands: %q", help)
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
	var scheduleLines []string
	for _, group := range HelpCardData().Groups {
		if group.Title == "⏰ 定时" {
			scheduleLines = group.Lines
		}
		for _, line := range group.Lines {
			for _, token := range tokenRE.FindAllString(line, -1) {
				if !strings.Contains(line, "**`"+token+"`**") {
					t.Fatalf("help command %q is not bold in line %q", token, line)
				}
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
	if want := scheduleHelpLines(); !reflect.DeepEqual(scheduleLines, want) {
		t.Fatalf("schedule help lines = %#v, want %#v", scheduleLines, want)
	}
	for _, want := range []string{"add <自然语言任务>", "confirm|cancel [draft-id]", "info|run|enable|disable|del <id>", "info|del <id>"} {
		if !strings.Contains(strings.Join(scheduleLines, "\n"), want) {
			t.Fatalf("schedule help is missing %q: %#v", want, scheduleLines)
		}
	}
	// Sanity: the card should surface the core commands, so the gate is not
	// vacuously passing on an empty token set.
	for _, want := range []string{"/new", "/status", "/stop", "/resume", "/agent-mode", "/mkdir", "/config", "/local-config", "/cron", "/timer", "/invite", "/remove"} {
		if !seen[want] {
			t.Fatalf("HelpCardData missing expected command %q", want)
		}
	}
}
