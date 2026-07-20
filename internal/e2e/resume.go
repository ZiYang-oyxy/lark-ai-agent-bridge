package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

const resumeSeedKey = "resume_seed"

var resumeSeedPattern = regexp.MustCompile(`^[A-Za-z0-9_]{8,32}$`)

func ResumeScenario() Scenario {
	steps := []Step{
		{Name: "initialize", Run: initializeResumeScenario},
		{Name: "select_agent", Run: selectResumeAgent},
	}
	for i := 0; i < 11; i++ {
		index := i
		steps = append(steps, Step{Name: fmt.Sprintf("create_session_%02d", index), Run: func(ctx context.Context, rc *RunContext) *Failure {
			return createResumeSession(ctx, rc, index)
		}})
	}
	steps = append(steps,
		Step{Name: "list_recent_sessions", Run: assertRecentResumeSessions},
		Step{Name: "select_session", Run: selectResumeTarget},
		Step{Name: "restart_candidate", Run: restartResumeCandidate},
		Step{Name: "list_after_restart", Run: assertResumeAfterRestart},
		Step{Name: "continue_resumed_session", Run: continueResumedSession},
		Step{Name: "reject_invalid_session", Run: rejectInvalidResume},
		Step{Name: "start_busy_task", Run: startBusyResumeTask},
		Step{Name: "reject_resume_while_busy", Run: rejectBusyResume},
		Step{Name: "stop_busy_task", Run: stopBusyResumeTask},
		Step{Name: "assert_binding_unchanged", Run: assertResumeBindingUnchanged},
	)
	return Scenario{
		Name:  "resume",
		Steps: steps,
		Cleanup: []Cleanup{
			{Name: "stop_residual_task", Run: cleanupResumeTask},
			{Name: "disarm_agent_fixture", Run: cleanupAgentFixture},
		},
	}
}

func initializeResumeScenario(_ context.Context, rc *RunContext) *Failure {
	if rc == nil || rc.Drivers.Messenger == nil || rc.Drivers.Replies == nil || rc.Drivers.Audit == nil || rc.Drivers.AgentFixture == nil || rc.Drivers.Lifecycle == nil {
		return &Failure{Class: FailureHarness, Message: "resume scenario drivers are incomplete"}
	}
	seed := rc.Value(resumeSeedKey)
	if seed == "" {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return &Failure{Class: FailureHarness, Message: fmt.Sprintf("generate resume seed: %v", err)}
		}
		seed = hex.EncodeToString(raw[:])
		rc.SetValue(resumeSeedKey, seed)
	}
	if !resumeSeedPattern.MatchString(seed) {
		return &Failure{Class: FailureHarness, Message: "resume seed has an unsafe format"}
	}
	return nil
}

func selectResumeAgent(ctx context.Context, rc *RunContext) *Failure {
	reply, failure := sendAndWaitReply(ctx, rc, "/agent-mode claude")
	if failure != nil {
		return failure
	}
	return recordReply(rc, reply)
}

func createResumeSession(ctx context.Context, rc *RunContext, index int) *Failure {
	plan := resumePlan(rc, fmt.Sprintf("session_%02d", index), fmt.Sprintf("建立可恢复会话 %02d", index), false)
	if failure := armAgentPlan(ctx, rc, plan); failure != nil {
		return failure
	}
	reply, failure := sendFixtureAndWaitCompleted(ctx, rc, plan.Prompt)
	if failure != nil {
		return failure
	}
	if failure := assertReplyContains(rc, reply, plan.Text, "card_contains_fixture_text"); failure != nil {
		return failure
	}
	if failure := collectInvocation(ctx, rc, plan.RunID, "started"); failure != nil {
		return failure
	}
	if failure := collectInvocation(ctx, rc, plan.RunID, "finished"); failure != nil {
		return failure
	}
	rc.SetValue(fmt.Sprintf("resume_session_%02d", index), plan.SessionID)
	rc.SetValue("agent_fixture_armed", "")
	return nil
}

func assertRecentResumeSessions(ctx context.Context, rc *RunContext) *Failure {
	reply, failure := sendAndWaitReply(ctx, rc, "/resume")
	if failure != nil {
		return failure
	}
	if failure := recordReply(rc, reply); failure != nil {
		return failure
	}
	raw := string(reply.Raw)
	last := -1
	for i := 10; i >= 1; i-- {
		id := rc.Value(fmt.Sprintf("resume_session_%02d", i))
		position := strings.Index(raw, id)
		if position < 0 || position <= last {
			return &Failure{Class: FailureAssertion, Assertion: "recent_10_order", Expected: "sessions 10..1 in descending recency", Actual: truncate(raw), Message: "resume list is missing or misorders a recent session"}
		}
		last = position
	}
	oldest := rc.Value("resume_session_00")
	if strings.Contains(raw, oldest) {
		return &Failure{Class: FailureAssertion, Assertion: "recent_10_limit", Expected: "oldest session omitted", Actual: oldest, Message: "resume list contains more than the newest ten sessions"}
	}
	return nil
}

func selectResumeTarget(ctx context.Context, rc *RunContext) *Failure {
	target := rc.Value("resume_session_05")
	rc.SetValue("resume_target", target)
	reply, failure := sendAndWaitReply(ctx, rc, "/resume "+target)
	if failure != nil {
		return failure
	}
	return assertReplyContains(rc, reply, "已恢复 Session `"+target+"`", "card_confirms_resume")
}

func restartResumeCandidate(ctx context.Context, rc *RunContext) *Failure {
	pid, failure := rc.Drivers.Lifecycle.RestartCandidate(ctx)
	if failure != nil {
		return failure
	}
	if pid <= 0 || pid == rc.Deployment.CandidatePID {
		return &Failure{Class: FailureProduct, Assertion: "candidate_pid_changed", Expected: "new positive PID", Actual: fmt.Sprint(pid), Message: "candidate restart did not replace the process"}
	}
	rc.Deployment.CandidatePID = pid
	rc.SetLastObserved(fmt.Sprintf("candidate_pid=%d", pid))
	return nil
}

func assertResumeAfterRestart(ctx context.Context, rc *RunContext) *Failure {
	reply, failure := sendAndWaitReply(ctx, rc, "/resume")
	if failure != nil {
		return failure
	}
	if failure := assertReplyContains(rc, reply, rc.Value("resume_target"), "catalog_survives_restart"); failure != nil {
		return failure
	}
	return assertReplyContains(rc, reply, "当前", "binding_survives_restart")
}

func continueResumedSession(ctx context.Context, rc *RunContext) *Failure {
	plan := resumePlan(rc, "continue", "继续", false)
	plan.SessionID = rc.Value("resume_target")
	return invokeAndAssertResume(ctx, rc, plan)
}

func rejectInvalidResume(ctx context.Context, rc *RunContext) *Failure {
	plan := resumePlan(rc, "invalid", "/resume", false)
	plan.Prompt = "/resume " + plan.Nonce
	if failure := armAgentPlan(ctx, rc, plan); failure != nil {
		return failure
	}
	reply, failure := sendAndWaitReply(ctx, rc, plan.Prompt)
	if failure != nil {
		return failure
	}
	if failure := assertReplyContains(rc, reply, "找不到该 Session", "invalid_session_rejected"); failure != nil {
		return failure
	}
	return assertPlanNotInvoked(ctx, rc, plan.RunID, "invalid command reached backend")
}

func startBusyResumeTask(ctx context.Context, rc *RunContext) *Failure {
	plan := resumePlan(rc, "busy", "保持运行", true)
	plan.SessionID = rc.Value("resume_target")
	if failure := armAgentPlan(ctx, rc, plan); failure != nil {
		return failure
	}
	reply, failure := sendAndWaitReply(ctx, rc, plan.Prompt)
	if failure != nil {
		return failure
	}
	if failure := assertReplyContains(rc, reply, plan.Text, "busy_fixture_started"); failure != nil {
		return failure
	}
	if failure := collectInvocation(ctx, rc, plan.RunID, "started"); failure != nil {
		return failure
	}
	invocation := rc.Value("last_invocation_argv")
	if !argvTextHasResume(invocation, rc.Value("resume_target")) {
		return resumeArgFailure(rc.Value("resume_target"), invocation)
	}
	rc.SetValue("busy_run_id", plan.RunID)
	rc.SetValue("busy_active", "true")
	rc.SetValue("agent_fixture_armed", "")
	return nil
}

func rejectBusyResume(ctx context.Context, rc *RunContext) *Failure {
	other := rc.Value("resume_session_06")
	plan := AgentFixturePlan{SchemaVersion: 1, RunID: fixtureRunID(rc, "busy_reject"), Nonce: other, Prompt: "/resume " + other, SessionID: other, Text: "UNEXPECTED_BACKEND", Block: false}
	if failure := armAgentPlan(ctx, rc, plan); failure != nil {
		return failure
	}
	reply, failure := sendAndWaitReply(ctx, rc, plan.Prompt)
	if failure != nil {
		return failure
	}
	if failure := assertReplyContains(rc, reply, "仍有正在执行或排队的任务", "busy_resume_rejected"); failure != nil {
		return failure
	}
	return assertPlanNotInvoked(ctx, rc, plan.RunID, "busy resume command reached backend")
}

func stopBusyResumeTask(ctx context.Context, rc *RunContext) *Failure {
	reply, failure := sendAndWaitReply(ctx, rc, "/stop")
	if failure != nil {
		return failure
	}
	if failure := assertReplyContains(rc, reply, "已请求停止当前任务", "busy_stop_ack"); failure != nil {
		return failure
	}
	if failure := collectInvocation(ctx, rc, rc.Value("busy_run_id"), "cancelled"); failure != nil {
		return failure
	}
	rc.SetValue("busy_active", "")
	return nil
}

func assertResumeBindingUnchanged(ctx context.Context, rc *RunContext) *Failure {
	plan := resumePlan(rc, "after_busy", "停止后继续", false)
	plan.SessionID = rc.Value("resume_target")
	return invokeAndAssertResume(ctx, rc, plan)
}

func invokeAndAssertResume(ctx context.Context, rc *RunContext, plan AgentFixturePlan) *Failure {
	if failure := armAgentPlan(ctx, rc, plan); failure != nil {
		return failure
	}
	reply, failure := sendFixtureAndWaitCompleted(ctx, rc, plan.Prompt)
	if failure != nil {
		return failure
	}
	if failure := assertReplyContains(rc, reply, plan.Text, "continuation_visible"); failure != nil {
		return failure
	}
	if failure := collectInvocation(ctx, rc, plan.RunID, "started"); failure != nil {
		return failure
	}
	argv := rc.Value("last_invocation_argv")
	if !argvTextHasResume(argv, rc.Value("resume_target")) {
		return resumeArgFailure(rc.Value("resume_target"), argv)
	}
	if failure := collectInvocation(ctx, rc, plan.RunID, "finished"); failure != nil {
		return failure
	}
	rc.SetValue("agent_fixture_armed", "")
	return nil
}

func armAgentPlan(ctx context.Context, rc *RunContext, plan AgentFixturePlan) *Failure {
	if failure := rc.Drivers.AgentFixture.ArmAgent(ctx, plan); failure != nil {
		return failure
	}
	rc.SetValue("agent_fixture_armed", "true")
	rc.SetLastObserved("agent_fixture_run=" + plan.RunID)
	return nil
}

func collectInvocation(ctx context.Context, rc *RunContext, runID, event string) *Failure {
	invocation, failure := rc.Drivers.AgentFixture.WaitAgentInvocation(ctx, runID, event)
	if failure != nil {
		return failure
	}
	if len(invocation.Raw) == 0 {
		return &Failure{Class: FailureHarness, Message: "fixture invocation has no raw evidence"}
	}
	if err := rc.Evidence.AppendInvocation(invocation.Raw); err != nil {
		return &Failure{Class: FailureEnvironment, Message: fmt.Sprintf("write fixture invocation evidence: %v", err)}
	}
	rc.SetValue("last_invocation_argv", strings.Join(invocation.Argv, "\x00"))
	rc.SetLastObserved("fixture_invocation=" + runID + " event=" + event)
	return nil
}

func assertPlanNotInvoked(ctx context.Context, rc *RunContext, runID, message string) *Failure {
	invoked, failure := rc.Drivers.AgentFixture.HasAgentInvocation(ctx, runID)
	if failure != nil {
		return failure
	}
	if disarm := rc.Drivers.AgentFixture.DisarmAgent(ctx); disarm != nil {
		return disarm
	}
	rc.SetValue("agent_fixture_armed", "")
	if invoked {
		return &Failure{Class: FailureProduct, Assertion: "command_does_not_invoke_backend", Expected: "no fixture invocation", Actual: runID, Message: message}
	}
	return nil
}

func cleanupResumeTask(ctx context.Context, rc *RunContext) *Failure {
	if rc == nil || rc.Drivers.Messenger == nil || rc.Value("busy_active") != "true" {
		return nil
	}
	_, failure := rc.Drivers.Messenger.SendText(ctx, "/stop")
	return failure
}

func cleanupAgentFixture(ctx context.Context, rc *RunContext) *Failure {
	if rc == nil || rc.Drivers.AgentFixture == nil || rc.Value("agent_fixture_armed") != "true" {
		return nil
	}
	return rc.Drivers.AgentFixture.DisarmAgent(ctx)
}

func resumePlan(rc *RunContext, kind, prefix string, block bool) AgentFixturePlan {
	nonce := "E2E_AGENT_FIXTURE_" + kind + "_" + rc.Value(resumeSeedKey)
	return AgentFixturePlan{
		SchemaVersion: 1,
		RunID:         fixtureRunID(rc, kind),
		Nonce:         nonce,
		Prompt:        prefix + " " + nonce,
		SessionID:     "E2E_AGENT_FIXTURE_session_" + rc.Value(resumeSeedKey) + "_" + kind,
		Text:          "READY_" + nonce,
		Block:         block,
	}
}

func fixtureRunID(rc *RunContext, kind string) string {
	return "e2e_resume_" + rc.Value(resumeSeedKey) + "_" + kind
}

func sendAndWaitReply(ctx context.Context, rc *RunContext, body string) (Reply, *Failure) {
	mark, failure := rc.Drivers.Audit.Mark(ctx)
	if failure != nil {
		return Reply{}, failure
	}
	source, failure := rc.Drivers.Messenger.SendText(ctx, body)
	if failure != nil {
		return Reply{}, failure
	}
	rc.SetLastObserved("source=" + source)
	reply, failure := rc.Drivers.Replies.WaitReply(ctx, mark, source)
	if failure != nil {
		return Reply{}, failure
	}
	if failure := recordReply(rc, reply); failure != nil {
		return Reply{}, failure
	}
	return reply, nil
}

func sendFixtureAndWaitCompleted(ctx context.Context, rc *RunContext, body string) (Reply, *Failure) {
	mark, failure := rc.Drivers.Audit.Mark(ctx)
	if failure != nil {
		return Reply{}, failure
	}
	source, failure := rc.Drivers.Messenger.SendText(ctx, body)
	if failure != nil {
		return Reply{}, failure
	}
	rc.SetLastObserved("source=" + source)
	event, failure := rc.Drivers.Audit.Wait(ctx, mark, AuditMatch{Action: "cardkit_update", Source: source, Detail: "event=completed"})
	if failure != nil {
		return Reply{}, failure
	}
	if failure := recordAudit(rc, event); failure != nil {
		return Reply{}, failure
	}
	reply, failure := rc.Drivers.Replies.WaitReply(ctx, mark, source)
	if failure != nil {
		return Reply{}, failure
	}
	if failure := recordReply(rc, reply); failure != nil {
		return Reply{}, failure
	}
	return reply, nil
}

func assertReplyContains(rc *RunContext, reply Reply, expected, assertion string) *Failure {
	ok, actual, failure := rc.Drivers.Replies.ContainsVisibleText(reply, expected)
	if failure != nil {
		return failure
	}
	rc.SetLastObserved(actual)
	if !ok {
		return &Failure{Class: FailureAssertion, Assertion: assertion, Expected: expected, Actual: actual, Message: "expected visible card text is missing"}
	}
	return nil
}

func argvTextHasResume(joined, sessionID string) bool {
	args := strings.Split(joined, "\x00")
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--resume" && args[i+1] == sessionID {
			return true
		}
	}
	return false
}

func resumeArgFailure(expected, actual string) *Failure {
	return &Failure{Class: FailureProduct, Assertion: "fixture_argv_contains_resume", Expected: "--resume " + expected, Actual: strings.ReplaceAll(actual, "\x00", " "), Message: "continuation did not target the selected session"}
}

func truncate(value string) string {
	if len(value) > 4000 {
		return value[:4000]
	}
	return value
}
