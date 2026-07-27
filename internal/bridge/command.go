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
	CommandUpgrade     CommandType = "upgrade"
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
	CommandGroupAccess CommandType = "group-access"
	CommandCd          CommandType = "cd"
	CommandWs          CommandType = "ws"
	CommandDevel       CommandType = "devel"
	CommandTodo        CommandType = "todo"
	CommandAction      CommandType = "action"
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
	WsSub        string
	WsName       string
	ActionID     string
	ActionValue  string
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
	case "upgrade":
		return Command{Type: CommandUpgrade, Text: strings.TrimSpace(rest), Raw: raw}
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
	case "group-access":
		return Command{Type: CommandGroupAccess, Text: strings.TrimSpace(rest), Raw: raw}
	case "cd":
		return Command{Type: CommandCd, Agent: defaultAgent, WorkDir: strings.TrimSpace(rest), Raw: raw}
	case "ws":
		fields := strings.Fields(rest)
		sub := "list"
		if len(fields) > 0 {
			sub = strings.ToLower(fields[0])
		}
		wsName := ""
		if len(fields) > 1 {
			wsName = fields[1]
		}
		return Command{Type: CommandWs, WsSub: sub, WsName: wsName, Raw: raw}
	case ".devel":
		// Hidden developer command (not listed in /help): toggles the opt-in
		// prerelease update channel. Text carries the raw argument ("", "0", "1").
		return Command{Type: CommandDevel, Text: strings.TrimSpace(rest), Raw: raw}
	case ".go":
		// Hidden self-loop command (not listed in /help): hands the argument to
		// the agent as a "improve bridge itself" requirement, driving the
		// self-loop workflow (analyse → edit source → layered verify → report).
		// Admin-only, since it lets the bridge modify its own source tree.
		return Command{Type: CommandTodo, Text: strings.TrimSpace(rest), Raw: raw}
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
	case "action":
		// Hidden action trigger command (not listed in /help): triggers a card action
		// directly via message, equivalent to clicking the corresponding card button.
		// Usage: /action <action-id> [value]
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return Command{Type: CommandUnknown, Raw: raw, Text: "用法：/action <action-id> [value]"}
		}
		actionID := fields[0]
		actionValue := strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
		return Command{Type: CommandAction, ActionID: actionID, ActionValue: actionValue, Raw: raw}
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
					"**`/new`** `[--workdir path] [prompt]` 开新会话",
					"**`/status`** 当前会话状态",
					"**`/stop`** 停止当前任务",
					"**`/upgrade`** 升级 Bridge（开发者模式可选 `stable|rc`）",
					"**`/resume`** `[session-id]` 恢复历史会话",
					"**`/agent-mode`** 切换 claude / codex",
					"**`/cd`** `[path]` 切换本 topic 工作目录",
					"**`/ws`** `list|save|use|del [name]` 命名工作区",
				},
			},
			{
				Title: "⚙️ 配置",
				Lines: []string{
					"**`/config`** 全局运行偏好",
					"**`/local-config`** `[reset]` 本群覆盖 / 重置",
				},
			},
			{
				Title: "⏰ 定时",
				Lines: scheduleHelpLines(),
			},
			{
				Title: "🔒 权限",
				Lines: []string{
					"**`/invite`** user|admin|member @人 · group · all group",
					"**`/remove`** user|admin|member @人 · group",
					"**`/group-access`** all|selected 设置当前群成员策略",
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
		"/upgrade - upgrade Bridge to the latest release (admin only; developer mode may select stable|rc)",
		"/agent-mode - choose claude or codex for subsequent messages",
		"/cd [path] - switch the working directory for this chat/topic",
		"/ws list|save|use|del [name] - manage named workspaces",
		"/cron [list [all]] - list recurring Agent tasks in this conversation, or all tasks for admins",
		"/cron add <natural-language task> - create a recurring task after confirmation",
		"/cron info|run|enable|disable|del <id> - inspect or manage a recurring task",
		"/timer [list [all]] - list one-shot Agent tasks in this conversation, or all tasks for admins",
		"/timer add <natural-language task> - create a one-shot task after confirmation",
		"/timer info|del <id> - inspect or delete a one-shot task",
		"/config - configure global defaults",
		"/local-config [reset] - override or reset this group's inherited defaults",
		"/invite user|admin|member @user, /invite group, /invite all group - grant access",
		"/remove user|admin|member @user, /remove group - revoke access",
		"/group-access all|selected - set the current allowed group's member policy",
		"/help - show this help",
		"Group intake: mention only (default), participated topics, or all group messages; bot senders are ignored by default.",
		"",
		"Plain text continues the current configured agent conversation. Use /new to start a new session.",
	}, "\n")
}
