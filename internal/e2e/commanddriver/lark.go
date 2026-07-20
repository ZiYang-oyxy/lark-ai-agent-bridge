package commanddriver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"lark-agent-bridge/internal/e2e"
)

type Driver struct {
	executor   Executor
	config     e2e.Config
	deployment e2e.Deployment

	identityMu sync.Mutex
	botOpenID  string
}

func New(executor Executor, config e2e.Config, deployment e2e.Deployment) *Driver {
	return &Driver{executor: executor, config: config, deployment: deployment}
}

func NewDrivers(config e2e.Config, deployment e2e.Deployment) e2e.Drivers {
	driver := New(OSExecutor{}, config, deployment)
	return e2e.Drivers{Messenger: driver, Replies: driver, Audit: driver, Fixture: driver}
}

func (d *Driver) SendText(ctx context.Context, body string) (string, *e2e.Failure) {
	botOpenID, failure := d.ensureIdentity(ctx)
	if failure != nil {
		return "", failure
	}
	text := fmt.Sprintf(`<at user_id="%s"></at> %s`, botOpenID, body)
	output, failure := d.run(ctx, commandLark, "send_text", Command{
		Name: "lark-cli",
		Args: []string{
			"--profile", d.config.Profile,
			"im", "+messages-send", "--as", "user",
			"--chat-id", d.config.ChatID,
			"--text", text,
			"--jq", ".data.message_id // .message_id // empty",
		},
	})
	if failure != nil {
		return "", failure
	}
	messageID, err := parseScalar(output.Stdout)
	if err != nil || messageID == "" {
		return "", &e2e.Failure{Class: e2e.FailureHarness, Message: fmt.Sprintf("parse send response: %v", err)}
	}
	return messageID, nil
}

func (d *Driver) WaitReply(ctx context.Context, mark int64, source string) (e2e.Reply, *e2e.Failure) {
	output, failure := d.runRemote(ctx, "wait_reply", waitReplyScript,
		d.config.AuditPath,
		fmt.Sprint(mark),
		source,
		pollSeconds(d.config.PollIntervalMS),
		fmt.Sprint(d.config.StepTimeoutMS),
	)
	if failure != nil {
		return e2e.Reply{}, failure
	}
	replyID, err := parseScalar(output.Stdout)
	if err != nil || replyID == "" {
		return e2e.Reply{}, &e2e.Failure{Class: e2e.FailureHarness, Message: fmt.Sprintf("parse reply id: %v", err)}
	}
	card, failure := d.run(ctx, commandLark, "fetch_reply", Command{
		Name: "lark-cli",
		Args: []string{
			"--profile", d.config.Profile,
			"im", "+messages-mget", "--as", "user",
			"--message-ids", replyID,
			"--format", "json",
		},
	})
	if failure != nil {
		return e2e.Reply{}, failure
	}
	if !json.Valid(card.Stdout) {
		return e2e.Reply{}, &e2e.Failure{Class: e2e.FailureHarness, Message: "message fetch returned malformed JSON"}
	}
	return e2e.Reply{MessageID: replyID, Raw: append([]byte(nil), card.Stdout...)}, nil
}

func (d *Driver) ContainsVisibleText(reply e2e.Reply, expected string) (bool, string, *e2e.Failure) {
	var decoded any
	if err := json.Unmarshal(reply.Raw, &decoded); err != nil {
		return false, "", &e2e.Failure{Class: e2e.FailureHarness, Message: fmt.Sprintf("decode reply JSON: %v", err)}
	}
	var values []string
	collectStrings(decoded, &values)
	actual := strings.Join(values, " | ")
	if len(actual) > 4000 {
		actual = actual[:4000]
	}
	for _, value := range values {
		if strings.Contains(value, expected) {
			return true, actual, nil
		}
	}
	return false, actual, nil
}

func (d *Driver) ensureIdentity(ctx context.Context) (string, *e2e.Failure) {
	d.identityMu.Lock()
	defer d.identityMu.Unlock()
	if d.botOpenID != "" {
		return d.botOpenID, nil
	}
	auth, failure := d.run(ctx, commandLark, "oauth", Command{
		Name: "lark-cli",
		Args: []string{"--profile", d.config.Profile, "auth", "status", "--json", "--verify"},
	})
	if failure != nil {
		return "", failure
	}
	var status struct {
		OK *bool `json:"ok"`
	}
	if err := json.Unmarshal(auth.Stdout, &status); err != nil {
		return "", &e2e.Failure{Class: e2e.FailureHarness, Message: fmt.Sprintf("decode oauth status: %v", err)}
	}
	if status.OK != nil && !*status.OK {
		return "", &e2e.Failure{Class: e2e.FailurePlatform, Message: "lark-cli OAuth profile is unavailable"}
	}
	identity, failure := d.runRemote(ctx, "identity", identityScript,
		d.config.ExpectedBotName,
		d.config.AppID,
		d.deployment.StateDir,
	)
	if failure != nil {
		return "", failure
	}
	openID, err := parseScalar(identity.Stdout)
	if err != nil || openID == "" {
		return "", &e2e.Failure{Class: e2e.FailureHarness, Message: fmt.Sprintf("parse Bot identity: %v", err)}
	}
	d.botOpenID = openID
	return openID, nil
}

func (d *Driver) run(ctx context.Context, kind commandKind, step string, command Command) (Output, *e2e.Failure) {
	output, err := d.executor.Run(ctx, command)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return Output{}, classifyCommandFailure(kind, err, output.Stderr, step)
	}
	return output, nil
}

func parseScalar(raw []byte) (string, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("empty output")
	}
	if strings.HasPrefix(value, `"`) {
		var decoded string
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return "", err
		}
		return decoded, nil
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("output is not one scalar line")
	}
	return value, nil
}

func collectStrings(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
	case []any:
		for _, item := range typed {
			collectStrings(item, out)
		}
	case map[string]any:
		for _, item := range typed {
			collectStrings(item, out)
		}
	}
}

const identityScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
expected_bot=$(decode "$1")
expected_app=$(decode "$2")
state=$(decode "$3")
env_file="$state/service.env"
test -f "$env_file" && test ! -L "$env_file"
test "$(stat -c '%a' "$env_file")" = 600
while IFS= read -r -d '' kv; do export "$kv"; done < "$env_file"
test "$LARK_APP_ID" = "$expected_app"
test -n "$LARK_APP_SECRET"
token=$(jq -nc '{app_id:env.LARK_APP_ID,app_secret:env.LARK_APP_SECRET}' \
  | curl -fsS -X POST 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' \
  -H 'Content-Type: application/json' --data-binary @- \
  | jq -er '.tenant_access_token')
curl -fsS 'https://open.feishu.cn/open-apis/bot/v3/info' \
  -H "Authorization: Bearer $token" \
  | jq -er --arg expected "$expected_bot" 'select(.bot.app_name == $expected) | .bot.open_id'
`

const waitReplyScript = `
set -Eeuo pipefail
decode() { printf '%s' "${1#x}" | base64 -d; }
audit=$(decode "$1")
mark=$(decode "$2")
source=$(decode "$3")
poll_seconds=$(decode "$4")
timeout_ms=$(decode "$5")
deadline=$((SECONDS + (timeout_ms + 999) / 1000))
while test "$SECONDS" -lt "$deadline"; do
  reply=$(tail -n "+$((mark + 1))" "$audit" | jq -r --arg source "$source" \
    'select(.Action == "cardkit_reply")
     | select((.Detail // "") | contains("reply_to=" + $source + " "))
     | .Detail' \
    | sed -n 's/.* message_id=\([^ ]*\).*/\1/p' | tail -n 1)
  if test -n "$reply"; then printf '%s\n' "$reply"; exit 0; fi
  sleep "$poll_seconds"
done
exit 124
`
