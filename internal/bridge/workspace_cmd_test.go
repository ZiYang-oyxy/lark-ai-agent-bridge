package bridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
	"lark-agent-bridge/internal/workspace"
)

// newTestServiceWithWorkspace builds a Service wired with a workspace store and
// an access policy whose sole admin is "admin". Non-admin senders fail the
// per-command admin gate exercised by the /cd and /ws mutating branches.
func newTestServiceWithWorkspace(t *testing.T, ws *workspace.Store) *Service {
	t.Helper()
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder(), session.NewManager(), nil)
	svc.Workspaces = ws
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(p *access.Policy) {
		p.Admins = append(p.Admins, "admin")
	}); err != nil {
		t.Fatal(err)
	}
	svc.Access = store
	svc.AccessControls = access.NewRuntimeControls()
	return svc
}

func cdCommand(workDir string) Command {
	return Command{Type: CommandCd, Agent: agent.Claude, WorkDir: workDir}
}

func TestWorkspaceCmdCdSwitchesAndResets(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.OpenWorkspaceStore(filepath.Join(dir, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServiceWithWorkspace(t, ws)
	msg := Message{ID: "m1", Text: "/cd " + proj, Sender: "admin", ChatID: "c1", ThreadID: "t1", IsGroup: true}

	key := sessionKeyForMode(agent.Claude, msg, config.ConversationModeTopic)
	// seed a session on a stale workdir so we can assert it was reset.
	s.Sessions.Reset(key, dir)

	if err := s.handleCd(context.Background(), msg, cdCommand(proj), config.RuntimePreference{ConversationMode: config.ConversationModeTopic}); err != nil {
		t.Fatalf("handleCd: %v", err)
	}

	scope := s.workspaceScope(key)
	want, _ := filepath.EvalSymlinks(proj)
	got, ok := ws.CwdFor(scope)
	if !ok || got != want {
		t.Fatalf("cwd = %q %v, want %q", got, ok, want)
	}
	if sess, ok := s.Sessions.Get(key); !ok || sess.WorkDir != want {
		t.Fatalf("session workdir = %q (ok=%v), want reset to %q", sess.WorkDir, ok, want)
	}
}

func TestWorkspaceCmdCdRejectsNonAdmin(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.OpenWorkspaceStore(filepath.Join(dir, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServiceWithWorkspace(t, ws)
	msg := Message{ID: "m1", Text: "/cd " + proj, Sender: "stranger", ChatID: "c1", ThreadID: "t1", IsGroup: true}

	if err := s.handleCd(context.Background(), msg, cdCommand(proj), config.RuntimePreference{ConversationMode: config.ConversationModeTopic}); err != nil {
		t.Fatalf("handleCd: %v", err)
	}
	key := sessionKeyForMode(agent.Claude, msg, config.ConversationModeTopic)
	if _, ok := ws.CwdFor(s.workspaceScope(key)); ok {
		t.Fatal("non-admin should not change cwd")
	}
}

func TestWorkspaceCmdCdBlacklistRejected(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.OpenWorkspaceStore(filepath.Join(dir, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServiceWithWorkspace(t, ws)
	msg := Message{ID: "m1", Sender: "admin", ChatID: "c1", ThreadID: "t1", IsGroup: true}

	if err := s.handleCd(context.Background(), msg, cdCommand("/"), config.RuntimePreference{ConversationMode: config.ConversationModeTopic}); err != nil {
		t.Fatalf("handleCd: %v", err)
	}
	key := sessionKeyForMode(agent.Claude, msg, config.ConversationModeTopic)
	if _, ok := ws.CwdFor(s.workspaceScope(key)); ok {
		t.Fatal("blacklisted target should not be stored")
	}
}

func TestWorkspaceCmdWsSaveUseList(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.OpenWorkspaceStore(filepath.Join(dir, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServiceWithWorkspace(t, ws)
	msg := Message{ID: "m1", Sender: "admin", ChatID: "c1", ThreadID: "t1", IsGroup: true}
	pref := config.RuntimePreference{ConversationMode: config.ConversationModeTopic}
	key := sessionKeyForMode(agent.Claude, msg, config.ConversationModeTopic)
	scope := s.workspaceScope(key)

	// Point the effective cwd at proj first so /ws save captures it. We seed the
	// session workdir (which effectiveWorkDir consults today) so this test does
	// not depend on the Task 6 authority reordering.
	want, _ := filepath.EvalSymlinks(proj)
	s.Sessions.Reset(key, want)

	// save
	saveCmd := Command{Type: CommandWs, Agent: agent.Claude, WsSub: "save", WsName: "foo"}
	if err := s.handleWs(context.Background(), msg, saveCmd, pref); err != nil {
		t.Fatalf("handleWs save: %v", err)
	}
	if got, ok := ws.UseNamed(scope, "foo"); !ok || got != want {
		t.Fatalf("alias foo = %q %v, want %q", got, ok, want)
	}

	// clear the scope cwd, then /ws use foo should restore it via the trio.
	if err := ws.ClearCwd(scope); err != nil {
		t.Fatal(err)
	}
	useCmd := Command{Type: CommandWs, Agent: agent.Claude, WsSub: "use", WsName: "foo"}
	if err := s.handleWs(context.Background(), msg, useCmd, pref); err != nil {
		t.Fatalf("handleWs use: %v", err)
	}
	if got, ok := ws.CwdFor(scope); !ok || got != want {
		t.Fatalf("after use cwd = %q %v, want %q", got, ok, want)
	}
	if sess, ok := s.Sessions.Get(key); !ok || sess.WorkDir != want {
		t.Fatalf("session workdir after use = %q (ok=%v), want %q", sess.WorkDir, ok, want)
	}

	// list should not error and includes the alias (assert via no-error contract).
	listCmd := Command{Type: CommandWs, Agent: agent.Claude, WsSub: "list"}
	if err := s.handleWs(context.Background(), msg, listCmd, pref); err != nil {
		t.Fatalf("handleWs list: %v", err)
	}
}

func TestWorkspaceCmdWsUseNonAdminRejected(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.OpenWorkspaceStore(filepath.Join(dir, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServiceWithWorkspace(t, ws)
	msg := Message{ID: "m1", Sender: "admin", ChatID: "c1", ThreadID: "t1", IsGroup: true}
	pref := config.RuntimePreference{ConversationMode: config.ConversationModeTopic}
	key := sessionKeyForMode(agent.Claude, msg, config.ConversationModeTopic)
	scope := s.workspaceScope(key)
	want, _ := filepath.EvalSymlinks(proj)
	if err := ws.SaveNamed(scope, "foo", want); err != nil {
		t.Fatal(err)
	}

	stranger := Message{ID: "m2", Sender: "stranger", ChatID: "c1", ThreadID: "t1", IsGroup: true}
	useCmd := Command{Type: CommandWs, Agent: agent.Claude, WsSub: "use", WsName: "foo"}
	if err := s.handleWs(context.Background(), stranger, useCmd, pref); err != nil {
		t.Fatalf("handleWs use: %v", err)
	}
	if _, ok := ws.CwdFor(scope); ok {
		t.Fatal("non-admin /ws use must not switch cwd")
	}
}
