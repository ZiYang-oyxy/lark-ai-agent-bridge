package commanddriver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"lark-agent-bridge/internal/e2e"
)

var fixtureNoncePattern = regexp.MustCompile(`^E2E_STOP_FIXTURE_[A-Za-z0-9_]{8,96}$`)

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
