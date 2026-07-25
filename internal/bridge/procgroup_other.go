//go:build !unix

package bridge

import "os/exec"

// configureProcessGroup is a no-op on non-unix platforms. The bridge only runs
// on Linux and macOS; this stub keeps the package buildable elsewhere.
func configureProcessGroup(cmd *exec.Cmd) {}
