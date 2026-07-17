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
	WorkDir         string
	Prompt          string
	ClaudeSessionID string
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
	args := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--effort", "low"}
	if sessionID := strings.TrimSpace(cfg.ClaudeSessionID); sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	args = append(args, prompt)
	return args, nil
}
