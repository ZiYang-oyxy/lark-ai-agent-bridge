//go:build unix

package bridge

import (
	"os/exec"
	"syscall"
	"time"
)

// stopGracePeriod is how long we wait after signalling the process group with
// SIGTERM before Go escalates to SIGKILL and forces cmd.Wait to return.
const stopGracePeriod = 5 * time.Second

// configureProcessGroup makes the agent CLI killable as a whole tree.
//
// The agent CLIs (claude / codex exec) are Node wrappers that fork the process
// actually streaming on stdout. Go's default exec.CommandContext cancellation
// only SIGKILLs the direct child (the Node shell); the grandchild is reparented
// to init and keeps running, keeps the stdout write end open, and our scanner
// blocks on Scan() forever — so cmd.Wait never returns and the "stopped" card /
// finish audit never fire. That is exactly the "点停止停不了" failure.
//
// Fix: put the child in its own process group (Setpgid) and, on ctx cancel,
// signal the whole group (kill(-pgid)) with SIGTERM first. WaitDelay guarantees
// Go escalates to SIGKILL and unblocks cmd.Wait even if something ignores TERM.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid targets the entire process group led by the child.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			// Group gone / already reaped is not an error worth surfacing;
			// fall back to killing the direct child so cancel still means kill.
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = stopGracePeriod
}
