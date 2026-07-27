package bridge

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/security"
)

const agentFailureAuditTailBytes = 4096

// partialInterruptionNotice is appended to the preserved partial output when an
// agent process dies mid-stream after having produced real content.
const partialInterruptionNotice = "⚠️ 上游连接中断，本轮未完成。以上为中断前已产出的内容，可重新发送以继续。"

// partialAgentInterruption reports whether a failed run should be surfaced as a
// content-preserving interruption rather than a bare error. It is true only
// when the process itself failed (an agentProcessError — not a context cancel,
// which is handled earlier) AND the stream had already yielded real ordered
// content before the process died. Both conditions must hold: a process that
// failed with no output is a genuine failure, and a non-process error never
// reaches this path with partial stream content.
func partialAgentInterruption(runErr error, result AgentRunResult) bool {
	if runErr == nil {
		return false
	}
	var processErr *agentProcessError
	if !errors.As(runErr, &processErr) {
		return false
	}
	return len(result.OrderedSegments) > 0
}

type agentFailureSource string

const (
	agentFailureSourceStderr agentFailureSource = "stderr"
	agentFailureSourceResult agentFailureSource = "result"
	agentFailureSourceError  agentFailureSource = "error"
)

type agentProcessError struct {
	cause      error
	source     agentFailureSource
	diagnostic string
}

func newAgentProcessError(cause error, source agentFailureSource, diagnostic string) error {
	return &agentProcessError{cause: cause, source: source, diagnostic: diagnostic}
}

func (e *agentProcessError) Error() string {
	if strings.TrimSpace(e.diagnostic) == "" {
		return e.cause.Error()
	}
	return fmt.Sprintf("%v: %s", e.cause, e.diagnostic)
}

func (e *agentProcessError) Unwrap() error { return e.cause }

func agentFailureAuditDetail(kind agent.Kind, runErr error) string {
	source, diagnostic := agentFailureSourceError, runErr.Error()
	var processErr *agentProcessError
	if errors.As(runErr, &processErr) {
		source, diagnostic = processErr.source, processErr.diagnostic
	}
	redacted := security.Redact(strings.TrimSpace(diagnostic))
	tail, truncated := utf8SafeTail(redacted, agentFailureAuditTailBytes)
	return fmt.Sprintf("agent=%s source=%s truncated=%t tail=%s", kind, source, truncated, tail)
}

func utf8SafeTail(value string, limit int) (string, bool) {
	if limit <= 0 {
		return "", value != ""
	}
	if len(value) <= limit {
		return value, false
	}
	tail := value[len(value)-limit:]
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	return tail, true
}
