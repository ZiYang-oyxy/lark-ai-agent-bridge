package bridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/devmode"
	"lark-agent-bridge/internal/session"
)

// develService builds a Service whose sole admin is "admin", with a dev-mode
// store and a directly-held fake renderer for output assertions.
func develService(t *testing.T) (*Service, *card.FakeRenderer) {
	t.Helper()
	renderer := card.NewFakeRenderer()
	svc := NewServiceWithSessions(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder(), session.NewManager(), nil)

	accessStore, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := accessStore.Update(func(p *access.Policy) { p.Admins = append(p.Admins, "admin") }); err != nil {
		t.Fatal(err)
	}
	svc.Access = accessStore
	svc.AccessControls = access.NewRuntimeControls()

	dm, err := devmode.OpenStore(filepath.Join(t.TempDir(), "dev-mode.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc.DevMode = dm
	svc.PrereleaseManifestURL = "https://updates.example/prerelease.json"
	return svc, renderer
}

func lastDevelText(t *testing.T, r *card.FakeRenderer) string {
	t.Helper()
	events := r.Events()
	if len(events) == 0 || len(events[len(events)-1].Segments) == 0 {
		t.Fatalf("no rendered segments: %#v", events)
	}
	return events[len(events)-1].Segments[0].Text
}

func TestDevelEnablesAndDisablesForAdmin(t *testing.T) {
	svc, renderer := develService(t)
	pref := config.RuntimePreference{ConversationMode: config.ConversationModeTopic}
	admin := Message{ID: "m1", Sender: "admin", ChatID: "c1", ThreadID: "t1", IsGroup: true}

	// Enable.
	if err := svc.handleDevel(context.Background(), admin, Command{Type: CommandDevel, Text: "1"}, pref); err != nil {
		t.Fatal(err)
	}
	if !svc.DevMode.Prerelease() {
		t.Fatal("prerelease should be enabled after /.devel 1")
	}
	if got := lastDevelText(t, renderer); !strings.Contains(got, "已开启") || !strings.Contains(got, "预发布") {
		t.Fatalf("enable text = %q", got)
	}

	// Disable.
	if err := svc.handleDevel(context.Background(), admin, Command{Type: CommandDevel, Text: "0"}, pref); err != nil {
		t.Fatal(err)
	}
	if svc.DevMode.Prerelease() {
		t.Fatal("prerelease should be disabled after /.devel 0")
	}
	if got := lastDevelText(t, renderer); !strings.Contains(got, "已关闭") {
		t.Fatalf("disable text = %q", got)
	}
}

func TestDevelStatusQueryDoesNotChangeState(t *testing.T) {
	svc, renderer := develService(t)
	pref := config.RuntimePreference{ConversationMode: config.ConversationModeTopic}
	admin := Message{ID: "m1", Sender: "admin", ChatID: "c1"}

	if _, err := svc.DevMode.SetPrerelease(true); err != nil {
		t.Fatal(err)
	}
	if err := svc.handleDevel(context.Background(), admin, Command{Type: CommandDevel, Text: ""}, pref); err != nil {
		t.Fatal(err)
	}
	if !svc.DevMode.Prerelease() {
		t.Fatal("bare /.devel must not change state")
	}
	if got := lastDevelText(t, renderer); !strings.Contains(got, "已开启") {
		t.Fatalf("status text = %q", got)
	}
}

func TestDevelRejectsNonAdmin(t *testing.T) {
	svc, renderer := develService(t)
	pref := config.RuntimePreference{ConversationMode: config.ConversationModeTopic}
	user := Message{ID: "m1", Sender: "not-admin", ChatID: "c1"}

	if err := svc.handleDevel(context.Background(), user, Command{Type: CommandDevel, Text: "1"}, pref); err != nil {
		t.Fatal(err)
	}
	if svc.DevMode.Prerelease() {
		t.Fatal("non-admin must not be able to enable prerelease")
	}
	if got := lastDevelText(t, renderer); !strings.Contains(got, "管理员") {
		t.Fatalf("denied text = %q", got)
	}
}

func TestDevelRejectsBadArgument(t *testing.T) {
	svc, _ := develService(t)
	pref := config.RuntimePreference{ConversationMode: config.ConversationModeTopic}
	admin := Message{ID: "m1", Sender: "admin", ChatID: "c1"}

	if err := svc.handleDevel(context.Background(), admin, Command{Type: CommandDevel, Text: "maybe"}, pref); err != nil {
		t.Fatal(err)
	}
	if svc.DevMode.Prerelease() {
		t.Fatal("invalid argument must not change state")
	}
}
