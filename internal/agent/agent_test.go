package agent

import (
	"strings"
	"testing"
)

func TestBuildCommands(t *testing.T) {
	claude, _ := AdapterFor(Claude)
	cmd, err := claude.BuildCommand(LaunchConfig{Kind: Claude, ApprovalMode: ApprovalFull})
	if err != nil {
		t.Fatalf("claude command error: %v", err)
	}
	if cmd[0] != "claude" || cmd[1] != "--dangerously-skip-permissions" {
		t.Fatalf("claude full command = %#v", cmd)
	}

	codex, _ := AdapterFor(Codex)
	cmd, err = codex.BuildCommand(LaunchConfig{Kind: Codex, ApprovalMode: ApprovalFull})
	if err != nil {
		t.Fatalf("codex command error: %v", err)
	}
	if got := strings.Join(cmd, " "); got != "codex -c check_for_update_on_startup=false --dangerously-bypass-approvals-and-sandbox" {
		t.Fatalf("codex full command = %#v", cmd)
	}
}

func TestBuildResumeCommands(t *testing.T) {
	claude, _ := AdapterFor(Claude)
	cmd, err := claude.BuildCommand(LaunchConfig{Kind: Claude, ApprovalMode: ApprovalFull, Resume: true, ResumeTarget: "34cc"})
	if err != nil {
		t.Fatalf("claude resume command error: %v", err)
	}
	if got := strings.Join(cmd, " "); got != "claude --dangerously-skip-permissions --resume 34cc" {
		t.Fatalf("claude resume command = %q", got)
	}

	cmd, err = claude.BuildCommand(LaunchConfig{Kind: Claude, ApprovalMode: ApprovalDefault, Resume: true, ResumeLast: true})
	if err != nil {
		t.Fatalf("claude continue command error: %v", err)
	}
	if got := strings.Join(cmd, " "); got != "claude --continue" {
		t.Fatalf("claude continue command = %q", got)
	}

	codex, _ := AdapterFor(Codex)
	cmd, err = codex.BuildCommand(LaunchConfig{Kind: Codex, ApprovalMode: ApprovalFull, Resume: true, ResumeTarget: "34cc"})
	if err != nil {
		t.Fatalf("codex resume command error: %v", err)
	}
	if got := strings.Join(cmd, " "); got != "codex -c check_for_update_on_startup=false --dangerously-bypass-approvals-and-sandbox resume 34cc" {
		t.Fatalf("codex resume command = %q", got)
	}

	cmd, err = codex.BuildCommand(LaunchConfig{Kind: Codex, ApprovalMode: ApprovalDefault, Resume: true, ResumeLast: true})
	if err != nil {
		t.Fatalf("codex last command error: %v", err)
	}
	if got := strings.Join(cmd, " "); got != "codex -c check_for_update_on_startup=false resume --last" {
		t.Fatalf("codex last command = %q", got)
	}
}
