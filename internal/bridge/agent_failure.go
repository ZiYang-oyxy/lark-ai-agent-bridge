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
