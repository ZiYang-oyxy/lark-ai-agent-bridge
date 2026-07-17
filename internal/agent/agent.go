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

const (
	ApprovalDefault ApprovalMode = "default"
	ApprovalAuto    ApprovalMode = "auto"
	ApprovalFull    ApprovalMode = "full"
)

type LaunchConfig struct {
	Kind         Kind
	WorkDir      string
	ApprovalMode ApprovalMode
	Resume       bool
	ResumeTarget string
	ResumeLast   bool
}

type Adapter interface {
	Kind() Kind
	Executable() string
	BuildCommand(LaunchConfig) ([]string, error)
}

func ParseKind(raw string) (Kind, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(Claude):
		return Claude, true
	case string(Codex):
		return Codex, true
	default:
		return "", false
	}
}

func ParseApprovalMode(raw string) (ApprovalMode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ApprovalDefault):
		return ApprovalDefault, true
	case string(ApprovalAuto):
		return ApprovalAuto, true
	case string(ApprovalFull):
		return ApprovalFull, true
	default:
		return "", false
	}
}

func AdapterFor(kind Kind) (Adapter, error) {
	switch kind {
	case Claude:
		return ClaudeAdapter{}, nil
	case Codex:
		return CodexAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported agent %q", kind)
	}
}

type ClaudeAdapter struct{}

func (ClaudeAdapter) Kind() Kind         { return Claude }
func (ClaudeAdapter) Executable() string { return "claude" }

func (a ClaudeAdapter) BuildCommand(cfg LaunchConfig) ([]string, error) {
	args := []string{a.Executable()}
	switch cfg.ApprovalMode {
	case ApprovalDefault:
	case ApprovalAuto:
		args = append(args, "--permission-mode", "acceptEdits")
	case ApprovalFull:
		args = append(args, "--dangerously-skip-permissions")
	default:
		return nil, fmt.Errorf("unsupported approval mode %q", cfg.ApprovalMode)
	}
	if cfg.ResumeLast {
		args = append(args, "--continue")
	} else if cfg.Resume {
		args = append(args, "--resume")
		if cfg.ResumeTarget != "" {
			args = append(args, cfg.ResumeTarget)
		}
	}
	return args, nil
}

type CodexAdapter struct{}

func (CodexAdapter) Kind() Kind         { return Codex }
func (CodexAdapter) Executable() string { return "codex" }

func (a CodexAdapter) BuildCommand(cfg LaunchConfig) ([]string, error) {
	args := []string{a.Executable(), "-c", "check_for_update_on_startup=false"}
	switch cfg.ApprovalMode {
	case ApprovalDefault:
	case ApprovalAuto:
		args = append(args, "--ask-for-approval", "on-request")
	case ApprovalFull:
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	default:
		return nil, fmt.Errorf("unsupported approval mode %q", cfg.ApprovalMode)
	}
	if cfg.Resume {
		args = append(args, "resume")
		if cfg.ResumeLast {
			args = append(args, "--last")
		} else if cfg.ResumeTarget != "" {
			args = append(args, cfg.ResumeTarget)
		}
	}
	return args, nil
}
