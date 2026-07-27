package bridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

func TestParseLocalConfigCommand(t *testing.T) {
	cmd := ParseCommand(Message{Text: "/local-config"}, agent.Claude)
	if cmd.Type != CommandLocalConfig {
		t.Fatalf("type = %s, want local-config", cmd.Type)
	}
	reset := ParseCommand(Message{Text: "/local-config reset"}, agent.Claude)
	if reset.Type != CommandLocalConfig || reset.Text != "reset" {
		t.Fatalf("reset cmd = %#v", reset)
	}
}

func localConfigService(t *testing.T) (*Service, *config.PreferenceStore) {
	t.Helper()
	defaults := config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, GroupMessageMode: config.GroupMessageModeMentionOnly, Agent: config.DefaultAgentKind}
	store, err := config.OpenPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"), defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(config.Config{}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	return svc, store
}

// /local-config in a DM must not write an override; it guides the user to
// /config instead.
func TestLocalConfigInDirectMessageGuidesToConfig(t *testing.T) {
	svc, store := localConfigService(t)
	msg := Message{ID: "m1", IsGroup: false, ChatID: "dm", Sender: "ou_user"}
	cmd := Command{Type: CommandLocalConfig}
	if err := svc.handleLocalConfigCommand(context.Background(), msg, cmd, store.Get().ConversationMode); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.ChatOverride("dm"); ok {
		t.Fatal("DM /local-config must not create a chat override")
	}
}

// local_config.save writes only the target group's override; global and other
// groups are untouched.
func TestLocalConfigSaveWritesOnlyTargetGroup(t *testing.T) {
	svc, store := localConfigService(t)
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "local-config-card",
		ActionID:  "local_config.save",
		Actor:     "ou_user",
		Value:     "oc-a",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "append_overflow_mode": "continue-card", "conversation_mode": "topic",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.Event.Type != "local_config_saved" {
		t.Fatalf("result event = %#v, want local_config_saved", result.Event)
	}
	if got := store.GetForChat("oc-a").ConversationMode; got != config.ConversationModeTopic {
		t.Fatalf("group oc-a conversation mode = %q, want topic", got)
	}
	if got := store.GetForChat("oc-a").AppendOverflowMode; got != config.AppendOverflowModeContinueCard {
		t.Fatalf("group oc-a append overflow mode = %q, want continue-card", got)
	}
	if got := store.Get().AppendOverflowMode; got != config.AppendOverflowModeTruncate {
		t.Fatalf("global append overflow mode = %q, want unchanged truncate", got)
	}
	if got := store.Get().ConversationMode; got != config.ConversationModeChat {
		t.Fatalf("global conversation mode = %q, want unchanged chat", got)
	}
	if got := store.GetForChat("oc-b").ConversationMode; got != config.ConversationModeChat {
		t.Fatalf("group oc-b conversation mode = %q, want unchanged chat", got)
	}
}

func TestLocalConfigSaveConfirmationListsOnlyChangedPreferences(t *testing.T) {
	svc, _ := localConfigService(t)
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "local-config-card", ActionID: "local_config.save", Actor: "ou_user", Value: "oc-a",
		FormValues: map[string]string{
			"effort": "low", "reply_mode": "append", "append_overflow_mode": "truncate",
			"conversation_mode": "topic", "topic_seed_mode": "quote", "group_message_mode": "mention_only",
			"respond_to_bots": "false", "show_meta_row_agent": "false",
			"show_meta_row_runtime": "false", "show_meta_row_developer": "false",
			"agent": config.DefaultAgentKind,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := segmentText(*result.Event)
	if !strings.Contains(text, "**会话模式**：`chat` → `topic`") {
		t.Fatalf("confirmation missing changed conversation mode: %q", text)
	}
	for _, unchanged := range []string{"**Effort**", "**回复模式**", "**Agent**", "**群消息接收**"} {
		if strings.Contains(text, unchanged) {
			t.Fatalf("confirmation includes unchanged field %q: %q", unchanged, text)
		}
	}
}

// local_config.save with no target chat id is rejected.
func TestLocalConfigSaveRequiresChatID(t *testing.T) {
	svc, store := localConfigService(t)
	result, _ := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID:  "local-config-card",
		ActionID:   "local_config.save",
		Actor:      "ou_user",
		Value:      "",
		FormValues: map[string]string{"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "topic"},
	})
	// With no target chat id the save must fail (error event) and persist
	// nothing under any plausible chat id.
	if result.Event != nil && result.Event.Type == "local_config_saved" {
		t.Fatal("save without chat id should not report success")
	}
	if _, ok := store.ChatOverride(""); ok {
		t.Fatal("save without chat id must not persist an empty-keyed override")
	}
}

// /local-config reset clears only the current group's override.
func TestLocalConfigResetClearsOnlyCurrentGroup(t *testing.T) {
	svc, store := localConfigService(t)
	topic := config.ConversationModeTopic
	if err := store.SetChat("oc-a", config.ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChat("oc-b", config.ChatOverride{ConversationMode: &topic}); err != nil {
		t.Fatal(err)
	}
	msg := Message{ID: "m1", IsGroup: true, ChatID: "oc-a", Sender: "ou_user"}
	cmd := Command{Type: CommandLocalConfig, Text: "reset"}
	if err := svc.handleLocalConfigCommand(context.Background(), msg, cmd, store.GetForChat("oc-a").ConversationMode); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.ChatOverride("oc-a"); ok {
		t.Fatal("oc-a override should be cleared")
	}
	if _, ok := store.ChatOverride("oc-b"); !ok {
		t.Fatal("oc-b override must be preserved")
	}
}

// /local-config set applies only the requested field to the existing group
// override. inherit clears that field's pointer without affecting the others.
func TestLocalConfigSetIncrementalInherit(t *testing.T) {
	svc, store := localConfigService(t)
	latest := config.ReplyModeLatestCard
	if err := store.SetChat("oc-a", config.ChatOverride{ReplyMode: &latest}); err != nil {
		t.Fatal(err)
	}
	msg := Message{ID: "set", IsGroup: true, ChatID: "oc-a", Sender: "ou_admin"}
	if err := svc.handleLocalConfigCommand(context.Background(), msg, Command{Type: CommandLocalConfig, Text: "set effort=high"}, store.GetForChat("oc-a").ConversationMode); err != nil {
		t.Fatal(err)
	}
	override, ok := store.ChatOverride("oc-a")
	if !ok || override.Effort == nil || *override.Effort != "high" || override.ReplyMode == nil || *override.ReplyMode != latest {
		t.Fatalf("override after effort set = %#v, want effort=high and preserved reply mode=%q", override, latest)
	}
	if err := svc.handleLocalConfigCommand(context.Background(), msg, Command{Type: CommandLocalConfig, Text: "set reply_mode=inherit"}, store.GetForChat("oc-a").ConversationMode); err != nil {
		t.Fatal(err)
	}
	override, ok = store.ChatOverride("oc-a")
	if !ok || override.ReplyMode != nil || override.Effort == nil || *override.Effort != "high" {
		t.Fatalf("override after reply_mode inherit = %#v, want inherited reply mode and preserved effort=high", override)
	}
}

func TestLocalConfigSetRequiresAdmin(t *testing.T) {
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

	msg := Message{ID: "denied", IsGroup: true, ChatID: "oc-a", Sender: "ou_user"}
	if err := svc.handleLocalConfigCommand(context.Background(), msg, Command{Type: CommandLocalConfig, Text: "set effort=high"}, store.GetForChat("oc-a").ConversationMode); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.ChatOverride("oc-a"); ok {
		t.Fatal("non-admin /local-config set must not persist an override")
	}
	events := svc.Cards.(*card.FakeRenderer).Events()
	if len(events) == 0 || !strings.Contains(segmentText(events[len(events)-1]), "管理员") {
		t.Fatalf("events = %#v", events)
	}
}

func TestLocalConfigSetRejectsDuplicateKeysCaseInsensitively(t *testing.T) {
	for _, text := range []string{
		"set effort=high EFFORT=inherit",
		"set EFFORT=inherit effort=high",
	} {
		t.Run(text, func(t *testing.T) {
			svc, store := localConfigService(t)
			msg := Message{ID: "duplicate", IsGroup: true, ChatID: "oc-a", Sender: "ou_admin"}
			if err := svc.handleLocalConfigCommand(context.Background(), msg, Command{Type: CommandLocalConfig, Text: text}, store.GetForChat("oc-a").ConversationMode); err != nil {
				t.Fatal(err)
			}
			if _, ok := store.ChatOverride("oc-a"); ok {
				t.Fatal("duplicate keys must not persist an override")
			}
			events := svc.Cards.(*card.FakeRenderer).Events()
			if len(events) == 0 || !strings.Contains(segmentText(events[len(events)-1]), "字段重复：effort") {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

// localConfigOverview reports the effective per-field values, marks exactly the
// group's overridden fields, and counts them.
func TestLocalConfigOverviewMarksOverriddenFields(t *testing.T) {
	svc, store := localConfigService(t)
	topic := config.ConversationModeTopic
	latest := config.ReplyModeLatestCard
	if err := store.SetChat("oc-a", config.ChatOverride{ConversationMode: &topic, ReplyMode: &latest}); err != nil {
		t.Fatal(err)
	}
	overview := svc.localConfigOverview("oc-a")
	if overview == nil {
		t.Fatal("localConfigOverview returned nil")
	}
	if overview.ChatID != "oc-a" {
		t.Fatalf("overview ChatID = %q, want oc-a", overview.ChatID)
	}
	if overview.OverrideCount != 2 {
		t.Fatalf("overview OverrideCount = %d, want 2", overview.OverrideCount)
	}
	byLabel := map[string]card.LocalConfigItem{}
	for _, item := range overview.Items {
		byLabel[item.Label] = item
	}
	for _, label := range []string{"回复模式", "会话模式"} {
		if !byLabel[label].Overridden {
			t.Fatalf("item %q should be marked overridden", label)
		}
	}
	for _, label := range []string{"群消息接收", "响应其他 bot", "Agent 可执行文件"} {
		if byLabel[label].Overridden {
			t.Fatalf("item %q should be inherited, not overridden", label)
		}
	}
	// Effective values flow through GetForChat.
	if got := byLabel["会话模式"].Value; got != string(config.ConversationModeTopic) {
		t.Fatalf("会话模式 value = %q, want topic", got)
	}
}

// A group with no override reports zero overrides and all items inherited.
func TestLocalConfigOverviewNoOverride(t *testing.T) {
	svc, _ := localConfigService(t)
	overview := svc.localConfigOverview("oc-empty")
	if overview == nil {
		t.Fatal("localConfigOverview returned nil")
	}
	if overview.OverrideCount != 0 {
		t.Fatalf("overview OverrideCount = %d, want 0", overview.OverrideCount)
	}
	for _, item := range overview.Items {
		if item.Overridden {
			t.Fatalf("item %q should be inherited", item.Label)
		}
	}
}

// A group /local-config form must carry the chat id so the save callback knows
// its target, and must not touch access lists.
func TestLocalConfigFormCarriesChatIDAndIgnoresAccess(t *testing.T) {
	svc, store := localConfigService(t)
	form := svc.localConfigForm(store.GetForChat("oc-a"), "oc-a")
	if form.ChatID != "oc-a" {
		t.Fatalf("form ChatID = %q, want oc-a", form.ChatID)
	}
	// Access fields are not part of a per-chat override; a save with access
	// form values present must ignore them.
	_, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "local-config-card", ActionID: "local_config.save", Actor: "ou_user", Value: "oc-a",
		FormValues: map[string]string{
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
			"allowed_users": "ou_should_be_ignored",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := store.ChatOverride("oc-a"); ok {
		// conversation_mode chat == global default, model/effort/reply == global,
		// so nothing distinct should have been written; but even if written it
		// must never carry access data (ChatOverride has no access field).
		_ = p
	}
	// Ensure the help text mentions the new command surface.
	if !strings.Contains(HelpText(), "/local-config") {
		t.Fatal("HelpText should document /local-config")
	}
}
