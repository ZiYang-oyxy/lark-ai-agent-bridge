package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lark-agent-bridge/internal/e2e"
)

func TestRunRejectsUnknownScenarioBeforeDriverActions(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{
		"run",
		"--scenario", "unknown",
		"--deployment", deploymentFile(t),
		"--config", configFile(t),
		"--evidence-dir", t.TempDir(),
	}, &stdout, func(e2e.Config, e2e.Deployment) e2e.Drivers {
		t.Fatal("driver factory called for unknown scenario")
		return e2e.Drivers{}
	}, func() (string, error) { return "run-unknown", nil })
	if err == nil {
		t.Fatal("run succeeded")
	}
	if !strings.Contains(stdout.String(), "FAIL class=harness scenario=unknown step=load_scenario evidence=") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunPrintsOneStablePassLine(t *testing.T) {
	var stdout bytes.Buffer
	driver := newPassingDriver()
	err := run([]string{
		"run",
		"--scenario", "stop",
		"--deployment", deploymentFile(t),
		"--config", configFile(t),
		"--evidence-dir", t.TempDir(),
	}, &stdout, func(e2e.Config, e2e.Deployment) e2e.Drivers {
		return e2e.Drivers{Messenger: driver, Replies: driver, Audit: driver, Fixture: driver}
	}, func() (string, error) { return "run-pass", nil })
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "PASS scenario=stop evidence=") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunRequiresAllFlagsBeforeEvidenceOrDrivers(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{"run", "--scenario", "stop"}, &stdout, func(e2e.Config, e2e.Deployment) e2e.Drivers {
		t.Fatal("driver factory called")
		return e2e.Drivers{}
	}, func() (string, error) { return "run-unused", nil })
	if err == nil || stdout.Len() != 0 {
		t.Fatalf("err/stdout = %v/%q", err, stdout.String())
	}
}

func deploymentFile(t *testing.T) string {
	t.Helper()
	return writeJSON(t, "deployment.json", `{"schema_version":1,"transaction":"/state/deployments/txn.1","candidate_pid":42,"source_commit":"abc123","binary_sha256":"sha256:abcd","workspace":"/workspace","state_dir":"/state","fixture_dir":"/state/fixtures"}`)
}

func configFile(t *testing.T) string {
	t.Helper()
	return writeJSON(t, "config.json", `{"schema_version":1,"profile":"profile","expected_bot_name":"bot","app_id":"app","chat_id":"chat","remote_host":"user@example.test","audit_path":"/audit/audit.jsonl","poll_interval_ms":20,"step_timeout_ms":1000}`)
}

func writeJSON(t *testing.T, name, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type passingDriver struct {
	sent  int
	mark  int64
	nonce string
}

func newPassingDriver() *passingDriver { return &passingDriver{} }

func (d *passingDriver) SendText(_ context.Context, body string) (string, *e2e.Failure) {
	d.sent++
	switch body {
	case "/agent-mode claude":
		return "agent-source", nil
	case "/stop":
		return "stop-source", nil
	default:
		return "start-source", nil
	}
}

func (d *passingDriver) WaitReply(_ context.Context, _ int64, source string) (e2e.Reply, *e2e.Failure) {
	switch source {
	case "agent-source":
		return e2e.Reply{MessageID: "agent-reply", Raw: []byte(`{"text":"claude"}`)}, nil
	case "start-source":
		return e2e.Reply{MessageID: "start-reply", Raw: []byte(`{"text":"READY_` + d.nonce + `"}`)}, nil
	default:
		return e2e.Reply{MessageID: "stop-reply", Raw: []byte(`{"text":"已请求停止当前任务"}`)}, nil
	}
}

func (d *passingDriver) ContainsVisibleText(reply e2e.Reply, expected string) (bool, string, *e2e.Failure) {
	return strings.Contains(string(reply.Raw), expected), string(reply.Raw), nil
}

func (d *passingDriver) Mark(context.Context) (int64, *e2e.Failure) {
	d.mark++
	return d.mark, nil
}

func (d *passingDriver) Wait(_ context.Context, _ int64, match e2e.AuditMatch) (e2e.AuditEvent, *e2e.Failure) {
	raw, _ := json.Marshal(map[string]string{"Action": match.Action, "Detail": match.Detail})
	return e2e.AuditEvent{Action: match.Action, Detail: match.Detail, Raw: raw}, nil
}

func (d *passingDriver) Arm(_ context.Context, nonce string) *e2e.Failure {
	d.nonce = nonce
	return nil
}

func (d *passingDriver) Disarm(context.Context) *e2e.Failure { return nil }
