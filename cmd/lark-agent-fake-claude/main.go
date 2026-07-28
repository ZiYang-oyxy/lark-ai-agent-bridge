// Command lark-agent-fake-claude replaces the shell shim heredoc that was
// baked into scripts/e2e/lib/server.sh's prepare_fake_claude_if_needed.
//
// It reads fixture JSONs from LAB_FAKE_FIXTURE_DIR (defaults to
// scripts/e2e/fixtures under the repo containing this binary) and dispatches
// on the last E2E_* marker in argv or a prompt substring match.
//
// Log file: appends "pid=<pid> args=<argv>" to $FAKE_CLAUDE_LOG when set, so
// existing L2 cases (which grep this log for markers) keep working.
//
// The instruction file must contain "Feishu Bridge Runtime Instructions"
// (mirrors the shell shim invariant) unless the prompt is the harmless
// pre-flight canary "Reply with exactly OK. Do not use tools."
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"lark-agent-bridge/internal/fakeclaude"
)

func die(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(code)
}

func main() {
	argv := os.Args[1:]

	// Mirror the shell shim's log side-effect: append pid + args to the log
	// file so e2e cases can grep for markers.
	if logPath := os.Getenv("FAKE_CLAUDE_LOG"); logPath != "" {
		if err := appendLog(logPath, os.Getpid(), argv); err != nil {
			die(1, "fake-claude: append log %s: %v", logPath, err)
		}
	}

	inv := fakeclaude.NewInvocation(argv)

	// Instruction-file validation is skipped for the canary probe (the shell
	// shim did the same via `case "$prompt" in *'Reply with exactly OK...`).
	if !strings.Contains(inv.Prompt, "Reply with exactly OK. Do not use tools.") {
		if err := fakeclaude.ValidateInstruction(argv); err != nil {
			// The shim used exit 92 when the instruction leaked into the user
			// prompt; preserve that specific code so upstream cases keep
			// matching. Other validation failures use 1.
			if strings.Contains(err.Error(), "leaked") {
				die(92, "fake-claude: %v", err)
			}
			die(1, "fake-claude: %v", err)
		}
	}

	fixtures, err := loadFixtures()
	if err != nil {
		die(1, "fake-claude: %v", err)
	}
	f := fakeclaude.Resolve(fixtures, inv)
	if err := emit(os.Stdout, f); err != nil {
		die(1, "fake-claude: emit %s: %v", f.Name, err)
	}
	if f.PostDelay > 0 {
		time.Sleep(time.Duration(f.PostDelay * float64(time.Second)))
	}
}

func loadFixtures() ([]fakeclaude.Fixture, error) {
	// LAB_FAKE_FIXTURE_DIR is required — no automatic path guessing, so that
	// the calling context (Makefile / rebuild-test / L1 shim wrapper) always
	// makes the fixture source explicit and auditable. Fail-closed on missing.
	dir := os.Getenv("LAB_FAKE_FIXTURE_DIR")
	if dir == "" {
		return nil, fmt.Errorf("LAB_FAKE_FIXTURE_DIR not set (require explicit fixture dir; e.g. LAB_FAKE_FIXTURE_DIR=$REPO/scripts/e2e/fixtures)")
	}
	return fakeclaude.LoadDir(dir)
}

func appendLog(path string, pid int, argv []string) error {
	// Reproduce the shim's format so audit greps keep matching.
	line := fmt.Sprintf("pid=%d args=%s\n", pid, strings.Join(argv, " "))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.WriteString(f, line)
	return err
}

func emit(w io.Writer, f fakeclaude.Fixture) error {
	for _, e := range f.Emit {
		if e.DelaySec > 0 {
			time.Sleep(time.Duration(e.DelaySec * float64(time.Second)))
		}
		if _, err := fmt.Fprintln(w, e.Line); err != nil {
			return err
		}
	}
	return nil
}
