package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestStopScenarioAssertsUserVisibleBehavior(t *testing.T) {
	drivers := passingStopDrivers()
	result := runStopWithFake(t, drivers)
	if result.Status != "passed" {
		t.Fatalf("result = %#v", result)
	}
	if strings.Join(drivers.auditActions, ",") != "batch_stop_requested,cardkit_update" {
		t.Fatalf("audit actions = %#v", drivers.auditActions)
	}
	for _, action := range drivers.auditActions {
		if action == "cardkit_text_stream" {
			t.Fatal("scenario bound to transport action")
		}
	}
	if got := strings.Join(drivers.sent, ","); got != "/agent-mode claude,E2E_STOP_FIXTURE_TESTNONCE,/stop" {
		t.Fatalf("sent = %q", got)
	}
}

func TestStopScenarioClassifiesEachFailure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeStopDrivers)
		class  FailureClass
		step   string
	}{
		{"reply timeout", func(d *fakeStopDrivers) { d.replyFailureAt = "start-source" }, FailureTimeout, "wait_ready_reply"},
		{"ready missing", func(d *fakeStopDrivers) {
			d.replies["start-source"] = Reply{MessageID: "start-reply", Raw: []byte(`{"text":"starting"}`)}
		}, FailureAssertion, "assert_ready"},
		{"stop ack missing", func(d *fakeStopDrivers) {
			d.replies["stop-source"] = Reply{MessageID: "stop-reply", Raw: []byte(`{"text":"stopping"}`)}
		}, FailureAssertion, "assert_stop_ack"},
		{"audit missing", func(d *fakeStopDrivers) { d.auditFailureAt = "batch_stop_requested" }, FailureTimeout, "wait_stop_audit"},
		{"terminal missing", func(d *fakeStopDrivers) { d.auditFailureAt = "cardkit_update" }, FailureTimeout, "wait_stopped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drivers := passingStopDrivers()
			tt.mutate(drivers)
			result := runStopWithFake(t, drivers)
			if result.Failure == nil || result.Failure.Class != tt.class || result.Failure.Step != tt.step {
				t.Fatalf("failure = %#v", result.Failure)
			}
			if result.Failure.Scenario != "stop" || result.Failure.LastObserved == "" {
				t.Fatalf("incomplete failure = %#v", result.Failure)
			}
		})
	}
}

func TestStopScenarioReportsCleanupSeparately(t *testing.T) {
	drivers := passingStopDrivers()
	drivers.disarmErr = errors.New("permission denied")
	result := runStopWithFake(t, drivers)
	if result.Failure != nil {
		t.Fatalf("primary failure = %#v", result.Failure)
	}
	if len(result.CleanupFailures) != 1 || result.CleanupFailures[0].Class != FailureCleanup || result.CleanupFailures[0].Step != "disarm_fixture" {
		t.Fatalf("cleanup failures = %#v", result.CleanupFailures)
	}
}

func TestStopScenarioAttemptsResidualStopWhenStartFails(t *testing.T) {
	drivers := passingStopDrivers()
	drivers.replyFailureAt = "start-source"
	result := runStopWithFake(t, drivers)
	if result.Failure == nil {
		t.Fatal("scenario passed")
	}
	if got := drivers.sent[len(drivers.sent)-1]; got != "/stop" {
		t.Fatalf("last cleanup message = %q", got)
	}
}

func TestStopScenarioRejectsUnsafePresetNonceBeforeFixture(t *testing.T) {
	drivers := passingStopDrivers()
	runContext := newRunnerContext(t)
	runContext.Drivers = drivers.driverSet()
	runContext.SetValue(stopNonceKey, `E2E_STOP_FIXTURE_'_unsafe`)
	result := NewRunner().Run(context.Background(), StopScenario(), runContext)
	if result.Failure == nil || result.Failure.Class != FailureHarness || result.Failure.Step != "arm_fixture" {
		t.Fatalf("failure = %#v", result.Failure)
	}
	if drivers.armedNonce != "" {
		t.Fatalf("armed unsafe nonce %q", drivers.armedNonce)
	}
}

func runStopWithFake(t *testing.T, drivers *fakeStopDrivers) Result {
	t.Helper()
	runContext := newRunnerContext(t)
	runContext.Drivers = drivers.driverSet()
	runContext.SetValue(stopNonceKey, "E2E_STOP_FIXTURE_TESTNONCE")
	return NewRunner().Run(context.Background(), StopScenario(), runContext)
}

type fakeStopDrivers struct {
	sent           []string
	sources        map[string]string
	replies        map[string]Reply
	replyFailureAt string
	auditFailureAt string
	auditActions   []string
	armedNonce     string
	disarmed       bool
	disarmErr      error
	mark           int64
}

func passingStopDrivers() *fakeStopDrivers {
	return &fakeStopDrivers{
		sources: map[string]string{
			"/agent-mode claude":         "agent-source",
			"E2E_STOP_FIXTURE_TESTNONCE": "start-source",
			"/stop":                      "stop-source",
		},
		replies: map[string]Reply{
			"agent-source": {MessageID: "agent-reply", Raw: []byte(`{"text":"Claude"}`)},
			"start-source": {MessageID: "start-reply", Raw: []byte(`{"content":"READY_E2E_STOP_FIXTURE_TESTNONCE"}`)},
			"stop-source":  {MessageID: "stop-reply", Raw: []byte(`{"content":"已请求停止当前任务"}`)},
		},
	}
}

func (d *fakeStopDrivers) driverSet() Drivers {
	return Drivers{Messenger: d, Replies: d, Audit: d, Fixture: d}
}

func (d *fakeStopDrivers) SendText(_ context.Context, body string) (string, *Failure) {
	d.sent = append(d.sent, body)
	source := d.sources[body]
	if source == "" {
		return "", &Failure{Class: FailureHarness, Message: "unexpected message " + body}
	}
	return source, nil
}

func (d *fakeStopDrivers) WaitReply(_ context.Context, _ int64, source string) (Reply, *Failure) {
	if source == d.replyFailureAt {
		return Reply{}, &Failure{Class: FailureTimeout, Message: "reply timeout", LastObserved: "source=" + source}
	}
	reply, ok := d.replies[source]
	if !ok {
		return Reply{}, &Failure{Class: FailureTimeout, Message: "reply missing", LastObserved: "source=" + source}
	}
	return reply, nil
}

func (d *fakeStopDrivers) ContainsVisibleText(reply Reply, expected string) (bool, string, *Failure) {
	var decoded any
	if err := json.Unmarshal(reply.Raw, &decoded); err != nil {
		return false, "", &Failure{Class: FailureHarness, Message: err.Error()}
	}
	actual := fmt.Sprint(decoded)
	return strings.Contains(string(reply.Raw), expected), actual, nil
}

func (d *fakeStopDrivers) Mark(context.Context) (int64, *Failure) {
	d.mark++
	return d.mark, nil
}

func (d *fakeStopDrivers) Wait(_ context.Context, _ int64, match AuditMatch) (AuditEvent, *Failure) {
	d.auditActions = append(d.auditActions, match.Action)
	last := "action=" + match.Action
	if match.Detail != "" {
		last += " detail=" + match.Detail
	}
	if match.Action == d.auditFailureAt {
		return AuditEvent{}, &Failure{Class: FailureTimeout, Message: "audit timeout", LastObserved: last}
	}
	event := AuditEvent{Action: match.Action, SessionID: match.Source, Detail: match.Detail}
	event.Raw, _ = json.Marshal(event)
	return event, nil
}

func (d *fakeStopDrivers) Arm(_ context.Context, nonce string) *Failure {
	d.armedNonce = nonce
	return nil
}

func (d *fakeStopDrivers) Disarm(context.Context) *Failure {
	d.disarmed = true
	if d.disarmErr != nil {
		return &Failure{Class: FailureEnvironment, Message: d.disarmErr.Error()}
	}
	return nil
}
