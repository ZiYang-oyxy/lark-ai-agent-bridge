package bridge

import (
	"fmt"
	"strings"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/card"
)

type CommandType string

const (
	CommandHelp        CommandType = "help"
	CommandRun         CommandType = "run"
	CommandStatus      CommandType = "status"
	CommandResume      CommandType = "resume"
	CommandStop        CommandType = "stop"
	CommandConfig      CommandType = "config"
	CommandLocalConfig CommandType = "local-config"
	CommandAgentMode   CommandType = "agent-mode"
	CommandCron        CommandType = "cron"
	CommandTimer       CommandType = "timer"
	CommandInvite      CommandType = "invite"
	CommandRemove      CommandType = "remove"
	CommandUnknown     CommandType = "unknown"
	CommandIgnored     CommandType = "ignored"
)

type Command struct {
	Type         CommandType
	Agent        agent.Kind
	Text         string
	WorkDir      string
	Reset        bool
	Explicit     bool
	Raw          string
	ScheduleKind string
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
	case "stop":
		return Command{Type: CommandStop, Agent: defaultAgent, Text: strings.TrimSpace(rest), Raw: raw}
	case "config":
		return Command{Type: CommandConfig, Text: strings.TrimSpace(rest), Raw: raw}
	case "local-config":
		return Command{Type: CommandLocalConfig, Text: strings.TrimSpace(rest), Raw: raw}
	case "agent-mode":
		return Command{Type: CommandAgentMode, Text: strings.TrimSpace(rest), Raw: raw}
	case "cron":
		return Command{Type: CommandCron, Agent: defaultAgent, Text: strings.TrimSpace(rest), Raw: raw}
	case "timer":
		return Command{Type: CommandTimer, Agent: defaultAgent, Text: strings.TrimSpace(rest), Raw: raw}
	case "invite":
		return Command{Type: CommandInvite, Text: strings.TrimSpace(rest), Raw: raw}
	case "remove":
		return Command{Type: CommandRemove, Text: strings.TrimSpace(rest), Raw: raw}
	case "new":
		cmd := Command{Type: CommandRun, Agent: defaultAgent, Text: strings.TrimSpace(rest), Reset: true, Explicit: true, Raw: raw}
		parseRunOptions(&cmd)
		return cmd
	case "resume":
		target := strings.TrimSpace(rest)
		if len(strings.Fields(target)) > 1 {
			return Command{Type: CommandUnknown, Raw: raw, Text: "用法：/resume 或 /resume <session-id>"}
		}
		return Command{Type: CommandResume, Agent: defaultAgent, Raw: raw, Text: target}
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

// HelpCardData is the sectioned model rendered by the /help CardKit card: four
// titled command groups plus a one-line footer. Commands are wrapped in
// markdown code, arguments are de-emphasised, and descriptions are in Chinese.
func HelpCardData() card.HelpCard {
	return card.HelpCard{
		Groups: []card.HelpGroup{
			{
				Title: "💬 会话",
				Lines: []string{
					"`/new` `[--workdir path] [prompt]` 开新会话",
					"`/status` 当前会话状态 · `/stop` 停止当前任务",
					"`/resume` `[session-id]` 恢复历史会话",
					"`/agent-mode` 切换 claude / codex",
				},
			},
			{
				Title: "⚙️ 配置",
				Lines: []string{
					"`/config` 全局运行偏好",
					"`/local-config` `[reset]` 本群覆盖 / 重置",
				},
			},
			{
				Title: "⏰ 定时",
				Lines: []string{
					"`/cron` 周期任务 · `/timer` 一次性任务",
				},
			},
			{
				Title: "🔒 权限",
				Lines: []string{
					"`/invite` user|admin @人 · group · all group",
					"`/remove` user|admin @人 · group",
				},
			},
		},
		Footer: "直接发文字 = 继续当前会话 · 群里默认需 @bot",
	}
}

func HelpText() string {
	return strings.Join([]string{
		"Feishu AI Agent Bridge commands:",
		"/new [--workdir <path>] [prompt] - start a new configured agent session in this chat/topic",
		"/status - show the current chat/topic session status",
		"/resume - list the 10 most recent sessions for the current agent and workdir",
		"/resume <session-id> - resume that session on the next message",
		"/stop - stop the active task in this chat/topic; queued inputs are preserved",
		"/agent-mode - choose claude or codex for subsequent messages",
		"/cron, /timer - manage recurring and one-shot Agent tasks",
		"/config - configure global defaults",
		"/local-config [reset] - override or reset this group's inherited defaults",
		"/invite user|admin @user, /invite group, /invite all group - grant access",
		"/remove user|admin @user, /remove group - revoke access",
		"/help - show this help",
		"Group intake: mention only (default), participated topics, or all group messages; bot senders are ignored by default.",
		"",
		"Plain text continues the current configured agent conversation. Use /new to start a new session.",
	}, "\n")
}
