package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const realContextPromptPrefix = "/new 不要调用工具，只回复 "

func RealContextScenario() Scenario {
	return Scenario{
		Name: "real_context",
		Steps: []Step{
			{Name: "initialize", Run: initializeRealContext},
			{Name: "select_claude", Run: selectRealClaude},
			{Name: "run_claude", Run: runRealClaude},
			{Name: "assert_claude", Run: assertRealClaude},
			{Name: "select_codex", Run: selectRealCodex},
			{Name: "run_codex", Run: runRealCodex},
			{Name: "assert_codex", Run: assertRealCodex},
		},
		Cleanup: []Cleanup{{Name: "stop_residual_task", Run: cleanupRealContext}},
	}
}

func initializeRealContext(_ context.Context, rc *RunContext) *Failure {
	if rc == nil || rc.Drivers.Messenger == nil || rc.Drivers.Replies == nil || rc.Drivers.Audit == nil {
		return &Failure{Class: FailureHarness, Message: "real context scenario drivers are incomplete"}
	}
	for _, agent := range []string{"claude", "codex"} {
		key := agent + "_marker"
		if rc.Value(key) != "" {
			continue
		}
		marker, err := newRealContextMarker(agent)
		if err != nil {
			return &Failure{Class: FailureHarness, Message: fmt.Sprintf("generate %s marker: %v", agent, err)}
		}
		rc.SetValue(key, marker)
	}
	return nil
}

func selectRealClaude(ctx context.Context, rc *RunContext) *Failure {
	return selectRealContextAgent(ctx, rc, "claude")
}

func selectRealCodex(ctx context.Context, rc *RunContext) *Failure {
	return selectRealContextAgent(ctx, rc, "codex")
}

func selectRealContextAgent(ctx context.Context, rc *RunContext, agent string) *Failure {
	reply, failure := sendAndWaitReply(ctx, rc, "/agent-mode "+agent)
	if failure != nil {
		return failure
	}
	return recordReply(rc, reply)
}

func runRealClaude(ctx context.Context, rc *RunContext) *Failure {
	return runRealContextAgent(ctx, rc, "claude")
}

func runRealCodex(ctx context.Context, rc *RunContext) *Failure {
	return runRealContextAgent(ctx, rc, "codex")
}

func runRealContextAgent(ctx context.Context, rc *RunContext, agent string) *Failure {
	mark, failure := rc.Drivers.Audit.Mark(ctx)
	if failure != nil {
		return failure
	}
	source, failure := rc.Drivers.Messenger.SendText(ctx, realContextPromptPrefix+rc.Value(agent+"_marker"))
	if failure != nil {
		return failure
	}
	rc.SetValue(agent+"_mark", fmt.Sprint(mark))
	rc.SetValue(agent+"_source", source)
	rc.SetLastObserved(agent + "_source=" + source)
	event, failure := rc.Drivers.Audit.Wait(ctx, mark, AuditMatch{Action: "cardkit_update", Source: source, Detail: "event=result"})
	if failure != nil {
		return failure
	}
	if failure := recordAudit(rc, event); failure != nil {
		return failure
	}
	rc.SetValue(agent+"_terminal", "true")
	reply, failure := rc.Drivers.Replies.WaitReply(ctx, mark, source)
	if failure != nil {
		return failure
	}
	if failure := recordReply(rc, reply); failure != nil {
		return failure
	}
	rc.SetValue(agent+"_reply_id", reply.MessageID)
	rc.SetValue(agent+"_reply_raw", string(reply.Raw))
	rc.SetLastObserved(agent + "_reply=" + reply.MessageID)
	return nil
}

func assertRealClaude(_ context.Context, rc *RunContext) *Failure {
	return assertRealContextCard(rc, "claude", "🍊")
}

func assertRealCodex(_ context.Context, rc *RunContext) *Failure {
	return assertRealContextCard(rc, "codex", "⚙️")
}

func assertRealContextCard(rc *RunContext, agent, label string) *Failure {
	reply := Reply{MessageID: rc.Value(agent + "_reply_id"), Raw: []byte(rc.Value(agent + "_reply_raw"))}
	if failure := assertReplyContains(rc, reply, rc.Value(agent+"_marker"), agent+"_marker_visible"); failure != nil {
		return failure
	}
	if failure := assertReplyContains(rc, reply, label, agent+"_agent_visible"); failure != nil {
		return failure
	}
	var actual string
	for _, prefix := range []string{"🟢 ctx:", "🟡 ctx:", "🔴 ctx:"} {
		ok, visible, failure := rc.Drivers.Replies.ContainsVisibleText(reply, prefix)
		if failure != nil {
			return failure
		}
		actual = visible
		if ok {
			rc.SetLastObserved(visible)
			return nil
		}
	}
	rc.SetLastObserved(actual)
	return &Failure{
		Class:     FailureAssertion,
		Assertion: agent + "_context_visible",
		Expected:  "🟢 ctx: or 🟡 ctx: or 🔴 ctx:",
		Actual:    actual,
		Message:   "colored context usage is missing from the terminal card",
	}
}

func cleanupRealContext(ctx context.Context, rc *RunContext) *Failure {
	if rc == nil || rc.Drivers.Messenger == nil {
		return nil
	}
	for _, agent := range []string{"codex", "claude"} {
		if rc.Value(agent+"_source") != "" && rc.Value(agent+"_terminal") != "true" {
			_, failure := rc.Drivers.Messenger.SendText(ctx, "/stop")
			return failure
		}
	}
	return nil
}

func newRealContextMarker(agent string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "E2E_REAL_CONTEXT_" + agent + "_" + hex.EncodeToString(raw[:]), nil
}
