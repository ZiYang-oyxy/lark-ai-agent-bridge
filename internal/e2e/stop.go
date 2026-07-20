package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

const stopNonceKey = "stop_fixture_nonce"

var stopNoncePattern = regexp.MustCompile(`^E2E_STOP_FIXTURE_[A-Za-z0-9_]{8,96}$`)

func StopScenario() Scenario {
	return Scenario{
		Name: "stop",
		Steps: []Step{
			{Name: "select_agent", Run: selectClaudeAgent},
			{Name: "arm_fixture", Run: armStopFixture},
			{Name: "send_fixture", Run: sendFixtureMessage},
			{Name: "wait_ready_reply", Run: waitReadyReply},
			{Name: "assert_ready", Run: assertReady},
			{Name: "send_stop", Run: sendStop},
			{Name: "wait_stop_reply", Run: waitStopReply},
			{Name: "assert_stop_ack", Run: assertStopAcknowledgement},
			{Name: "wait_stop_audit", Run: waitStopAudit},
			{Name: "wait_stopped", Run: waitStopped},
		},
		Cleanup: []Cleanup{
			{Name: "stop_residual_task", Run: stopResidualTask},
			{Name: "disarm_fixture", Run: disarmStopFixture},
		},
	}
}

func selectClaudeAgent(ctx context.Context, rc *RunContext) *Failure {
	if failure := requireDrivers(rc); failure != nil {
		return failure
	}
	mark, failure := rc.Drivers.Audit.Mark(ctx)
	if failure != nil {
		return failure
	}
	source, failure := rc.Drivers.Messenger.SendText(ctx, "/agent-mode claude")
	if failure != nil {
		return failure
	}
	rc.SetLastObserved("agent_source=" + source)
	reply, failure := rc.Drivers.Replies.WaitReply(ctx, mark, source)
	if failure != nil {
		return failure
	}
	return recordReply(rc, reply)
}

func armStopFixture(ctx context.Context, rc *RunContext) *Failure {
	nonce := rc.Value(stopNonceKey)
	if nonce == "" {
		generated, err := newStopNonce()
		if err != nil {
			return &Failure{Class: FailureHarness, Message: fmt.Sprintf("generate fixture nonce: %v", err)}
		}
		nonce = generated
		rc.SetValue(stopNonceKey, nonce)
	}
	if !stopNoncePattern.MatchString(nonce) {
		return &Failure{Class: FailureHarness, Message: "fixture nonce has an unsafe format", LastObserved: "nonce rejected"}
	}
	if failure := rc.Drivers.Fixture.Arm(ctx, nonce); failure != nil {
		return failure
	}
	rc.SetValue("fixture_armed", "true")
	rc.SetLastObserved("fixture armed")
	return nil
}

func sendFixtureMessage(ctx context.Context, rc *RunContext) *Failure {
	mark, failure := rc.Drivers.Audit.Mark(ctx)
	if failure != nil {
		return failure
	}
	source, failure := rc.Drivers.Messenger.SendText(ctx, rc.Value(stopNonceKey))
	if failure != nil {
		return failure
	}
	rc.SetValue("start_mark", fmt.Sprint(mark))
	rc.SetValue("start_source", source)
	rc.SetLastObserved("start_source=" + source)
	return nil
}

func waitReadyReply(ctx context.Context, rc *RunContext) *Failure {
	reply, failure := rc.Drivers.Replies.WaitReply(ctx, mustInt64(rc.Value("start_mark")), rc.Value("start_source"))
	if failure != nil {
		return failure
	}
	rc.SetValue("start_reply", reply.MessageID)
	rc.SetValue("start_reply_raw", string(reply.Raw))
	rc.SetLastObserved("start_reply=" + reply.MessageID)
	return recordReply(rc, reply)
}

func assertReady(_ context.Context, rc *RunContext) *Failure {
	reply := Reply{MessageID: rc.Value("start_reply"), Raw: []byte(rc.Value("start_reply_raw"))}
	expected := "READY_" + rc.Value(stopNonceKey)
	ok, actual, failure := rc.Drivers.Replies.ContainsVisibleText(reply, expected)
	if failure != nil {
		return failure
	}
	rc.SetLastObserved(actual)
	if !ok {
		return &Failure{Class: FailureAssertion, Assertion: "card_contains_ready", Expected: expected, Actual: actual, Message: "fixture READY marker is not visible"}
	}
	return nil
}

func sendStop(ctx context.Context, rc *RunContext) *Failure {
	mark, failure := rc.Drivers.Audit.Mark(ctx)
	if failure != nil {
		return failure
	}
	source, failure := rc.Drivers.Messenger.SendText(ctx, "/stop")
	if failure != nil {
		return failure
	}
	rc.SetValue("stop_mark", fmt.Sprint(mark))
	rc.SetValue("stop_source", source)
	rc.SetValue("stop_sent", "true")
	rc.SetLastObserved("stop_source=" + source)
	return nil
}

func waitStopReply(ctx context.Context, rc *RunContext) *Failure {
	reply, failure := rc.Drivers.Replies.WaitReply(ctx, mustInt64(rc.Value("stop_mark")), rc.Value("stop_source"))
	if failure != nil {
		return failure
	}
	rc.SetValue("stop_reply", reply.MessageID)
	rc.SetValue("stop_reply_raw", string(reply.Raw))
	rc.SetLastObserved("stop_reply=" + reply.MessageID)
	return recordReply(rc, reply)
}

func assertStopAcknowledgement(_ context.Context, rc *RunContext) *Failure {
	reply := Reply{MessageID: rc.Value("stop_reply"), Raw: []byte(rc.Value("stop_reply_raw"))}
	const expected = "已请求停止当前任务"
	ok, actual, failure := rc.Drivers.Replies.ContainsVisibleText(reply, expected)
	if failure != nil {
		return failure
	}
	rc.SetLastObserved(actual)
	if !ok {
		return &Failure{Class: FailureAssertion, Assertion: "card_contains_stop_ack", Expected: expected, Actual: actual, Message: "stop acknowledgement is not visible"}
	}
	return nil
}

func waitStopAudit(ctx context.Context, rc *RunContext) *Failure {
	event, failure := rc.Drivers.Audit.Wait(ctx, mustInt64(rc.Value("start_mark")), AuditMatch{Action: "batch_stop_requested"})
	if failure != nil {
		return failure
	}
	return recordAudit(rc, event)
}

func waitStopped(ctx context.Context, rc *RunContext) *Failure {
	event, failure := rc.Drivers.Audit.Wait(ctx, mustInt64(rc.Value("start_mark")), AuditMatch{
		Action: "cardkit_update",
		Source: rc.Value("start_source"),
		Detail: "event=stopped",
	})
	if failure != nil {
		return failure
	}
	return recordAudit(rc, event)
}

func disarmStopFixture(ctx context.Context, rc *RunContext) *Failure {
	if rc.Drivers.Fixture == nil || rc.Value("fixture_armed") != "true" {
		return nil
	}
	return rc.Drivers.Fixture.Disarm(ctx)
}

func stopResidualTask(ctx context.Context, rc *RunContext) *Failure {
	if rc.Drivers.Messenger == nil || rc.Value("start_source") == "" || rc.Value("stop_sent") == "true" {
		return nil
	}
	_, failure := rc.Drivers.Messenger.SendText(ctx, "/stop")
	return failure
}

func requireDrivers(rc *RunContext) *Failure {
	if rc.Drivers.Messenger == nil || rc.Drivers.Replies == nil || rc.Drivers.Audit == nil || rc.Drivers.Fixture == nil {
		return &Failure{Class: FailureHarness, Message: "scenario drivers are incomplete"}
	}
	return nil
}

func recordReply(rc *RunContext, reply Reply) *Failure {
	if err := rc.Evidence.WriteCard(reply.MessageID, reply.Raw); err != nil {
		return &Failure{Class: FailureEnvironment, Message: fmt.Sprintf("write reply evidence: %v", err)}
	}
	return nil
}

func recordAudit(rc *RunContext, event AuditEvent) *Failure {
	raw := event.Raw
	if len(raw) == 0 {
		encoded, err := json.Marshal(event)
		if err != nil {
			return &Failure{Class: FailureHarness, Message: fmt.Sprintf("marshal audit event: %v", err)}
		}
		raw = encoded
	}
	if err := rc.Evidence.AppendAudit(raw); err != nil {
		return &Failure{Class: FailureEnvironment, Message: fmt.Sprintf("write audit evidence: %v", err)}
	}
	rc.SetLastObserved("audit_action=" + event.Action + " " + event.Detail)
	return nil
}

func newStopNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "E2E_STOP_FIXTURE_" + hex.EncodeToString(raw[:]), nil
}

func mustInt64(raw string) int64 {
	var value int64
	_, _ = fmt.Sscan(raw, &value)
	return value
}
