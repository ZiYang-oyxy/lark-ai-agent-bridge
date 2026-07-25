package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lark-agent-bridge/internal/buildinfo"
)

func TestAppendUpgradeNotifyEnvEncodesContext(t *testing.T) {
	env := appendUpgradeNotifyEnv([]string{"PATH=/bin"}, upgradeNotifyContext{
		Version: "0.1.9", ChatID: "oc_abc", MessageID: "om_xyz",
	})
	var got string
	for _, e := range env {
		if strings.HasPrefix(e, upgradeNotifyEnvVar+"=") {
			got = e
		}
	}
	if got == "" {
		t.Fatalf("升级上下文未注入 env: %#v", env)
	}
	for _, want := range []string{"0.1.9", "oc_abc", "om_xyz"} {
		if !strings.Contains(got, want) {
			t.Fatalf("env 缺 %q: %s", want, got)
		}
	}
}

func TestAppendUpgradeNotifyEnvSkipsWhenNoAnchor(t *testing.T) {
	// 缺 message_id 或 chat_id 时不注入(无法定位对话)
	for _, ctx := range []upgradeNotifyContext{
		{Version: "0.1.9", ChatID: "oc_abc"},    // 缺 message
		{Version: "0.1.9", MessageID: "om_xyz"}, // 缺 chat
	} {
		env := appendUpgradeNotifyEnv([]string{"PATH=/bin"}, ctx)
		for _, e := range env {
			if strings.HasPrefix(e, upgradeNotifyEnvVar+"=") {
				t.Fatalf("缺锚点时不应注入: %#v", ctx)
			}
		}
	}
}

func TestAppendUpgradeNotifyEnvDedupes(t *testing.T) {
	// 已存在同名变量时应先剔除,不叠加
	env := appendUpgradeNotifyEnv(
		[]string{"PATH=/bin", upgradeNotifyEnvVar + "=STALE"},
		upgradeNotifyContext{Version: "0.2.0", ChatID: "oc_a", MessageID: "om_b"},
	)
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, upgradeNotifyEnvVar+"=") {
			n++
			if strings.Contains(e, "STALE") {
				t.Fatalf("陈旧值未被剔除: %s", e)
			}
		}
	}
	if n != 1 {
		t.Fatalf("同名变量应唯一, got %d", n)
	}
}

func TestNotifyUpgradeSuccessSendsReplyWhenPending(t *testing.T) {
	setBuildVersion(t, "0.1.9")
	notifier := &fakeNotifier{}
	svc := newNotifyTestService(notifier)
	t.Setenv(upgradeNotifyEnvVar, `{"version":"0.1.9","chat_id":"oc_abc","message_id":"om_origin"}`)

	svc.NotifyUpgradeSuccessIfPending(context.Background())

	if len(notifier.replies) != 1 {
		t.Fatalf("应发一条升级成功消息, got %d", len(notifier.replies))
	}
	r := notifier.replies[0]
	if r.ReplyToMessageID != "om_origin" {
		t.Fatalf("reply 锚点错: %q", r.ReplyToMessageID)
	}
	if !strings.Contains(r.Message, "0.1.9") || !strings.Contains(r.Message, "升级") {
		t.Fatalf("消息内容不含版本/升级: %q", r.Message)
	}
	// 消费后 env 应被清除,防止本进程重复消费
	if v := envLookup(upgradeNotifyEnvVar); v != "" {
		t.Fatalf("env 未清除: %q", v)
	}
}

func TestNotifyUpgradeSuccessSurvivesManagedRestartWithoutEnv(t *testing.T) {
	setBuildVersion(t, "0.1.9")
	notifier := &fakeNotifier{}
	svc := newNotifyTestService(notifier)
	svc.Config.SessionStorePath = filepath.Join(t.TempDir(), "sessions.json")
	t.Setenv(upgradeNotifyEnvVar, "")

	if err := svc.persistUpgradeNotify(upgradeNotifyContext{Version: "0.1.9", ChatID: "oc_abc", MessageID: "om_origin"}); err != nil {
		t.Fatal(err)
	}
	svc.NotifyUpgradeSuccessIfPending(context.Background())
	svc.NotifyUpgradeSuccessIfPending(context.Background())

	if len(notifier.replies) != 1 {
		t.Fatalf("persisted notification must be consumed exactly once, got %#v", notifier.replies)
	}
	if _, err := os.Stat(svc.upgradeNotifyPath()); !os.IsNotExist(err) {
		t.Fatalf("pending handoff was not consumed: %v", err)
	}
}

func TestNotifyUpgradeSuccessRejectsVersionMismatch(t *testing.T) {
	setBuildVersion(t, "0.1.8")
	notifier := &fakeNotifier{}
	svc := newNotifyTestService(notifier)
	svc.Config.SessionStorePath = filepath.Join(t.TempDir(), "sessions.json")
	if err := svc.persistUpgradeNotify(upgradeNotifyContext{Version: "0.1.9", ChatID: "oc_abc", MessageID: "om_origin"}); err != nil {
		t.Fatal(err)
	}

	svc.NotifyUpgradeSuccessIfPending(context.Background())

	if len(notifier.replies) != 0 {
		t.Fatalf("old binary must not report a newer version as successful: %#v", notifier.replies)
	}
	if !hasAuditAction(svc.Audit.Events(), "upgrade_notify_version_mismatch") {
		t.Fatalf("missing mismatch audit: %#v", svc.Audit.Events())
	}
}

func TestNotifyUpgradeSuccessSilentWhenNoEnv(t *testing.T) {
	notifier := &fakeNotifier{}
	svc := newNotifyTestService(notifier)
	// 显式确保无 env
	t.Setenv(upgradeNotifyEnvVar, "")
	svc.NotifyUpgradeSuccessIfPending(context.Background())
	if len(notifier.replies) != 0 {
		t.Fatalf("无升级上下文不应发消息, got %#v", notifier.replies)
	}
}

func envLookup(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok {
		return ""
	}
	return v
}

func setBuildVersion(t *testing.T, version string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = version
	t.Cleanup(func() { buildinfo.Version = old })
}
