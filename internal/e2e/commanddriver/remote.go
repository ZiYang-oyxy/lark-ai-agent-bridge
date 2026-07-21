package commanddriver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"lark-agent-bridge/internal/e2e"
)

var fixtureNoncePattern = regexp.MustCompile(`^E2E_STOP_FIXTURE_[A-Za-z0-9_]{8,96}$`)
var agentRunPattern = regexp.MustCompile(`^e2e_[A-Za-z0-9_]{8,96}$`)
var agentNoncePattern = regexp.MustCompile(`^E2E_AGENT_FIXTURE_[A-Za-z0-9_]{8,96}$`)

func (d *Driver) Mark(ctx context.Context) (int64, *e2e.Failure) {
	output, failure := d.runRemote(ctx, "audit_mark", auditMarkScript, d.config.AuditPath)
	if failure != nil {
		return 0, failure
	}
	mark, err := strconv.ParseInt(strings.TrimSpace(string(output.Stdout)), 10, 64)
	if err != nil || mark < 0 {
		return 0, &e2e.Failure{Class: e2e.FailureHarness, Message: fmt.Sprintf("parse audit mark: %v", err)}
	}
	return mark, nil
}

func (d *Driver) Wait(ctx context.Context, mark int64, match e2e.AuditMatch) (e2e.AuditEvent, *e2e.Failure) {
	output, failure := d.runRemote(ctx, "wait_audit", auditWaitScript,
		d.config.AuditPath,
		fmt.Sprint(mark),
		match.Action,
		match.Source,
		match.Detail,
		pollSeconds(d.config.PollIntervalMS),
		fmt.Sprint(d.config.StepTimeoutMS),
	)
	if failure != nil {
		return e2e.AuditEvent{}, failure
	}
	raw := strings.TrimSpace(string(output.Stdout))
	if !json.Valid([]byte(raw)) {
		return e2e.AuditEvent{}, &e2e.Failure{Class: e2e.FailureHarness, Message: "audit wait returned malformed JSON"}
	}
	var wire struct {
		Action       string `json:"Action"`
		ActionLower  string `json:"action"`
		SessionID    string `json:"SessionID"`
		SessionLower string `json:"session_id"`
		Detail       string `json:"Detail"`
		DetailLower  string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return e2e.AuditEvent{}, &e2e.Failure{Class: e2e.FailureHarness, Message: fmt.Sprintf("decode audit event: %v", err)}
	}
	event := e2e.AuditEvent{
		Action:    firstNonEmpty(wire.Action, wire.ActionLower),
		SessionID: firstNonEmpty(wire.SessionID, wire.SessionLower),
		Detail:    firstNonEmpty(wire.Detail, wire.DetailLower),
		Raw:       json.RawMessage(append([]byte(nil), []byte(raw)...)),
	}
	if event.Action == "" {
		return e2e.AuditEvent{}, &e2e.Failure{Class: e2e.FailureHarness, Message: "audit event has no action"}
	}
	return event, nil
}

func (d *Driver) Arm(ctx context.Context, nonce string) *e2e.Failure {
	if !fixtureNoncePattern.MatchString(nonce) {
		return &e2e.Failure{Class: e2e.FailureHarness, Message: "fixture nonce has an unsafe format"}
	}
	_, failure := d.runRemote(ctx, "arm_fixture", armFixtureScript, d.deployment.StateDir, nonce)
	return failure
}

func (d *Driver) Disarm(ctx context.Context) *e2e.Failure {
	_, failure := d.runRemote(ctx, "disarm_fixture", disarmFixtureScript, d.deployment.StateDir)
	return failure
}

func (d *Driver) ArmAgent(ctx context.Context, plan e2e.AgentFixturePlan) *e2e.Failure {
	if failure := validateAgentPlan(plan); failure != nil {
		return failure
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return &e2e.Failure{Class: e2e.FailureHarness, Message: fmt.Sprintf("encode agent fixture plan: %v", err)}
	}
	_, failure := d.runRemote(ctx, "arm_agent_fixture", armAgentFixtureScript, d.deployment.StateDir, string(raw))
	return failure
}

func (d *Driver) WaitAgentInvocation(ctx context.Context, runID, event string) (e2e.AgentInvocation, *e2e.Failure) {
	if !agentRunPattern.MatchString(runID) || (event != "started" && event != "finished" && event != "cancelled") {
		return e2e.AgentInvocation{}, &e2e.Failure{Class: e2e.FailureHarness, Message: "unsafe fixture invocation selector"}
	}
	output, failure := d.runRemote(ctx, "wait_agent_invocation", waitAgentInvocationScript,
		d.deployment.StateDir, runID, event, pollSeconds(d.config.PollIntervalMS), fmt.Sprint(d.config.StepTimeoutMS))
	if failure != nil {
		return e2e.AgentInvocation{}, failure
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(output.Stdout)))
	decoder.DisallowUnknownFields()
	var invocation e2e.AgentInvocation
	if err := decoder.Decode(&invocation); err != nil {
		return e2e.AgentInvocation{}, &e2e.Failure{Class: e2e.FailureHarness, Message: fmt.Sprintf("decode fixture invocation: %v", err)}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || invocation.SchemaVersion != 1 || invocation.RunID != runID || invocation.Event != event ||
		strings.TrimSpace(invocation.SessionID) == "" || (event == "started" && len(invocation.Argv) == 0) || (event == "cancelled" && !invocation.Cancelled) {
		return e2e.AgentInvocation{}, &e2e.Failure{Class: e2e.FailureHarness, Message: "fixture invocation violates the protocol"}
	}
	invocation.Raw = json.RawMessage(append([]byte(nil), bytes.TrimSpace(output.Stdout)...))
	return invocation, nil
}

func (d *Driver) DisarmAgent(ctx context.Context) *e2e.Failure {
	_, failure := d.runRemote(ctx, "disarm_agent_fixture", disarmAgentFixtureScript, d.deployment.StateDir)
	return failure
}

func (d *Driver) HasAgentInvocation(ctx context.Context, runID string) (bool, *e2e.Failure) {
	if !agentRunPattern.MatchString(runID) {
		return false, &e2e.Failure{Class: e2e.FailureHarness, Message: "unsafe fixture invocation selector"}
	}
	output, failure := d.runRemote(ctx, "find_agent_invocation", findAgentInvocationScript, d.deployment.StateDir, runID)
	if failure != nil {
		return false, failure
	}
	value := strings.TrimSpace(string(output.Stdout))
	if value == "true" {
		return true, nil
	}
	if value == "false" {
		return false, nil
	}
	return false, &e2e.Failure{Class: e2e.FailureHarness, Message: "fixture invocation lookup returned invalid output"}
}

func (d *Driver) RestartCandidate(ctx context.Context) (int, *e2e.Failure) {
	if d.config.ControllerPath == "" {
		return 0, &e2e.Failure{Class: e2e.FailureHarness, Message: "controller_path is required for candidate restart"}
	}
	output, failure := d.run(ctx, commandSSH, "restart_candidate", Command{
		Name: d.config.ControllerPath,
		Args: []string{"restart", d.deployment.Transaction},
	})
	if failure != nil {
		return 0, failure
	}
	line := strings.TrimSpace(string(output.Stdout))
	const prefix = "candidate pid="
	if strings.Count(line, prefix) != 1 || !strings.HasPrefix(line, prefix) || strings.Contains(line, "\n") {
		return 0, &e2e.Failure{Class: e2e.FailureHarness, Message: "controller returned an invalid candidate PID"}
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(line, prefix))
	if err != nil || pid <= 0 {
		return 0, &e2e.Failure{Class: e2e.FailureHarness, Message: "controller returned an invalid candidate PID"}
	}
	return pid, nil
}

func validateAgentPlan(plan e2e.AgentFixturePlan) *e2e.Failure {
	if plan.SchemaVersion != 1 || !agentRunPattern.MatchString(plan.RunID) || !agentNoncePattern.MatchString(plan.Nonce) ||
		strings.TrimSpace(plan.SessionID) == "" || len(plan.SessionID) > 256 || strings.ContainsAny(plan.SessionID, "\r\n\x00") ||
		strings.TrimSpace(plan.Prompt) == "" || len(plan.Prompt) > 4096 || strings.ContainsRune(plan.Prompt, '\x00') || !strings.Contains(plan.Prompt, plan.Nonce) ||
		strings.TrimSpace(plan.Text) == "" || len(plan.Text) > 4096 || strings.ContainsRune(plan.Text, '\x00') {
		return &e2e.Failure{Class: e2e.FailureHarness, Message: "agent fixture plan is invalid"}
	}
	return nil
}

func (d *Driver) runRemote(ctx context.Context, step, script string, values ...string) (Output, *e2e.Failure) {
	args := []string{"-o", "GSSAPIAuthentication=no", d.config.RemoteHost, "bash", "-s", "--"}
	for _, value := range values {
		args = append(args, encodeRemoteArg(value))
	}
	return d.run(ctx, commandSSH, step, Command{Name: "ssh", Args: args, Stdin: []byte(script)})
}

func encodeRemoteArg(value string) string {
	return "x" + base64.StdEncoding.EncodeToString([]byte(value))
}

func pollSeconds(milliseconds int) string {
	return fmt.Sprintf("%.3f", float64(milliseconds)/1000)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

const auditMarkScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
audit=$(decode "$1")
test -f "$audit"
wc -l < "$audit"
`

const auditWaitScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
audit=$(decode "$1")
mark=$(decode "$2")
action=$(decode "$3")
source=$(decode "$4")
detail=$(decode "$5")
poll_seconds=$(decode "$6")
timeout_ms=$(decode "$7")
deadline=$((SECONDS + (timeout_ms + 999) / 1000))
while test "$SECONDS" -lt "$deadline"; do
  event=$(tail -n "+$((mark + 1))" "$audit" | jq -c \
    --arg action "$action" --arg source "$source" --arg detail "$detail" \
    'select(.Action == $action)
     | select($source == "" or ((.SessionID // "") | contains($source)))
     | select($detail == "" or ((.Detail // "") | contains($detail)))' \
    | tail -n 1)
  if test -n "$event"; then printf '%s\n' "$event"; exit 0; fi
  sleep "$poll_seconds"
done
exit 124
`

const armFixtureScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
state=$(decode "$1")
nonce=$(decode "$2")
case "$nonce" in E2E_STOP_FIXTURE_*) ;; *) exit 64 ;; esac
case "$nonce" in *[!A-Za-z0-9_]*) exit 64 ;; esac
test -d "$state" && test ! -L "$state"
test "$(stat -c '%a' "$state")" = 700
umask 077
tmp=$(mktemp "$state/stop-fixture.nonce.XXXXXX")
printf '%s\n' "$nonce" > "$tmp"
chmod 600 "$tmp"
mv -f "$tmp" "$state/stop-fixture.nonce"
`

const disarmFixtureScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
state=$(decode "$1")
test -d "$state" && test ! -L "$state"
nonce_file="$state/stop-fixture.nonce"
test ! -e "$nonce_file" || rm -f "$nonce_file"
`

const armAgentFixtureScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
state=$(decode "$1")
plan=$(decode "$2")
test -d "$state" && test ! -L "$state" && test "$(stat -c '%a' "$state")" = 700
printf '%s' "$plan" | jq -e '
  type == "object" and .schema_version == 1
  and (.run_id | test("^e2e_[A-Za-z0-9_]{8,96}$"))
  and (.nonce | test("^E2E_AGENT_FIXTURE_[A-Za-z0-9_]{8,96}$"))
  and (.nonce as $nonce | (.prompt | type == "string" and length > 0 and length <= 4096 and contains($nonce)))
  and (.session_id | type == "string" and length > 0 and length <= 256)
  and (.text | type == "string" and length > 0 and length <= 4096)
  and (.block | type == "boolean")
  and (keys | sort == ["block","nonce","prompt","run_id","schema_version","session_id","text"])
' >/dev/null
umask 077
tmp=$(mktemp "$state/agent-fixture.plan.json.XXXXXX")
printf '%s\n' "$plan" > "$tmp"
chmod 600 "$tmp"
mv -f "$tmp" "$state/agent-fixture.plan.json"
`

const waitAgentInvocationScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
state=$(decode "$1")
run_id=$(decode "$2")
event=$(decode "$3")
poll_seconds=$(decode "$4")
timeout_ms=$(decode "$5")
test -d "$state" && test ! -L "$state"
case "$run_id" in e2e_*) ;; *) exit 64 ;; esac
case "$run_id" in *[!A-Za-z0-9_]*) exit 64 ;; esac
case "$event" in started|finished|cancelled) ;; *) exit 64 ;; esac
transcript="$state/agent-fixture.invocations.jsonl"
deadline=$((SECONDS + (timeout_ms + 999) / 1000))
while test "$SECONDS" -lt "$deadline"; do
  if test -f "$transcript" && test ! -L "$transcript" && test "$(stat -c '%a' "$transcript")" = 600; then
    record=$(jq -c --arg run "$run_id" --arg event "$event" 'select(.schema_version == 1 and .run_id == $run and .event == $event)' "$transcript" | tail -n 1)
    if test -n "$record"; then printf '%s\n' "$record"; exit 0; fi
  fi
  sleep "$poll_seconds"
done
exit 124
`

const disarmAgentFixtureScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
state=$(decode "$1")
test -d "$state" && test ! -L "$state"
plan="$state/agent-fixture.plan.json"
test ! -e "$plan" || rm -f "$plan"
`

const findAgentInvocationScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
state=$(decode "$1")
run_id=$(decode "$2")
test -d "$state" && test ! -L "$state"
case "$run_id" in e2e_*) ;; *) exit 64 ;; esac
case "$run_id" in *[!A-Za-z0-9_]*) exit 64 ;; esac
transcript="$state/agent-fixture.invocations.jsonl"
if test ! -e "$transcript"; then printf 'false\n'; exit 0; fi
test -f "$transcript" && test ! -L "$transcript" && test "$(stat -c '%a' "$transcript")" = 600
jq -s -e --arg run "$run_id" 'any(.[]; .schema_version == 1 and .run_id == $run)' "$transcript" >/dev/null \
  && printf 'true\n' || printf 'false\n'
`
