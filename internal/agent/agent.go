package agent

import (
	"fmt"
	"strings"
)

type Kind string

const Claude Kind = "claude"

type ApprovalMode string

const ApprovalFull ApprovalMode = "full"

type OneShotConfig struct {
	Kind            Kind
	Bin             string
	WorkDir         string
	Prompt          string
	ClaudeSessionID string
	Model           string
	Effort          string
}

func ParseKind(raw string) (Kind, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(Claude):
		return Claude, true
	default:
		return "", false
	}
}

func BuildOneShotCommand(cfg OneShotConfig) ([]string, error) {
	switch cfg.Kind {
	case Claude:
		return buildClaudeOneShotCommand(cfg)
	default:
		return nil, fmt.Errorf("agent %q is not supported", cfg.Kind)
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
	args := []string{bin, "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
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
	if sessionID := strings.TrimSpace(cfg.ClaudeSessionID); sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	args = append(args, prompt)
	return args, nil
}
