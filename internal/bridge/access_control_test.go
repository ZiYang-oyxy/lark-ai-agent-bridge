package bridge

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/feishu"
)

func TestAccessGateDeniesBeforeDispatchAndHintsMentionedGroup(t *testing.T) {
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), recorder)
	svc.Access = store
	svc.AccessControls = access.NewRuntimeControls()
	svc.AccessControls.OwnerRefreshFailed(assertBridgeErr("no scope"))

	if err := svc.HandleMessage(context.Background(), Message{ID: "dm", ChatID: "dm", Sender: "ou_no", Text: "run"}); err != nil {
		t.Fatal(err)
	}
	if len(renderer.Events()) != 0 {
		t.Fatalf("denied dm rendered events: %#v", renderer.Events())
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "group", ChatID: "oc_no", Sender: "ou_no", Text: "/help", IsGroup: true, Mentioned: true}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if len(events) != 1 || !strings.Contains(events[0].Segments[0].Text, "/invite group") {
		t.Fatalf("group hint events = %#v", events)
	}
	if !auditContainsAction(recorder.Events(), "access_denied") {
		t.Fatalf("audit = %#v", recorder.Events())
	}
}

func TestInviteAllGroupsConfigPanelAndCallbackGate(t *testing.T) {
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Access, svc.AccessControls, svc.AccessAppID = store, controls, "cli_app"
	svc.AccessInfo = fakeBridgeAccessInfo{chats: []feishu.KnownChat{{ID: "oc_one", Name: "One"}, {ID: "oc_two", Name: "Two"}}}

	if err := svc.HandleMessage(context.Background(), Message{ID: "all", ChatID: "dm", Sender: "ou_owner", Text: "/invite all group"}); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().AllowedChats; len(got) != 2 {
		t.Fatalf("allowed chats = %#v", got)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "cfg", ChatID: "dm", Sender: "ou_owner", Text: "/config"}); err != nil {
		t.Fatal(err)
	}
	form := renderer.Events()[len(renderer.Events())-1].ConfigForm
	if form == nil || len(form.AllowedChats) != 2 || form.AllowedChats[0].Name != "One" {
		t.Fatalf("config form = %#v", form)
	}

	if err := store.Update(func(p *access.Policy) { p.AllowedUsers = append(p.AllowedUsers, "ou_user") }); err != nil {
		t.Fatal(err)
	}
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "config", ActionID: "config.save", Actor: "ou_user"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || !strings.Contains(result.Event.Segments[0].Text, "仅管理员") {
		t.Fatalf("callback result = %#v", result)
	}
}

func TestServiceRejectsNonAdminConfigClose(t *testing.T) {
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(p *access.Policy) { p.AllowedUsers = append(p.AllowedUsers, "ou_user") }); err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	deleter := &recordingMessageDeleter{}
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Access = store
	svc.AccessControls = controls
	svc.MessageDeleter = deleter

	result, err := svc.HandleActionResult(t.Context(), ActionRequest{SessionID: "config", ActionID: "config.close", Actor: "ou_user", OpenMessageID: "om_config"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || !strings.Contains(result.Event.Segments[0].Text, "仅管理员") || len(deleter.messageIDs) != 0 {
		t.Fatalf("result/deletes = %#v / %#v", result, deleter.messageIDs)
	}
}

func TestServiceAllowsNonAdminGenericCardClose(t *testing.T) {
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(p *access.Policy) { p.AllowedUsers = append(p.AllowedUsers, "ou_user") }); err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	deleter := &recordingMessageDeleter{}
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Access = store
	svc.AccessControls = controls
	svc.MessageDeleter = deleter

	result, err := svc.HandleActionResult(t.Context(), ActionRequest{
		SessionID: "help", ActionID: "card.close", Actor: "ou_user", OpenMessageID: "om_help",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (ActionResult{}) || !reflect.DeepEqual(deleter.messageIDs, []string{"om_help"}) {
		t.Fatalf("result/deletes = %#v / %#v", result, deleter.messageIDs)
	}
}

type fakeBridgeAccessInfo struct{ chats []feishu.KnownChat }

func (f fakeBridgeAccessInfo) GetOwner(context.Context, string) (string, error) {
	return "ou_owner", nil
}
func (f fakeBridgeAccessInfo) ListChats(context.Context) ([]feishu.KnownChat, error) {
	return f.chats, nil
}

func TestOwnerCanInviteUsersAndAdminGateConfig(t *testing.T) {
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Access = store
	svc.AccessControls = controls

	invite := Message{ID: "invite", ChatID: "dm", Sender: "ou_owner", Text: "/invite user @_user_1", Mentions: []Mention{{OpenID: "ou_alice", Name: "Alice"}}}
	if err := svc.HandleMessage(context.Background(), invite); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().AllowedUsers; len(got) != 1 || got[0] != "ou_alice" {
		t.Fatalf("allowed users = %#v", got)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "config", ChatID: "dm", Sender: "ou_alice", Text: "/config"}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if !strings.Contains(events[len(events)-1].Segments[0].Text, "仅管理员") {
		t.Fatalf("admin denial event = %#v", events[len(events)-1])
	}
}

func TestUserAllowlistMutationRepliesClarifyPrivateChatScope(t *testing.T) {
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Access = store
	svc.AccessControls = controls

	for _, command := range []string{"/invite user @Alice", "/remove user @Alice"} {
		msg := Message{
			ID:       command,
			ChatID:   "dm",
			Sender:   "ou_owner",
			Text:     command,
			Mentions: []Mention{{OpenID: "ou_alice", Name: "Alice"}},
		}
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		events := renderer.Events()
		got := events[len(events)-1].Segments[0].Text
		if !strings.Contains(got, "私聊用户白名单") {
			t.Fatalf("%s reply = %q", command, got)
		}
	}
}

func TestSelectedGroupMembersAreManagedPerChat(t *testing.T) {
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Access = store
	svc.AccessControls = controls

	ownerMessage := func(id, text string, mentions ...Mention) {
		t.Helper()
		if err := svc.HandleMessage(t.Context(), Message{ID: id, ChatID: "oc_team", Sender: "ou_owner", Text: text, IsGroup: true, Mentioned: true, Mentions: mentions}); err != nil {
			t.Fatal(err)
		}
	}
	ownerMessage("invite-group", "/invite group")
	ownerMessage("invite-member", "/invite member @Alice", Mention{OpenID: "ou_alice", Name: "Alice"})
	ownerMessage("selected", "/group-access selected")

	policy := store.Get()
	group := access.GroupPolicyFor(policy, "oc_team")
	if group.Mode != access.GroupModeSelectedMembers || len(group.AllowedMembers) != 1 || group.AllowedMembers[0] != "ou_alice" {
		t.Fatalf("group policy = %#v", group)
	}
	if got := access.CanUseGroup(policy, controls, "oc_team", "ou_alice"); !got.OK || got.Reason != access.ReasonAllowedMember {
		t.Fatalf("member decision = %#v", got)
	}
	if got := access.CanUseGroup(policy, controls, "oc_team", "ou_stranger"); got.OK || got.Reason != access.ReasonDeniedMember {
		t.Fatalf("stranger decision = %#v", got)
	}

	before := len(renderer.Events())
	if err := svc.HandleMessage(t.Context(), Message{ID: "stranger", ChatID: "oc_team", Sender: "ou_stranger", Text: "/help", IsGroup: true, Mentioned: true}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if len(events) != before+1 || !strings.Contains(events[len(events)-1].Segments[0].Text, "/invite member") {
		t.Fatalf("stranger denial = %#v", events[before:])
	}

	ownerMessage("all", "/group-access all")
	if got := access.CanUseGroup(store.Get(), controls, "oc_team", "ou_stranger"); !got.OK || got.Reason != access.ReasonAllowedChat {
		t.Fatalf("all-members decision = %#v", got)
	}
}

func TestMemberManagementRequiresAllowedGroup(t *testing.T) {
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Access, svc.AccessControls = store, controls
	msg := Message{ID: "member", ChatID: "oc_missing", Sender: "ou_owner", Text: "/invite member @Alice", IsGroup: true, Mentioned: true, Mentions: []Mention{{OpenID: "ou_alice", Name: "Alice"}}}
	if err := svc.HandleMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	got := renderer.Events()[0].Segments[0].Text
	if !strings.Contains(got, "先发送 /invite group") {
		t.Fatalf("reply = %q", got)
	}
}

type assertBridgeErr string

func (e assertBridgeErr) Error() string { return string(e) }
