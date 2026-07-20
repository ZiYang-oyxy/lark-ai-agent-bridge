package agent

import (
	"fmt"
	"strings"
)

type Kind string

const (
	Claude Kind = "claude"
	Codex  Kind = "codex"
)

type ApprovalMode string

const ApprovalFull ApprovalMode = "full"

type OneShotConfig struct {
	Kind           Kind
	Bin            string
	WorkDir        string
	Prompt         string
	AgentSessionID string
	Model          string
	Effort         string
	// Home is the resolved agent home / config directory. Empty means "use the
	// agent's default home" (no config-dir environment variable is injected).
	Home string
	// Images are validated local image paths. Only Codex consumes them as
	// repeated --image flags; Claude continues to receive paths in the prompt.
	Images []string
}

func ParseKind(raw string) (Kind, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(Claude):
		return Claude, true
	case string(Codex):
		return Codex, true
	default:
		return "", false
	}
}

func BuildOneShotCommand(cfg OneShotConfig) ([]string, error) {
	switch cfg.Kind {
	case Claude:
		return buildClaudeOneShotCommand(cfg)
	case Codex:
		return buildCodexOneShotCommand(cfg)
	default:
		return nil, fmt.Errorf("agent %q is not supported", cfg.Kind)
	}
}

func buildCodexOneShotCommand(cfg OneShotConfig) ([]string, error) {
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("codex prompt is empty")
	}
	bin := strings.TrimSpace(cfg.Bin)
	if bin == "" {
		bin = "codex"
	}
	images := make([]string, 0, len(cfg.Images)*2)
	for _, path := range cfg.Images {
		if path = strings.TrimSpace(path); path != "" {
			images = append(images, "--image", path)
		}
	}
	if sessionID := strings.TrimSpace(cfg.AgentSessionID); sessionID != "" {
		args := []string{bin, "exec", "resume", "--json"}
		args = append(args, images...)
		args = append(args, sessionID, "-")
		return args, nil
	}
	args := []string{bin, "exec", "--json"}
	args = append(args, images...)
	if len(images) > 0 {
		args = append(args, "--")
	}
	return append(args, "-"), nil
}

// PromptOnStdin reports whether the agent protocol reads the prompt from
// stdin rather than from argv.
func PromptOnStdin(kind Kind) bool { return kind == Codex }

// AgentEnv returns the environment variables that select the agent's home /
// config directory for a child process. It returns nil when home is empty,
// meaning the child inherits the parent's default (the pre-feature behaviour).
// The mapping is per-kind: Claude uses CLAUDE_CONFIG_DIR, Codex uses CODEX_HOME
// (reserved for when Codex support lands).
func AgentEnv(kind Kind, home string) []string {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil
	}
	switch kind {
	case Claude:
		return []string{"CLAUDE_CONFIG_DIR=" + home}
	case Codex:
		return []string{"CODEX_HOME=" + home}
	default:
		return nil
	}
}

func buildClaudeOneShotCommand(cfg OneShotConfig) ([]string, error) {
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("claude prompt is empty")
	}
	bin := strings.TrimSpace(cfg.Bin)
	if bin == "" {
		bin = "claude"
	}
	args := []string{bin, "-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--dangerously-skip-permissions"}
	effort := strings.TrimSpace(cfg.Effort)
	if effort == "" {
		effort = "low"
	}
	if !strings.EqualFold(effort, "default") {
		args = append(args, "--effort", effort)
	}
	if model := strings.TrimSpace(cfg.Model); model != "" && !strings.EqualFold(model, "default") {
		args = append(args, "--model", model)
	}
	if sessionID := strings.TrimSpace(cfg.AgentSessionID); sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	args = append(args, prompt)
	return args, nil
}
