//go:build unix

package bridge

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestConfigureProcessGroupKillsGrandchild reproduces the "点停止停不了" bug at
// the process level: an agent CLI (the direct child) forks a long-lived
// grandchild that holds the stdout write end. Without process-group killing,
// ctx cancel only reaps the direct child; the grandchild survives, stdout never
// closes, and cmd.Wait blocks forever. With configureProcessGroup the whole
// group dies and Wait returns promptly.
func TestConfigureProcessGroupKillsGrandchild(t *testing.T) {
	dir := t.TempDir()
	// PID marker so we can assert the grandchild really died.
	markerParent := filepath.Join(dir, "gc.pid")

	// Script models a real agent CLI: it backgrounds a long-lived grandchild
	// (sleep) that INHERITS this script's stdout write end, records the
	// grandchild pid, then waits. Because the grandchild holds stdout open, the
	// parent's exit alone would not EOF the pipe — mirroring how claude/codex's
	// Node wrapper forks a streaming grandchild. Only killing the whole process
	// group closes stdout and lets a reader see EOF. `sleep 60 &` (no subshell)
	// keeps the grandchild attached to the inherited fd.
	script := "#!/bin/sh\n" +
		"sleep 60 &\n" +
		"echo $! > " + markerParent + "\n" +
		"wait\n"
	scriptPath := filepath.Join(dir, "fakecli.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "/bin/sh", scriptPath)
	// Hold the stdout pipe like the real runner does, so the only way Wait
	// returns is the process group actually dying.
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	configureProcessGroup(cmd)
	// Tighten the grace period so the test is fast but still exercises the
	// TERM-then-Wait path.
	cmd.WaitDelay = 2 * time.Second

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Drain stdout exactly like parseClaudeStream does. This goroutine only
	// returns when the write end is fully closed — i.e. when every process in
	// the group (including the grandchild) is dead. This is the real signal the
	// bug hinges on.
	scanDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, pipe); close(scanDone) }()

	// Wait until the grandchild pid is on disk, then cancel.
	gcPID := waitForPID(t, markerParent)
	cancel()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// Wait returned — good. (err is non-nil: killed/canceled.)
	case <-time.After(6 * time.Second):
		t.Fatalf("cmd.Wait did not return after cancel — stop is stuck (regression)")
	}

	select {
	case <-scanDone:
		// stdout reached EOF — the grandchild's write end is gone.
	case <-time.After(2 * time.Second):
		t.Fatalf("stdout never reached EOF after cancel — grandchild still holds the pipe (regression)")
	}

	// The grandchild must be gone. Give the group kill a moment to propagate.
	if alive := pidAliveWithin(gcPID, 2*time.Second); alive {
		t.Fatalf("grandchild pid %d still alive after cancel — process group not killed", gcPID)
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild pid marker never appeared at %s", path)
	return 0
}

// pidAliveWithin reports whether pid is still alive, polling until it dies or
// the timeout elapses. signal 0 probes existence without affecting the process.
func pidAliveWithin(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return false // no such process
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
}
