package bridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

func TestConfigSetWritesSpecifiedFieldsAndPreservesCurrent(t *testing.T) {
	svc, store := localConfigService(t)
	current := store.Get()
	if err := svc.handleConfigCommand(context.Background(), Message{ID: "set", Sender: "ou_admin"}, Command{Type: CommandConfig, Text: "set reply_mode=latest-card effort=high"}, current, current.ConversationMode); err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.ReplyMode != config.ReplyModeLatestCard || got.Effort != "high" || got.Model != current.Model || got.ConversationMode != current.ConversationMode {
		t.Fatalf("preference = %#v, current = %#v", got, current)
	}
}

func TestConfigSetAllowsClearingOptionalFields(t *testing.T) {
	svc, store := localConfigService(t)
	current := store.Get()
	current.AgentHome = "home-a"
	current.AgentBin = "bin-a"
	current.ShowMetaRowAgent = true
	current.ShowMetaRowRuntime = true
	if err := store.Set(current); err != nil {
		// The local test store has no agent catalogue, so labels remain valid.
		t.Fatal(err)
	}
	if err := svc.handleConfigCommand(context.Background(), Message{ID: "clear", Sender: "ou_admin"}, Command{Type: CommandConfig, Text: "set agent_home= agent_bin= meta_rows="}, current, current.ConversationMode); err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.AgentHome != "" || got.AgentBin != "" || got.ShowMetaRowAgent || got.ShowMetaRowRuntime || got.ShowMetaRowDeveloper {
		t.Fatalf("cleared preference = %#v", got)
	}
}

func TestConfigSetRejectsInvalidOrUnsupportedFieldsWithoutWriting(t *testing.T) {
	for _, text := range []string{"set effort=turbo", "set model=x", "set unknown=value"} {
		t.Run(text, func(t *testing.T) {
			svc, store := localConfigService(t)
			before := store.Get()
			if err := svc.handleConfigCommand(context.Background(), Message{ID: "invalid", Sender: "ou_admin"}, Command{Type: CommandConfig, Text: text}, before, before.ConversationMode); err != nil {
				t.Fatal(err)
			}
			if got := store.Get(); got != before {
				t.Fatalf("preference = %#v, want unchanged %#v", got, before)
			}
		})
	}
}

func TestConfigSetRequiresAdmin(t *testing.T) {
	svc, store := localConfigService(t)
	accessStore, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := accessStore.Update(func(p *access.Policy) { p.AllowedUsers = []string{"ou_user"} }); err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	svc.Access, svc.AccessControls = accessStore, controls
	before := store.Get()
	if err := svc.handleConfigCommand(context.Background(), Message{ID: "denied", Sender: "ou_user"}, Command{Type: CommandConfig, Text: "set effort=high"}, before, before.ConversationMode); err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got != before {
		t.Fatalf("preference = %#v, want unchanged %#v", got, before)
	}
	events := svc.Cards.(*card.FakeRenderer).Events()
	if len(events) == 0 || !strings.Contains(segmentText(events[len(events)-1]), "管理员") {
		t.Fatalf("events = %#v", events)
	}
}
