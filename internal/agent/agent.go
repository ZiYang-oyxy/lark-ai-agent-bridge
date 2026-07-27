package agent

import (
	"encoding/json"
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
	// ForkFromAgentSessionID triggers Claude's --fork-session mode: resume
	// from the given source id but create a brand-new session id on the fly
	// so the source session is not modified. Only honored by Claude
	// (Codex CLI currently has no headless fork). Ignored when both this and
	// AgentSessionID are set — a real session id always wins because we no
	// longer need to fork after the seed run.
	ForkFromAgentSessionID string
	Model                  string
	Effort                 string
	// Home is the resolved agent home / config directory. Empty means "use the
	// agent's default home" (no config-dir environment variable is injected).
	Home string
	// Images are validated local image paths. Only Codex consumes them as
	// repeated --image flags; Claude continues to receive paths in the prompt.
	Images []string
	// ClaudeSystemPromptFile is a bridge-owned immutable instruction file.
	ClaudeSystemPromptFile string
	// DeveloperInstructions is the bridge-owned Codex developer layer.
	DeveloperInstructions string
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
	global := []string{}
	if effort := strings.ToLower(strings.TrimSpace(cfg.Effort)); effort != "" && effort != "default" {
		global = append(global, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	if cfg.DeveloperInstructions != "" {
		encoded, err := codexDeveloperInstructionsArg(cfg.DeveloperInstructions)
		if err != nil {
			return nil, err
		}
		global = append(global, "-c", encoded)
	}
	images := make([]string, 0, len(cfg.Images)*2)
	for _, path := range cfg.Images {
		if path = strings.TrimSpace(path); path != "" {
			images = append(images, "--image", path)
		}
	}
	if sessionID := strings.TrimSpace(cfg.AgentSessionID); sessionID != "" {
		args := []string{bin, "exec"}
		args = append(args, global...)
		args = append(args, "resume", "--json")
		args = append(args, images...)
		args = append(args, sessionID, "-")
		return args, nil
	}
	args := []string{bin, "exec"}
	args = append(args, global...)
	args = append(args, "--json")
	args = append(args, images...)
	if len(images) > 0 {
		args = append(args, "--")
	}
	return append(args, "-"), nil
}

func codexDeveloperInstructionsArg(text string) (string, error) {
	if strings.ContainsRune(text, '\x00') {
		return "", fmt.Errorf("codex developer instructions contain NUL")
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return "", fmt.Errorf("encode codex developer instructions: %w", err)
	}
	return "developer_instructions=" + string(encoded), nil
}

// PromptOnStdin reports whether the agent protocol reads the prompt from
// stdin rather than from argv. Both Codex (`codex exec … -`) and Claude
// (`claude -p` with no positional prompt) do, which keeps large prompts off
// argv and clear of the kernel's per-argument MAX_ARG_STRLEN limit.
func PromptOnStdin(kind Kind) bool { return kind == Codex || kind == Claude }

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

// ContextCacheEnv pins the workspace exporter and Bridge reader to the same
// per-agent sidecar directory. A blank directory leaves export disabled.
func ContextCacheEnv(kind Kind, dir string) []string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	switch kind {
	case Claude:
		return []string{"CLAUDE_CONTEXT_CACHE_DIR=" + dir}
	case Codex:
		return []string{"CODEX_CONTEXT_CACHE_DIR=" + dir}
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
	if path := strings.TrimSpace(cfg.ClaudeSystemPromptFile); path != "" {
		if strings.ContainsRune(path, '\x00') {
			return nil, fmt.Errorf("claude system prompt file contains NUL")
		}
		args = append(args, "--append-system-prompt-file", path)
	}
	if model := strings.TrimSpace(cfg.Model); model != "" && !strings.EqualFold(model, "default") {
		args = append(args, "--model", model)
	}
	if sessionID := strings.TrimSpace(cfg.AgentSessionID); sessionID != "" {
		args = append(args, "--resume", sessionID)
	} else if forkFrom := strings.TrimSpace(cfg.ForkFromAgentSessionID); forkFrom != "" {
		// First run of a forked session: seed the history from ForkFrom but
		// let Claude mint a fresh session id so the source keeps evolving
		// independently. After this run the caller stores the new id as
		// AgentSessionID and the else branch above stops firing.
		args = append(args, "--resume", forkFrom, "--fork-session")
	}
	// Prompt is delivered on stdin, not argv: a single argv element is capped by
	// the kernel's MAX_ARG_STRLEN (~128KB on Linux) regardless of the far larger
	// ARG_MAX total. Large prompts (pasted logs, forwarded/quoted material) blew
	// past that cap and fork/exec failed with "argument list too long", killing
	// the whole run. `claude -p` with no positional prompt reads it from stdin,
	// which has no such size limit. See PromptOnStdin + service.go stdin wiring.
	return args, nil
}
