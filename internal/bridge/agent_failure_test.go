package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/bridgeinstructions"
)

func TestAgentFailureAuditDetailUsesStructuredSourceAndRedactedTail(t *testing.T) {
	noise := "HOOK_HEAD_SENTINEL\n" + strings.Repeat("hook-start\n", 700)
	err := newAgentProcessError(
		errors.New("exit status 1"),
		agentFailureSourceResult,
		noise+"token=super-secret\nRESULT_TAIL_SENTINEL",
	)
	detail := agentFailureAuditDetail(agent.Claude, err)
	if !containsAll(detail, "agent=claude", "source=result", "truncated=true", "RESULT_TAIL_SENTINEL", "token=[REDACTED]") {
		t.Fatalf("detail = %q", detail)
	}
	if strings.Contains(detail, "HOOK_HEAD_SENTINEL") || strings.Contains(detail, "super-secret") {
		t.Fatalf("detail leaked head or secret: %q", detail)
	}
}

func TestAgentFailureAuditDetailFallsBackToGenericError(t *testing.T) {
	detail := agentFailureAuditDetail(agent.Codex, errors.New("provider unavailable"))
	if !containsAll(detail, "agent=codex", "source=error", "truncated=false", "provider unavailable") {
		t.Fatalf("detail = %q", detail)
	}
}

func TestUTF8SafeTailDropsOnlyPartialLeadingCodePoint(t *testing.T) {
	value := strings.Repeat("界", 1366) + "尾"
	tail, truncated := utf8SafeTail(value, agentFailureAuditTailBytes)
	if !truncated || !utf8.ValidString(tail) || len(tail) > agentFailureAuditTailBytes {
		t.Fatalf("tail bytes=%d truncated=%t valid=%t", len(tail), truncated, utf8.ValidString(tail))
	}
	if !strings.HasSuffix(tail, "尾") {
		t.Fatalf("tail lost diagnostic suffix: %q", tail)
	}
}

func TestCLIExecRunnerPreservesFailureDiagnosticSource(t *testing.T) {
	runtime, err := bridgeinstructions.NewRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	tests := []struct {
		name       string
		script     string
		wantSource agentFailureSource
		wantTail   string
	}{
		{
			name: "stderr takes precedence",
			script: `#!/bin/sh
printf '%s\n' '{"type":"assistant","message":{"model":"fake","content":[{"type":"text","text":"ignored result"}]}}'
printf '%s\n' 'STDERR_TAIL_SENTINEL' >&2
exit 17
`,
			wantSource: agentFailureSourceStderr,
			wantTail:   "STDERR_TAIL_SENTINEL",
		},
		{
			name: "result is fallback",
			script: `#!/bin/sh
printf '%s\n' '{"type":"assistant","message":{"model":"fake","content":[{"type":"text","text":"RESULT_TAIL_SENTINEL"}]}}'
exit 19
`,
			wantSource: agentFailureSourceResult,
			wantTail:   "RESULT_TAIL_SENTINEL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(bin, []byte(tc.script), 0o755); err != nil {
				t.Fatal(err)
			}
			_, runErr := (CLIExecRunner{Instructions: runtime}).Run(context.Background(), AgentRunRequest{
				Kind: agent.Claude, Bin: bin, Prompt: "fail", BridgeInstructionsVersion: bridgeinstructions.CurrentVersion,
			})
			if runErr == nil {
				t.Fatal("runner error = nil")
			}
			var processErr *agentProcessError
			if !errors.As(runErr, &processErr) {
				t.Fatalf("runner error type = %T, want *agentProcessError", runErr)
			}
			if processErr.source != tc.wantSource || !strings.Contains(processErr.diagnostic, tc.wantTail) {
				t.Fatalf("process error = %#v, want source=%s tail=%q", processErr, tc.wantSource, tc.wantTail)
			}
		})
	}
}
