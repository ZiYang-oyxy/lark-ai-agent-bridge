package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDeploymentAcceptsSchemaOne(t *testing.T) {
	path := writeInput(t, validDeploymentJSON())

	got, err := LoadDeployment(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.CandidatePID != 42 || got.SourceCommit != "abc123" {
		t.Fatalf("deployment = %#v", got)
	}
}

func TestLoadDeploymentRejectsUnknownAndUnsafeValues(t *testing.T) {
	valid := validDeploymentJSON()
	tests := map[string]string{
		"unknown field":   strings.TrimSuffix(valid, "}") + `,"secret":"x"}`,
		"unknown schema":  strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		"zero pid":        strings.Replace(valid, `"candidate_pid":42`, `"candidate_pid":0`, 1),
		"empty commit":    strings.Replace(valid, `"source_commit":"abc123"`, `"source_commit":""`, 1),
		"relative state":  strings.Replace(valid, `"state_dir":"/state"`, `"state_dir":"state"`, 1),
		"foreign fixture": strings.Replace(valid, `"fixture_dir":"/state/fixtures"`, `"fixture_dir":"/tmp/fixtures"`, 1),
		"trailing json":   valid + `{}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeInput(t, raw)
			if _, err := LoadDeployment(path); err == nil {
				t.Fatal("LoadDeployment succeeded")
			}
		})
	}
}

func TestLoadConfigAcceptsSchemaOne(t *testing.T) {
	path := writeInput(t, validConfigJSON())

	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "profile" || got.RemoteHost != "user@example.test" || got.StepTimeoutMS != 12000 || got.ControllerPath != "/controller/test-instance.sh" {
		t.Fatalf("config = %#v", got)
	}
}

func TestLoadConfigRejectsCredentialAndUnsafeFields(t *testing.T) {
	valid := validConfigJSON()
	tests := map[string]string{
		"credential":          strings.TrimSuffix(valid, "}") + `,"token":"private"}`,
		"unknown schema":      strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		"unsafe host":         strings.Replace(valid, `"remote_host":"user@example.test"`, `"remote_host":"user@example.test;id"`, 1),
		"relative audit":      strings.Replace(valid, `"audit_path":"/audit/audit.jsonl"`, `"audit_path":"audit.jsonl"`, 1),
		"relative controller": strings.Replace(valid, `"controller_path":"/controller/test-instance.sh"`, `"controller_path":"controller.sh"`, 1),
		"zero poll":           strings.Replace(valid, `"poll_interval_ms":200`, `"poll_interval_ms":0`, 1),
		"huge poll":           strings.Replace(valid, `"poll_interval_ms":200`, `"poll_interval_ms":60000`, 1),
		"zero timeout":        strings.Replace(valid, `"step_timeout_ms":12000`, `"step_timeout_ms":0`, 1),
		"short timeout":       strings.Replace(valid, `"step_timeout_ms":12000`, `"step_timeout_ms":100`, 1),
		"excessive timeout":   strings.Replace(valid, `"step_timeout_ms":12000`, `"step_timeout_ms":300001`, 1),
		"missing app id":      strings.Replace(valid, `"app_id":"app"`, `"app_id":""`, 1),
		"trailing config":     valid + `[]`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeInput(t, raw)
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("LoadConfig succeeded")
			}
		})
	}
}

func TestLoadConfigAllowsLongRealAgentTimeout(t *testing.T) {
	path := writeInput(t, strings.Replace(validConfigJSON(), `"step_timeout_ms":12000`, `"step_timeout_ms":180000`, 1))
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.StepTimeoutMS != 180000 {
		t.Fatalf("step timeout = %d, want 180000", config.StepTimeoutMS)
	}
}

func validDeploymentJSON() string {
	return `{"schema_version":1,"transaction":"/state/deployments/txn.1","candidate_pid":42,"source_commit":"abc123","binary_sha256":"sha256:abcd","workspace":"/workspace","state_dir":"/state","fixture_dir":"/state/fixtures"}`
}

func validConfigJSON() string {
	return `{"schema_version":1,"profile":"profile","expected_bot_name":"bot","app_id":"app","chat_id":"chat","remote_host":"user@example.test","audit_path":"/audit/audit.jsonl","poll_interval_ms":200,"step_timeout_ms":12000,"controller_path":"/controller/test-instance.sh"}`
}

func writeInput(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
