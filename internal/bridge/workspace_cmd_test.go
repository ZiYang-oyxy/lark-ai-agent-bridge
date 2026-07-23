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

	// Point the effective cwd at proj first so /ws save captures it. Under the
	// Task 6 authority model the scope cwd (workspace store) is the sole workdir
	// authority effectiveWorkDir consults, so we seed it there.
	want, _ := filepath.EvalSymlinks(proj)
	if err := ws.SetCwd(scope, want); err != nil {
		t.Fatal(err)
	}

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

// TestWorkspaceCmdWsDeleteAliases verifies the unified delete verb: /ws
// removal must accept del / delete / remove / rm interchangeably, so users need
// not remember a workspace-specific word distinct from /cron and /timer.
func TestWorkspaceCmdWsDeleteAliases(t *testing.T) {
	for _, verb := range []string{"del", "delete", "remove", "rm"} {
		t.Run(verb, func(t *testing.T) {
			dir := t.TempDir()
			ws, err := workspace.OpenWorkspaceStore(filepath.Join(dir, "workspaces.json"))
			if err != nil {
				t.Fatal(err)
			}
			s := newTestServiceWithWorkspace(t, ws)
			msg := Message{ID: "m1", Sender: "admin", ChatID: "c1", ThreadID: "t1", IsGroup: true}
			pref := config.RuntimePreference{ConversationMode: config.ConversationModeTopic}
			scope := s.workspaceScope(sessionKeyForMode(agent.Claude, msg, config.ConversationModeTopic))

			if err := ws.SaveNamed(scope, "foo", dir); err != nil {
				t.Fatal(err)
			}
			delCmd := Command{Type: CommandWs, Agent: agent.Claude, WsSub: verb, WsName: "foo"}
			if err := s.handleWs(context.Background(), msg, delCmd, pref); err != nil {
				t.Fatalf("handleWs %s: %v", verb, err)
			}
			if _, ok := ws.UseNamed(scope, "foo"); ok {
				t.Fatalf("alias foo still present after /ws %s", verb)
			}
		})
	}
}

func TestWorkspaceCmdCdNonexistentTriggersConfirmAndDoesNotStore(t *testing.T) {
	dir := t.TempDir()
	// A well-formed path under a real TempDir that does not yet exist.
	target := filepath.Join(dir, "new-proj")
	ws, err := workspace.OpenWorkspaceStore(filepath.Join(dir, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServiceWithWorkspace(t, ws)
	msg := Message{ID: "m1", Sender: "admin", ChatID: "c1", ThreadID: "t1", IsGroup: true}
	pref := config.RuntimePreference{ConversationMode: config.ConversationModeTopic}
	key := sessionKeyForMode(agent.Claude, msg, config.ConversationModeTopic)
	scope := s.workspaceScope(key)

	if err := s.handleCd(context.Background(), msg, cdCommand(target), pref); err != nil {
		t.Fatalf("handleCd: %v", err)
	}

	// (a) directory not created, cwd not stored yet — only the confirm card.
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("directory should not exist before confirmation: stat err = %v", statErr)
	}
	if _, ok := ws.CwdFor(scope); ok {
		t.Fatal("cwd must not be stored before the user confirms creation")
	}

	// (b) a CdSwitch pending was stored keyed by the card's sessionID.
	pendingID := runID(key.ID(), msg.ID)
	s.mu.Lock()
	pending, ok := s.pendingRuns[pendingID]
	s.mu.Unlock()
	if !ok {
		t.Fatalf("expected a pending run keyed by %q", pendingID)
	}
	if !pending.CdSwitch || pending.CdScope != scope {
		t.Fatalf("pending = %+v, want CdSwitch=true scope=%q", pending, scope)
	}
	want, _ := filepath.EvalSymlinks(dir)
	wantReal := filepath.Join(want, "new-proj")
	if pending.WorkDir != wantReal {
		t.Fatalf("pending workdir = %q, want %q", pending.WorkDir, wantReal)
	}

	// Now simulate the "create directory" button press.
	if _, err := s.HandleActionResult(context.Background(), ActionRequest{
		ActionID:  "create_workdir",
		SessionID: pendingID,
		Value:     target,
		Actor:     "admin",
	}); err != nil {
		t.Fatalf("create_workdir action: %v", err)
	}

	// Directory now exists, cwd switched into it, session reset.
	if info, statErr := os.Stat(target); statErr != nil || !info.IsDir() {
		t.Fatalf("directory should exist after confirmation: stat err = %v", statErr)
	}
	got, ok := ws.CwdFor(scope)
	if !ok || got != wantReal {
		t.Fatalf("cwd after create = %q %v, want %q", got, ok, wantReal)
	}
	if sess, ok := s.Sessions.Get(key); !ok || sess.WorkDir != wantReal {
		t.Fatalf("session workdir after create = %q (ok=%v), want %q", sess.WorkDir, ok, wantReal)
	}
	// pending consumed
	s.mu.Lock()
	_, stillPending := s.pendingRuns[pendingID]
	s.mu.Unlock()
	if stillPending {
		t.Fatal("pending should have been popped by create_workdir")
	}
}

// TestEffectiveWorkDirScopeCwdBeatsSession locks the Task 6 authority model:
// the topic workspace cwd is the sole workdir authority. Even when a session
// records a (stale) run workdir, effectiveWorkDir must return the scope cwd,
// not the session's value. Record and decide are decoupled.
func TestEffectiveWorkDirScopeCwdBeatsSession(t *testing.T) {
	dir := t.TempDir()
	scopeDir := filepath.Join(dir, "scope")
	sessDir := filepath.Join(dir, "sess")
	for _, d := range []string{scopeDir, sessDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.OpenWorkspaceStore(filepath.Join(dir, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServiceWithWorkspace(t, ws)
	msg := Message{ID: "m1", Sender: "admin", ChatID: "c1", ThreadID: "t1", IsGroup: true}
	key := sessionKeyForMode(agent.Claude, msg, config.ConversationModeTopic)

	// session records a stale run workdir (record, not decide)...
	s.Sessions.Reset(key, sessDir)
	// ...but the scope cwd is the authority.
	if err := ws.SetCwd(s.workspaceScope(key), scopeDir); err != nil {
		t.Fatal(err)
	}

	if got := s.effectiveWorkDir(key, Command{}); got != scopeDir {
		t.Fatalf("effectiveWorkDir = %q, want scope cwd %q (session must not win)", got, scopeDir)
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
