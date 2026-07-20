package commanddriver

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"

	"lark-agent-bridge/internal/e2e"
)

func TestRemoteDriverDoesNotInterpolateDynamicValuesIntoScript(t *testing.T) {
	executor := &recordingExecutor{}
	driver := New(executor, validConfig(), validDeployment())
	nonce := `E2E_STOP_FIXTURE_0123456789ABCDEF`
	if failure := driver.Arm(context.Background(), nonce); failure != nil {
		t.Fatal(failure)
	}
	if len(executor.Commands) != 1 {
		t.Fatalf("commands = %#v", executor.Commands)
	}
	got := executor.Commands[0]
	if got.Name != "ssh" || !contains(got.Args, encodeRemoteArg(nonce)) {
		t.Fatalf("command = %#v", got)
	}
	if strings.Contains(string(got.Stdin), nonce) {
		t.Fatal("nonce interpolated into remote script")
	}
}

func TestRemoteDriverRejectsUnsafeNonceBeforeExecution(t *testing.T) {
	executor := &recordingExecutor{}
	driver := New(executor, validConfig(), validDeployment())
	failure := driver.Arm(context.Background(), `E2E_STOP_FIXTURE_'_$(touch_forbidden)`)
	if failure == nil || failure.Class != e2e.FailureHarness {
		t.Fatalf("failure = %#v", failure)
	}
	if len(executor.Commands) != 0 {
		t.Fatalf("executed = %#v", executor.Commands)
	}
}

func TestRemoteArgumentEncodingPreservesEmptyValues(t *testing.T) {
	encoded := encodeRemoteArg("")
	if encoded == "" || encoded[0] != 'x' {
		t.Fatalf("encoded empty value = %q", encoded)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, "x"))
	if err != nil || string(decoded) != "" {
		t.Fatalf("decoded/err = %q/%v", decoded, err)
	}
}

func TestEmbeddedRemoteScriptsParseWithSystemBash(t *testing.T) {
	scripts := map[string]string{
		"identity":   identityScript,
		"reply":      waitReplyScript,
		"audit-mark": auditMarkScript,
		"audit-wait": auditWaitScript,
		"arm":        armFixtureScript,
		"disarm":     disarmFixtureScript,
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			command := exec.Command("/bin/bash", "-n")
			command.Stdin = strings.NewReader(script)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("bash -n: %v: %s", err, output)
			}
		})
	}
}

func TestAuditWaitPassesFiltersAsArgumentsAndParsesEvent(t *testing.T) {
	executor := &recordingExecutor{Results: []executorResult{{output: Output{Stdout: []byte(`{"Action":"cardkit_update","SessionID":"om_source","Detail":"event=stopped"}`)}}}}
	driver := New(executor, validConfig(), validDeployment())

	event, failure := driver.Wait(context.Background(), 987654321, e2e.AuditMatch{Action: "cardkit_update", Source: "om_source", Detail: "event=stopped"})
	if failure != nil || event.Action != "cardkit_update" || event.Detail != "event=stopped" {
		t.Fatalf("event/failure = %#v/%#v", event, failure)
	}
	command := executor.Commands[0]
	for _, value := range []string{"987654321", "cardkit_update", "om_source", "event=stopped"} {
		if !contains(command.Args, encodeRemoteArg(value)) {
			t.Fatalf("missing argv %q in %#v", value, command.Args)
		}
		if strings.Contains(string(command.Stdin), value) {
			t.Fatalf("dynamic value %q interpolated into script", value)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
