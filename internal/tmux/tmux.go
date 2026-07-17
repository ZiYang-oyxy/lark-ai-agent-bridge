package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

type Manager struct {
	Session         string
	Runner          CommandRunner
	SendSubmitDelay time.Duration
}

type WindowSpec struct {
	Name    string
	WorkDir string
	Command []string
}

func NewManager(session string, runner CommandRunner) *Manager {
	sendSubmitDelay := time.Duration(0)
	if runner == nil {
		runner = ExecRunner{}
		sendSubmitDelay = 650 * time.Millisecond
	}
	return &Manager{Session: session, Runner: runner, SendSubmitDelay: sendSubmitDelay}
}

func (m *Manager) EnsureSession(ctx context.Context) error {
	if m.Session == "" {
		return fmt.Errorf("tmux session is empty")
	}
	_, err := m.Runner.Run(ctx, "tmux", "has-session", "-t", m.Session)
	if err == nil {
		return nil
	}
	if out, err := m.Runner.Run(ctx, "tmux", "new-session", "-d", "-s", m.Session, "-n", "bridge-control"); err != nil {
		return fmt.Errorf("create tmux session %q: %w: %s", m.Session, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) NewWindow(ctx context.Context, spec WindowSpec) error {
	if err := m.EnsureSession(ctx); err != nil {
		return err
	}
	if spec.Name == "" {
		return fmt.Errorf("tmux window name is empty")
	}
	if len(spec.Command) == 0 {
		return fmt.Errorf("tmux window command is empty")
	}
	args := []string{"new-window", "-d", "-t", m.Session, "-n", spec.Name}
	if spec.WorkDir != "" {
		args = append(args, "-c", spec.WorkDir)
	}
	args = append(args, shellJoin(spec.Command))
	if out, err := m.Runner.Run(ctx, "tmux", args...); err != nil {
		return fmt.Errorf("create tmux window %q: %w: %s", spec.Name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) SendLine(ctx context.Context, windowName, line string) error {
	if windowName == "" {
		return fmt.Errorf("tmux window name is empty")
	}
	target := m.target(windowName)
	if line != "" {
		if out, err := m.Runner.Run(ctx, "tmux", "send-keys", "-t", target, "--", line); err != nil {
			return fmt.Errorf("send input to %q: %w: %s", target, err, strings.TrimSpace(string(out)))
		}
		if m.SendSubmitDelay > 0 {
			time.Sleep(m.SendSubmitDelay)
		}
	}
	if out, err := m.Runner.Run(ctx, "tmux", "send-keys", "-t", target, "C-m"); err != nil {
		return fmt.Errorf("send input to %q: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) Interrupt(ctx context.Context, windowName string) error {
	target := m.target(windowName)
	if out, err := m.Runner.Run(ctx, "tmux", "send-keys", "-t", target, "C-c"); err != nil {
		return fmt.Errorf("interrupt %q: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) KillPaneDescendants(ctx context.Context, windowName string, preserveExecutables ...string) error {
	target := m.target(windowName)
	out, err := m.Runner.Run(ctx, "tmux", "display-message", "-p", "-t", target, "#{pane_pid}")
	if err != nil {
		return fmt.Errorf("get pane pid %q: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("parse pane pid %q: %w", target, err)
	}
	if panePID <= 0 {
		return fmt.Errorf("parse pane pid %q: invalid pid %d", target, panePID)
	}
	out, err = m.Runner.Run(ctx, "ps", "-axo", "pid=,ppid=,command=")
	if err != nil {
		return fmt.Errorf("list process tree for %q: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	processes := parseProcessList(string(out))
	killPIDs := descendantToolPIDs(panePID, processes, preserveExecutables)
	if len(killPIDs) == 0 {
		return nil
	}
	sort.Ints(killPIDs)
	args := []string{"-TERM"}
	for _, pid := range killPIDs {
		args = append(args, strconv.Itoa(pid))
	}
	if out, err := m.Runner.Run(ctx, "kill", args...); err != nil {
		return fmt.Errorf("kill pane descendants for %q: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) CapturePane(ctx context.Context, windowName string, lastLines int) (string, error) {
	if lastLines <= 0 {
		lastLines = 200
	}
	target := m.target(windowName)
	out, err := m.Runner.Run(ctx, "tmux", "capture-pane", "-p", "-t", target, "-S", fmt.Sprintf("-%d", lastLines))
	if err != nil {
		return "", fmt.Errorf("capture %q: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (m *Manager) KillWindow(ctx context.Context, windowName string) error {
	if windowName == "" {
		return nil
	}
	if out, err := m.Runner.Run(ctx, "tmux", "kill-window", "-t", m.target(windowName)); err != nil {
		return fmt.Errorf("kill window %q: %w: %s", windowName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) KillSession(ctx context.Context) error {
	if m.Session == "" {
		return nil
	}
	if out, err := m.Runner.Run(ctx, "tmux", "kill-session", "-t", m.Session); err != nil {
		return fmt.Errorf("kill tmux session %q: %w: %s", m.Session, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) AttachCommand(windowName string) string {
	if windowName == "" {
		return fmt.Sprintf("tmux attach -t %s", shellQuote(m.Session))
	}
	return fmt.Sprintf("tmux attach -t %s:%s", shellQuote(m.Session), shellQuote(windowName))
}

func (m *Manager) target(windowName string) string {
	return fmt.Sprintf("%s:%s", m.Session, windowName)
}

type processInfo struct {
	PID     int
	PPID    int
	Command string
}

func parseProcessList(raw string) []processInfo {
	var processes []processInfo
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		processes = append(processes, processInfo{
			PID:     pid,
			PPID:    ppid,
			Command: strings.Join(fields[2:], " "),
		})
	}
	return processes
}

func descendantToolPIDs(rootPID int, processes []processInfo, preserveExecutables []string) []int {
	children := map[int][]processInfo{}
	for _, proc := range processes {
		children[proc.PPID] = append(children[proc.PPID], proc)
	}
	var killPIDs []int
	var walk func(int)
	walk = func(pid int) {
		for _, child := range children[pid] {
			if !matchesExecutable(child.Command, preserveExecutables) {
				killPIDs = append(killPIDs, child.PID)
			}
			walk(child.PID)
		}
	}
	walk(rootPID)
	return killPIDs
}

func matchesExecutable(command string, names []string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	exe := filepath.Base(fields[0])
	for _, name := range names {
		if exe == name {
			return true
		}
	}
	return false
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') &&
			!(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') &&
			!strings.ContainsRune("@%_+=:,./-", r)
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
