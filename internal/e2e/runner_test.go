package e2e

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRunnerStopsAtExactFailedStepAndRunsCleanupInReverse(t *testing.T) {
	var calls []string
	scenario := Scenario{
		Name: "stop",
		Steps: []Step{
			{Name: "arm", Run: recordSuccess(&calls, "arm")},
			{Name: "ready", Run: recordFailure(&calls, "ready", Failure{Class: FailureAssertion, Assertion: "card_contains", Expected: "READY", Actual: "starting", Message: "marker missing"})},
			{Name: "stop", Run: recordSuccess(&calls, "stop")},
		},
		Cleanup: []Cleanup{
			{Name: "first-cleanup", Run: recordSuccess(&calls, "first-cleanup")},
			{Name: "second-cleanup", Run: recordSuccess(&calls, "second-cleanup")},
		},
	}

	result := NewRunner().Run(context.Background(), scenario, newRunnerContext(t))
	if want := []string{"arm", "ready", "second-cleanup", "first-cleanup"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if result.Failure == nil || result.Failure.Step != "ready" || result.Failure.Scenario != "stop" || result.Failure.Class != FailureAssertion {
		t.Fatalf("result = %#v", result)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestRunnerKeepsPrimaryFailureWhenCleanupFails(t *testing.T) {
	scenario := Scenario{
		Name: "stop",
		Steps: []Step{{Name: "wait", Run: func(context.Context, *RunContext) *Failure {
			return &Failure{Class: FailureTimeout, Message: "reply timeout", LastObserved: "no reply"}
		}}},
		Cleanup: []Cleanup{{Name: "disarm", Run: func(context.Context, *RunContext) *Failure {
			return &Failure{Class: FailureEnvironment, Message: "permission denied"}
		}}},
	}

	result := NewRunner().Run(context.Background(), scenario, newRunnerContext(t))
	if result.Failure == nil || result.Failure.Class != FailureTimeout || result.Failure.Step != "wait" {
		t.Fatalf("primary = %#v", result.Failure)
	}
	if len(result.CleanupFailures) != 1 || result.CleanupFailures[0].Class != FailureCleanup || result.CleanupFailures[0].Step != "disarm" {
		t.Fatalf("cleanup = %#v", result.CleanupFailures)
	}
}

func TestRunnerUsesStableScenarioStepForDriverFailure(t *testing.T) {
	scenario := Scenario{Name: "stop", Steps: []Step{{Name: "wait_ready_reply", Run: func(context.Context, *RunContext) *Failure {
		return &Failure{Class: FailureEnvironment, Step: "ssh_wait_reply", Message: "ssh failed"}
	}}}}

	result := NewRunner().Run(context.Background(), scenario, newRunnerContext(t))
	if result.Failure == nil || result.Failure.Step != "wait_ready_reply" {
		t.Fatalf("failure = %#v", result.Failure)
	}
}

func TestRunnerTurnsExpiredStepIntoTimeoutWithLastObservation(t *testing.T) {
	runContext := newRunnerContext(t)
	runContext.Config.StepTimeoutMS = 20
	scenario := Scenario{Name: "stop", Steps: []Step{{Name: "wait_reply", Run: func(ctx context.Context, rc *RunContext) *Failure {
		rc.SetLastObserved("audit=cardkit_reply_pending")
		<-ctx.Done()
		return nil
	}}}}

	result := NewRunner().Run(context.Background(), scenario, runContext)
	if result.Failure == nil || result.Failure.Class != FailureTimeout || result.Failure.Step != "wait_reply" || result.Failure.LastObserved != "audit=cardkit_reply_pending" {
		t.Fatalf("failure = %#v", result.Failure)
	}
}

func TestRegistryRejectsUnknownScenario(t *testing.T) {
	registry := Registry{"stop": {Name: "stop"}}
	if _, err := registry.Resolve("resume"); err == nil || err.Class != FailureHarness || err.Step != "load_scenario" {
		t.Fatalf("error = %#v", err)
	}
}

func TestRunnerWritesResultAndActionEvidence(t *testing.T) {
	runContext := newRunnerContext(t)
	scenario := Scenario{Name: "stop", Steps: []Step{{Name: "complete", Run: func(context.Context, *RunContext) *Failure { return nil }}}}

	result := NewRunner().Run(context.Background(), scenario, runContext)
	if result.Status != "passed" || result.Failure != nil {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(runContext.Evidence.Dir(), "result.json")); err != nil {
		t.Fatal(err)
	}
	actions, err := os.ReadFile(filepath.Join(runContext.Evidence.Dir(), "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actions) == "" {
		t.Fatal("actions evidence is empty")
	}
}

func recordSuccess(calls *[]string, name string) StepFunc {
	return func(context.Context, *RunContext) *Failure {
		*calls = append(*calls, name)
		return nil
	}
}

func recordFailure(calls *[]string, name string, failure Failure) StepFunc {
	return func(context.Context, *RunContext) *Failure {
		*calls = append(*calls, name)
		return &failure
	}
}

func newRunnerContext(t *testing.T) *RunContext {
	t.Helper()
	evidence := newTestEvidence(t)
	return &RunContext{
		Deployment: testDeployment(),
		Config: Config{
			SchemaVersion: 1,
			StepTimeoutMS: 1000,
		},
		Evidence: evidence,
		Values:   make(map[string]string),
	}
}

func TestRunnerReportsEvidenceWriteFailure(t *testing.T) {
	runContext := newRunnerContext(t)
	if err := os.Chmod(runContext.Evidence.Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(runContext.Evidence.Dir(), 0o700) })
	scenario := Scenario{Name: "stop", Steps: []Step{{Name: "complete", Run: func(context.Context, *RunContext) *Failure { return nil }}}}

	result := NewRunner().Run(context.Background(), scenario, runContext)
	if result.Failure == nil || result.Failure.Class != FailureEnvironment {
		t.Fatalf("result = %#v", result)
	}
}
