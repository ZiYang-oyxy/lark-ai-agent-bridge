package bridge

import "time"

// stopGracePeriod is how long we wait after signalling the agent process group
// with SIGTERM before Go escalates to SIGKILL (via cmd.WaitDelay) and before we
// force-close the stdout pipe from the Runner to unblock parseStream. Shared by
// procgroup_unix.go and service.go's stop-cancel fallback goroutine.
const stopGracePeriod = 5 * time.Second
