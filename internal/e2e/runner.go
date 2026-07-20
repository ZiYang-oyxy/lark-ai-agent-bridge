package e2e

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type StepFunc func(context.Context, *RunContext) *Failure

type Step struct {
	Name string
	Run  StepFunc
}

type Cleanup struct {
	Name string
	Run  StepFunc
}

type Scenario struct {
	Name    string
	Steps   []Step
	Cleanup []Cleanup
}

type Registry map[string]Scenario

func (r Registry) Resolve(name string) (Scenario, *Failure) {
	scenario, ok := r[name]
	if !ok {
		return Scenario{}, &Failure{
			Class:    FailureHarness,
			Scenario: name,
			Step:     "load_scenario",
			Message:  fmt.Sprintf("unknown scenario %q", name),
		}
	}
	return scenario, nil
}

type RunContext struct {
	Deployment Deployment
	Config     Config
	Evidence   *Evidence
	Drivers    Drivers

	mu           sync.Mutex
	Values       map[string]string
	lastObserved string
}

func (r *RunContext) SetValue(key, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Values == nil {
		r.Values = make(map[string]string)
	}
	r.Values[key] = value
}

func (r *RunContext) Value(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Values[key]
}

func (r *RunContext) SetLastObserved(value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastObserved = value
}

func (r *RunContext) LastObserved() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastObserved
}

type Runner struct {
	now func() time.Time
}

func NewRunner() *Runner {
	return &Runner{now: time.Now}
}

func (r *Runner) Run(ctx context.Context, scenario Scenario, runContext *RunContext) Result {
	started := r.now()
	result := Result{
		SchemaVersion: 1,
		Scenario:      scenario.Name,
		Status:        "passed",
		StartedAt:     started,
	}
	if runContext == nil || runContext.Evidence == nil {
		result.Status = "failed"
		result.Failure = &Failure{Class: FailureHarness, Scenario: scenario.Name, Step: "initialize", Message: "run context and evidence are required"}
		result.FinishedAt = r.now()
		return result
	}

	for _, step := range scenario.Steps {
		if failure := r.appendAction(runContext, scenario.Name, step.Name, "started", ""); failure != nil {
			result.Failure = failure
			break
		}
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout(runContext.Config))
		failure := step.Run(stepCtx, runContext)
		ctxErr := stepCtx.Err()
		cancel()
		if ctxErr != nil && failure == nil {
			failure = &Failure{
				Class:        FailureTimeout,
				Message:      ctxErr.Error(),
				LastObserved: runContext.LastObserved(),
			}
		}
		if failure != nil {
			normalizeFailure(failure, scenario.Name, step.Name, runContext.LastObserved())
			result.Failure = failure
			_ = r.appendAction(runContext, scenario.Name, step.Name, "failed", failure.Message)
			break
		}
		if evidenceFailure := r.appendAction(runContext, scenario.Name, step.Name, "passed", ""); evidenceFailure != nil {
			result.Failure = evidenceFailure
			break
		}
	}

	for i := len(scenario.Cleanup) - 1; i >= 0; i-- {
		cleanup := scenario.Cleanup[i]
		cleanupCtx, cancel := context.WithTimeout(context.Background(), stepTimeout(runContext.Config))
		failure := cleanup.Run(cleanupCtx, runContext)
		ctxErr := cleanupCtx.Err()
		cancel()
		if ctxErr != nil && failure == nil {
			failure = &Failure{Message: ctxErr.Error(), LastObserved: runContext.LastObserved()}
		}
		if failure != nil {
			failure.Class = FailureCleanup
			normalizeFailure(failure, scenario.Name, cleanup.Name, runContext.LastObserved())
			result.CleanupFailures = append(result.CleanupFailures, *failure)
			_ = r.appendAction(runContext, scenario.Name, cleanup.Name, "cleanup_failed", failure.Message)
			continue
		}
		if evidenceFailure := r.appendAction(runContext, scenario.Name, cleanup.Name, "cleanup_passed", ""); evidenceFailure != nil {
			evidenceFailure.Class = FailureCleanup
			result.CleanupFailures = append(result.CleanupFailures, *evidenceFailure)
		}
	}

	if result.Failure != nil || len(result.CleanupFailures) > 0 {
		result.Status = "failed"
	}
	result.FinishedAt = r.now()
	if err := runContext.Evidence.Finish(result); err != nil && result.Failure == nil {
		result.Status = "failed"
		result.Failure = &Failure{
			Class:        FailureEnvironment,
			Scenario:     scenario.Name,
			Step:         "write_result",
			LastObserved: runContext.LastObserved(),
			Message:      fmt.Sprintf("write result evidence: %v", err),
		}
	}
	return result
}

func (r *Runner) appendAction(runContext *RunContext, scenario, step, state, detail string) *Failure {
	err := runContext.Evidence.AppendAction(ActionRecord{
		Time:         r.now(),
		Scenario:     scenario,
		Step:         step,
		State:        state,
		Detail:       detail,
		LastObserved: runContext.LastObserved(),
	})
	if err == nil {
		return nil
	}
	return &Failure{
		Class:        FailureEnvironment,
		Scenario:     scenario,
		Step:         step,
		LastObserved: runContext.LastObserved(),
		Message:      fmt.Sprintf("write action evidence: %v", err),
	}
}

func normalizeFailure(failure *Failure, scenario, step, lastObserved string) {
	failure.Scenario = scenario
	failure.Step = step
	if failure.LastObserved == "" {
		failure.LastObserved = lastObserved
	}
	if failure.Class == "" {
		failure.Class = FailureHarness
	}
}

func stepTimeout(config Config) time.Duration {
	if config.StepTimeoutMS <= 0 {
		return 12 * time.Second
	}
	return time.Duration(config.StepTimeoutMS) * time.Millisecond
}
