package bridge

import (
	"fmt"
	"strings"

	"lark-agent-bridge/internal/agent"
)

type CommandType string

const (
	CommandHelp      CommandType = "help"
	CommandRun       CommandType = "run"
	CommandSessions  CommandType = "sessions"
	CommandStatus    CommandType = "status"
	CommandAttach    CommandType = "attach"
	CommandInterrupt CommandType = "interrupt"
	CommandStop      CommandType = "stop"
	CommandHistory   CommandType = "history"
	CommandResume    CommandType = "resume"
	CommandTopic     CommandType = "topic"
	CommandUnknown   CommandType = "unknown"
	CommandIgnored   CommandType = "ignored"
)

type Command struct {
	Type         CommandType
	Agent        agent.Kind
	Text         string
	WorkDir      string
	ApprovalMode agent.ApprovalMode
	ResumeTarget string
	ResumeLast   bool
	Raw          string
}

func ParseCommand(msg Message, defaultAgent agent.Kind, defaultApproval agent.ApprovalMode) Command {
	raw := strings.TrimSpace(msg.Text)
	if !msg.ShouldHandle() {
		return Command{Type: CommandIgnored, Raw: raw}
	}
	if raw == "" {
		return Command{Type: CommandIgnored, Raw: raw}
	}
	if !strings.HasPrefix(raw, "/") {
		return Command{Type: CommandRun, Agent: defaultAgent, Text: raw, ApprovalMode: defaultApproval, Raw: raw}
	}
	name, rest := splitCommand(raw)
	switch name {
	case "help":
		return Command{Type: CommandHelp, Raw: raw}
	case "sessions":
		return Command{Type: CommandSessions, Raw: raw}
	case "status":
		return parseAgentScopedCommand(CommandStatus, rest, defaultAgent, raw)
	case "attach":
		return parseAgentScopedCommand(CommandAttach, rest, defaultAgent, raw)
	case "interrupt":
		return parseAgentScopedCommand(CommandInterrupt, rest, defaultAgent, raw)
	case "stop", "terminate":
		return parseAgentScopedCommand(CommandStop, rest, defaultAgent, raw)
	case "history":
		return parseAgentScopedCommand(CommandHistory, rest, defaultAgent, raw)
	case "resume":
		return parseResumeCommand(rest, defaultAgent, defaultApproval, raw)
	case "topic":
		return Command{Type: CommandTopic, Text: strings.TrimSpace(rest), Raw: raw}
	case string(agent.Claude), string(agent.Codex):
		kind, _ := agent.ParseKind(name)
		cmd := Command{Type: CommandRun, Agent: kind, Text: strings.TrimSpace(rest), ApprovalMode: defaultApproval, Raw: raw}
		parseRunOptions(&cmd)
		return cmd
	default:
		return Command{Type: CommandUnknown, Raw: raw, Text: fmt.Sprintf("unknown command /%s", name)}
	}
}

func TargetWorkDir(msg Message, defaultAgent, defaultWorkDir string) string {
	kind, ok := agent.ParseKind(defaultAgent)
	if !ok {
		kind = agent.Claude
	}
	cmd := ParseCommand(msg, kind, agent.ApprovalDefault)
	if cmd.Type == CommandRun && cmd.WorkDir != "" {
		return cmd.WorkDir
	}
	return defaultWorkDir
}

func parseAgentScopedCommand(commandType CommandType, rest string, defaultAgent agent.Kind, raw string) Command {
	cmd := Command{Type: commandType, Agent: defaultAgent, Raw: raw}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return cmd
	}
	if kind, ok := agent.ParseKind(fields[0]); ok {
		cmd.Agent = kind
		cmd.Text = strings.Join(fields[1:], " ")
		return cmd
	}
	cmd.Text = strings.TrimSpace(rest)
	return cmd
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
		case "--approval":
			if i+1 < len(fields) {
				if mode, ok := agent.ParseApprovalMode(fields[i+1]); ok {
					cmd.ApprovalMode = mode
				}
				i++
				continue
			}
		case "--auto":
			cmd.ApprovalMode = agent.ApprovalAuto
			continue
		case "--full":
			cmd.ApprovalMode = agent.ApprovalFull
			continue
		}
		out = append(out, fields[i])
	}
	cmd.Text = strings.Join(out, " ")
}

func parseResumeCommand(rest string, defaultAgent agent.Kind, defaultApproval agent.ApprovalMode, raw string) Command {
	cmd := Command{Type: CommandResume, Agent: defaultAgent, ApprovalMode: defaultApproval, Raw: raw}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return cmd
	}
	if kind, ok := agent.ParseKind(fields[0]); ok {
		cmd.Agent = kind
		fields = fields[1:]
	}
	target := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "--workdir", "--cwd":
			if i+1 < len(fields) {
				cmd.WorkDir = fields[i+1]
				i++
				continue
			}
		case "--approval":
			if i+1 < len(fields) {
				if mode, ok := agent.ParseApprovalMode(fields[i+1]); ok {
					cmd.ApprovalMode = mode
				}
				i++
				continue
			}
		case "--auto":
			cmd.ApprovalMode = agent.ApprovalAuto
			continue
		case "--full":
			cmd.ApprovalMode = agent.ApprovalFull
			continue
		case "--last", "-c", "--continue", "last", "continue":
			cmd.ResumeLast = true
			continue
		}
		target = append(target, fields[i])
	}
	cmd.ResumeTarget = strings.TrimSpace(strings.Join(target, " "))
	cmd.Text = cmd.ResumeTarget
	return cmd
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
		"Feishu Agent Bridge commands:",
		"/claude <prompt> - send prompt to a Claude session",
		"/codex <prompt> - send prompt to a Codex session",
		"agent options: --workdir <path>, --approval default|auto|full, --auto, --full",
		"/sessions - list active sessions",
		"/status [claude|codex] - show current session status",
		"/attach [claude|codex] - show tmux attach command",
		"/interrupt [claude|codex] - interrupt current query",
		"/stop [claude|codex] - terminate current session",
		"/history [claude|codex] - show prompt history for current session",
		"/resume [claude|codex] [session-id|--last] - launch an agent resume session",
		"/topic on|off|status - control whether topic/thread id is part of the session key",
		"/help - show this help",
	}, "\n")
}
