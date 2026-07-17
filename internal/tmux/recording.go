package tmux

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"lark-agent-bridge/internal/security"
)

type RecordedCommand struct {
	Name string
	Args []string
}

type RecordingRunner struct {
	mu        sync.Mutex
	Commands  []RecordedCommand
	Fail      map[string]error
	Responses map[string][][]byte
}

func NewRecordingRunner() *RecordingRunner {
	return &RecordingRunner{Fail: map[string]error{}, Responses: map[string][][]byte{}}
}

func (r *RecordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := append([]string(nil), args...)
	r.Commands = append(r.Commands, RecordedCommand{Name: name, Args: cp})
	key := name + " " + strings.Join(args, " ")
	if err, ok := r.Fail[key]; ok {
		return []byte(err.Error()), err
	}
	if responses := r.Responses[key]; len(responses) > 0 {
		out := responses[0]
		r.Responses[key] = responses[1:]
		return out, nil
	}
	if name == "tmux" && len(args) >= 1 && args[0] == "has-session" {
		return []byte("missing"), fmt.Errorf("missing session")
	}
	return nil, nil
}

func (r *RecordingRunner) Snapshot() []RecordedCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]RecordedCommand, len(r.Commands))
	for i, cmd := range r.Commands {
		cp[i] = RecordedCommand{Name: cmd.Name, Args: append([]string(nil), cmd.Args...)}
		for j, arg := range cp[i].Args {
			cp[i].Args[j] = security.Redact(arg)
		}
	}
	return cp
}
