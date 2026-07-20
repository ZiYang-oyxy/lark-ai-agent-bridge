package commanddriver

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"lark-agent-bridge/internal/e2e"
)

type executorResult struct {
	output Output
	err    error
}

type recordingExecutor struct {
	Commands []Command
	Results  []executorResult
}

func (e *recordingExecutor) Run(_ context.Context, command Command) (Output, error) {
	e.Commands = append(e.Commands, command)
	if len(e.Results) == 0 {
		return Output{}, nil
	}
	result := e.Results[0]
	e.Results = e.Results[1:]
	return result.output, result.err
}

func TestClassifyCommandFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind commandKind
		want e2e.FailureClass
	}{
		{"missing command", os.ErrNotExist, commandLark, e2e.FailureEnvironment},
		{"lark exit", errors.New("exit status 1"), commandLark, e2e.FailurePlatform},
		{"ssh exit", errors.New("exit status 255"), commandSSH, e2e.FailureEnvironment},
		{"deadline", context.DeadlineExceeded, commandLark, e2e.FailureTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failure := classifyCommandFailure(tt.kind, tt.err, []byte("token=private"), "invoke")
			if failure.Class != tt.want || failure.Step != "invoke" {
				t.Fatalf("failure = %#v", failure)
			}
			if failure.Message == "" || reflect.DeepEqual(failure.Message, "token=private") {
				t.Fatalf("unredacted failure = %#v", failure)
			}
		})
	}
}

func validConfig() e2e.Config {
	return e2e.Config{
		SchemaVersion:   1,
		Profile:         "profile",
		ExpectedBotName: "Test Bot",
		AppID:           "cli_app",
		ChatID:          "oc_chat",
		RemoteHost:      "user@example.test",
		AuditPath:       "/workspace/.bridge/audit.jsonl",
		PollIntervalMS:  20,
		StepTimeoutMS:   1000,
	}
}

func validDeployment() e2e.Deployment {
	return e2e.Deployment{
		SchemaVersion: 1,
		Transaction:   "/state/deployments/txn.1",
		CandidatePID:  42,
		SourceCommit:  "abc123",
		BinarySHA256:  "sha256:abcd",
		Workspace:     "/workspace",
		StateDir:      "/state",
		FixtureDir:    "/state/fixtures",
	}
}

func assertArgPair(t *testing.T, args []string, name, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name && args[i+1] == value {
			return
		}
	}
	t.Fatalf("args %#v do not contain %q %q", args, name, value)
}
