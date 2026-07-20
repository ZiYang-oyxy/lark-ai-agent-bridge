package commanddriver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"lark-agent-bridge/internal/e2e"
	"lark-agent-bridge/internal/security"
)

type Command struct {
	Name  string
	Args  []string
	Stdin []byte
	Env   []string
}

type Output struct {
	Stdout []byte
	Stderr []byte
}

type Executor interface {
	Run(context.Context, Command) (Output, error)
}

type OSExecutor struct{}

func (OSExecutor) Run(ctx context.Context, spec Command) (Output, error) {
	command := exec.CommandContext(ctx, spec.Name, spec.Args...)
	command.Stdin = bytes.NewReader(spec.Stdin)
	if spec.Env != nil {
		command.Env = append(os.Environ(), spec.Env...)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
}

type commandKind int

const (
	commandLark commandKind = iota
	commandSSH
)

func classifyCommandFailure(kind commandKind, err error, stderr []byte, step string) *e2e.Failure {
	class := e2e.FailureEnvironment
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || exitCode(err) == 124 {
		class = e2e.FailureTimeout
	} else if kind == commandLark && !errors.Is(err, os.ErrNotExist) {
		class = e2e.FailurePlatform
	}
	detail := security.Redact(string(bytes.TrimSpace(stderr)))
	if detail == "" {
		detail = err.Error()
	}
	return &e2e.Failure{Class: class, Step: step, Message: fmt.Sprintf("%s command failed: %s", step, detail)}
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
