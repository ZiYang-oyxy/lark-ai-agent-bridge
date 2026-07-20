package bridge

import (
	"fmt"
	"strings"

	"lark-agent-bridge/internal/agent"
)

type CommandType string

const (
	CommandHelp    CommandType = "help"
	CommandRun     CommandType = "run"
	CommandStatus  CommandType = "status"
	CommandConfig  CommandType = "config"
	CommandInvite  CommandType = "invite"
	CommandRemove  CommandType = "remove"
	CommandUnknown CommandType = "unknown"
	CommandIgnored CommandType = "ignored"
)

type Command struct {
	Type     CommandType
	Agent    agent.Kind
	Text     string
	WorkDir  string
	Reset    bool
	Explicit bool
	Raw      string
}

func ParseCommand(msg Message, defaultAgent agent.Kind) Command {
	raw := strings.TrimSpace(msg.Text)
	if raw == "" && len(msg.Attachments) == 0 {
		return Command{Type: CommandIgnored, Raw: raw}
	}
	if !strings.HasPrefix(raw, "/") {
		return Command{Type: CommandRun, Agent: defaultAgent, Text: raw, Raw: raw}
	}
	name, rest := splitCommand(raw)
	switch name {
	case "help":
		return Command{Type: CommandHelp, Raw: raw}
	case "status":
		return Command{Type: CommandStatus, Agent: defaultAgent, Raw: raw}
	case "config":
		return Command{Type: CommandConfig, Text: strings.TrimSpace(rest), Raw: raw}
	case "invite":
		return Command{Type: CommandInvite, Text: strings.TrimSpace(rest), Raw: raw}
	case "remove":
		return Command{Type: CommandRemove, Text: strings.TrimSpace(rest), Raw: raw}
	case "new":
		cmd := Command{Type: CommandRun, Agent: defaultAgent, Text: strings.TrimSpace(rest), Reset: true, Explicit: true, Raw: raw}
		parseRunOptions(&cmd)
		return cmd
	case "resume":
		return Command{Type: CommandUnknown, Raw: raw, Text: "/resume 暂未实现。请使用 /new 开始新会话。"}
	default:
		return Command{Type: CommandUnknown, Raw: raw, Text: fmt.Sprintf("unknown command /%s", name)}
	}
}

func TargetWorkDir(msg Message, defaultAgent, defaultWorkDir string) string {
	kind, ok := agent.ParseKind(defaultAgent)
	if !ok {
		kind = agent.Claude
	}
	cmd := ParseCommand(msg, kind)
	if cmd.Type == CommandRun && cmd.WorkDir != "" {
		return cmd.WorkDir
	}
	return defaultWorkDir
}

func parseRunOptions(cmd *Command) {
	fields := strings.Fields(cmd.Text)
	if len(fields) == 0 {
		return
	}
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "--workdir", "--cwd":
			if i+1 < len(fields) {
				cmd.WorkDir = fields[i+1]
				i++
				continue
			}
		}
		out = append(out, fields[i])
	}
	cmd.Text = strings.Join(out, " ")
}

func splitCommand(raw string) (string, string) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", ""
	}
	fields := strings.Fields(raw)
	name := strings.ToLower(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(raw, fields[0]))
	return name, rest
}

func HelpText() string {
	return strings.Join([]string{
		"Feishu AI Agent Bridge commands:",
		"/new [--workdir <path>] [prompt] - start a new configured agent session in this chat/topic",
		"/status - show the current chat/topic session status",
		"/config - configure agent, home, executable, Claude model/effort, reply mode, and chat/topic mode",
		"/invite user|admin @user, /invite group, /invite all group - grant access",
		"/remove user|admin @user, /remove group - revoke access",
		"/help - show this help",
		"Group intake: mention only (default), participated topics, or all group messages; bot senders are ignored by default.",
		"",
		"Plain text continues the current configured agent conversation. Use /new to start a new session.",
	}, "\n")
}
