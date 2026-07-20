package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResumeScenarioCoversCatalogRestartAndBackendArgv(t *testing.T) {
	fake := newFakeResumeDrivers()
	rc := newRunnerContext(t)
	rc.Drivers = fake.driverSet()
	rc.SetValue(resumeSeedKey, "TESTSEED1234")
	result := NewRunner().Run(context.Background(), ResumeScenario(), rc)
	if result.Status != "passed" {
		t.Fatalf("result = %#v", result)
	}
	if fake.restarts != 1 {
		t.Fatalf("restarts = %d", fake.restarts)
	}
	if len(fake.completed) < 13 {
		t.Fatalf("completed sessions = %#v", fake.completed)
	}
	if fake.backendCommands != 14 {
		t.Fatalf("backend commands = %d, want 14", fake.backendCommands)
	}
	if fake.resultWaits != 13 {
		t.Fatalf("terminal result waits = %d, want 13", fake.resultWaits)
	}
	data, err := os.ReadFile(filepath.Join(rc.Evidence.Dir(), "fixture-invocations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"event":"started"`) || !strings.Contains(string(data), `"event":"cancelled"`) {
		t.Fatalf("incomplete invocation evidence: %s", data)
	}
}

func TestResumeScenarioClassifiesMissingResumeArgvAsProduct(t *testing.T) {
	fake := newFakeResumeDrivers()
	fake.omitResume = true
	rc := newRunnerContext(t)
	rc.Drivers = fake.driverSet()
	rc.SetValue(resumeSeedKey, "TESTSEED1234")
	result := NewRunner().Run(context.Background(), ResumeScenario(), rc)
	if result.Failure == nil || result.Failure.Class != FailureProduct || result.Failure.Assertion != "fixture_argv_contains_resume" {
		t.Fatalf("failure = %#v", result.Failure)
	}
}

type fakeResumeDrivers struct {
	mark            int64
	nextSource      int
	bodies          map[string]string
	armed           *AgentFixturePlan
	plans           map[string]AgentFixturePlan
	invocations     map[string]map[string]AgentInvocation
	completed       []string
	selected        string
	busy            bool
	restarts        int
	omitResume      bool
	backendCommands int
	resultWaits     int
}

func newFakeResumeDrivers() *fakeResumeDrivers {
	return &fakeResumeDrivers{bodies: map[string]string{}, plans: map[string]AgentFixturePlan{}, invocations: map[string]map[string]AgentInvocation{}}
}

func (f *fakeResumeDrivers) driverSet() Drivers {
	return Drivers{Messenger: f, Replies: f, Audit: f, AgentFixture: f, Lifecycle: f}
}

func (f *fakeResumeDrivers) Mark(context.Context) (int64, *Failure) { f.mark++; return f.mark, nil }
func (f *fakeResumeDrivers) Wait(_ context.Context, _ int64, match AuditMatch) (AuditEvent, *Failure) {
	if match.Action == "cardkit_update" && match.Detail == "event=result" {
		f.resultWaits++
	}
	return AuditEvent{Action: match.Action, Detail: match.Detail}, nil
}

func (f *fakeResumeDrivers) SendText(_ context.Context, body string) (string, *Failure) {
	f.nextSource++
	source := fmt.Sprintf("source-%d", f.nextSource)
	f.bodies[source] = body
	if plan, ok := f.plans[body]; ok && !strings.HasPrefix(body, "/resume ") {
		args := []string{"-p", "--output-format", "stream-json"}
		if f.selected != "" && !f.omitResume {
			args = append(args, "--resume", f.selected)
		}
		args = append(args, body)
		started := AgentInvocation{SchemaVersion: 1, Event: "started", RunID: plan.RunID, SessionID: plan.SessionID, Argv: args}
		started.Raw, _ = json.Marshal(started)
		f.invocations[plan.RunID] = map[string]AgentInvocation{"started": started}
		f.backendCommands++
		if plan.Block {
			f.busy = true
		} else {
			finished := AgentInvocation{SchemaVersion: 1, Event: "finished", RunID: plan.RunID, SessionID: plan.SessionID}
			finished.Raw, _ = json.Marshal(finished)
			f.invocations[plan.RunID]["finished"] = finished
		}
	}
	if body == "/stop" && f.busy {
		f.busy = false
		for runID, events := range f.invocations {
			if _, finished := events["finished"]; !finished {
				cancelled := AgentInvocation{SchemaVersion: 1, Event: "cancelled", RunID: runID, SessionID: f.selected, Cancelled: true}
				cancelled.Raw, _ = json.Marshal(cancelled)
				events["cancelled"] = cancelled
			}
		}
	}
	return source, nil
}

func (f *fakeResumeDrivers) WaitReply(_ context.Context, _ int64, source string) (Reply, *Failure) {
	body := f.bodies[source]
	text := ""
	switch {
	case body == "/agent-mode claude":
		text = "Claude"
	case body == "/resume":
		var list []string
		for i := len(f.completed) - 1; i >= 0 && len(list) < 10; i-- {
			list = append(list, f.completed[i])
		}
		text = strings.Join(list, "\n")
		if f.selected != "" {
			text += "\n当前 " + f.selected
		}
	case body == "/stop":
		text = "已请求停止当前任务"
	case strings.HasPrefix(body, "/resume "):
		target := strings.TrimPrefix(body, "/resume ")
		if f.busy {
			text = "当前会话仍有正在执行或排队的任务"
		} else if containsString(f.completed, target) {
			f.selected = target
			text = "已恢复 Session `" + target + "`"
		} else {
			text = "找不到该 Session"
		}
	default:
		plan, ok := f.plans[body]
		if !ok {
			return Reply{}, &Failure{Class: FailureHarness, Message: "unexpected body " + body}
		}
		text = plan.Text
		if !plan.Block {
			f.completed = append(f.completed, plan.SessionID)
			f.selected = plan.SessionID
		}
	}
	raw, _ := json.Marshal(map[string]string{"text": text})
	return Reply{MessageID: source, Raw: raw}, nil
}

func (f *fakeResumeDrivers) ContainsVisibleText(reply Reply, expected string) (bool, string, *Failure) {
	actual := string(reply.Raw)
	return strings.Contains(actual, expected), actual, nil
}

func (f *fakeResumeDrivers) ArmAgent(_ context.Context, plan AgentFixturePlan) *Failure {
	f.armed = &plan
	f.plans[plan.Prompt] = plan
	return nil
}
func (f *fakeResumeDrivers) WaitAgentInvocation(_ context.Context, runID, event string) (AgentInvocation, *Failure) {
	if invocation, ok := f.invocations[runID][event]; ok {
		return invocation, nil
	}
	return AgentInvocation{}, &Failure{Class: FailureTimeout, Message: "invocation missing"}
}
func (f *fakeResumeDrivers) HasAgentInvocation(_ context.Context, runID string) (bool, *Failure) {
	_, ok := f.invocations[runID]
	return ok, nil
}
func (f *fakeResumeDrivers) DisarmAgent(context.Context) *Failure { f.armed = nil; return nil }
func (f *fakeResumeDrivers) RestartCandidate(context.Context) (int, *Failure) {
	f.restarts++
	return 99, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
