package commanddriver

import (
	"context"
	"strings"
	"testing"

	"lark-agent-bridge/internal/e2e"
)

func TestLarkSendTextUsesSeparateArguments(t *testing.T) {
	executor := &recordingExecutor{Results: []executorResult{
		{output: Output{Stdout: []byte(`{"ok":true}`)}},
		{output: Output{Stdout: []byte("ou_bot\n")}},
		{output: Output{Stdout: []byte("om_message\n")}},
	}}
	driver := New(executor, validConfig(), validDeployment())

	id, failure := driver.SendText(context.Background(), `/stop ' unsafe`)
	if failure != nil || id != "om_message" {
		t.Fatalf("id/failure = %q/%#v", id, failure)
	}
	if len(executor.Commands) != 3 {
		t.Fatalf("commands = %#v", executor.Commands)
	}
	send := executor.Commands[2]
	if send.Name != "lark-cli" {
		t.Fatalf("name = %q", send.Name)
	}
	assertArgPair(t, send.Args, "--text", `<at user_id="ou_bot"></at> /stop ' unsafe`)
	for _, arg := range send.Args {
		if arg == "sh" || arg == "-c" {
			t.Fatalf("shell argument present: %#v", send.Args)
		}
	}
}

func TestWaitReplyFetchesStructuredCard(t *testing.T) {
	executor := &recordingExecutor{Results: []executorResult{
		{output: Output{Stdout: []byte("om_reply\n")}},
		{output: Output{Stdout: []byte(`{"data":{"items":[{"message_id":"om_reply","body":{"content":"READY_marker"}}]}}`)}},
	}}
	driver := New(executor, validConfig(), validDeployment())

	reply, failure := driver.WaitReply(context.Background(), 7, "om_source")
	if failure != nil || reply.MessageID != "om_reply" || !strings.Contains(string(reply.Raw), "READY_marker") {
		t.Fatalf("reply/failure = %#v/%#v", reply, failure)
	}
	if len(executor.Commands) != 2 || executor.Commands[0].Name != "ssh" || executor.Commands[1].Name != "lark-cli" {
		t.Fatalf("commands = %#v", executor.Commands)
	}
}

func TestContainsVisibleTextTraversesAnyJSONStrings(t *testing.T) {
	driver := New(&recordingExecutor{}, validConfig(), validDeployment())
	reply := e2e.Reply{Raw: []byte(`{"card":{"elements":[{"text":{"content":"READY_marker"}}]}}`)}
	ok, actual, failure := driver.ContainsVisibleText(reply, "READY_marker")
	if failure != nil || !ok || !strings.Contains(actual, "READY_marker") {
		t.Fatalf("ok/actual/failure = %v/%q/%#v", ok, actual, failure)
	}
}

func TestContainsVisibleTextClassifiesMalformedJSONAsHarness(t *testing.T) {
	driver := New(&recordingExecutor{}, validConfig(), validDeployment())
	_, _, failure := driver.ContainsVisibleText(e2e.Reply{Raw: []byte(`{`)}, "READY")
	if failure == nil || failure.Class != e2e.FailureHarness {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestIdentityScriptNeverPlacesAppSecretInProcessArguments(t *testing.T) {
	for _, forbidden := range []string{`--arg secret "$LARK_APP_SECRET"`, `-d "$(jq`} {
		if strings.Contains(identityScript, forbidden) {
			t.Fatalf("identity script exposes App Secret through argv: %s", forbidden)
		}
	}
	for _, required := range []string{`env.LARK_APP_SECRET`, `--data-binary @-`} {
		if !strings.Contains(identityScript, required) {
			t.Fatalf("identity script missing stdin credential transport %q", required)
		}
	}
}
