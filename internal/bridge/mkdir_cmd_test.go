package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

func TestMkdirWorkspaceBoundary(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	renderer := card.NewFakeRenderer()
	svc := NewService(config.Config{DefaultAgent: "claude", DefaultWorkDir: root}, renderer, newFakeRunner(), audit.NewRecorder())
	msg := Message{ID: "mkdir", Sender: "ou_admin"}

	inside := filepath.Join(root, "nested", "work")
	if err := svc.handleMkdirCommand(context.Background(), msg, Command{Type: CommandMkdir, Text: inside}, config.RuntimePreference{}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(inside); err != nil || !info.IsDir() {
		t.Fatalf("inside directory not created: %v", err)
	}

	link := filepath.Join(root, "escape-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{filepath.Join(root, "..", filepath.Base(outside), "escape"), filepath.Join(outside, "absolute"), filepath.Join(link, "child")} {
		before := len(renderer.Events())
		if err := svc.handleMkdirCommand(context.Background(), msg, Command{Type: CommandMkdir, Text: raw}, config.RuntimePreference{}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(raw); !os.IsNotExist(err) {
			t.Fatalf("outside path %q unexpectedly exists: %v", raw, err)
		}
		events := renderer.Events()
		if len(events) != before+1 || !strings.Contains(segmentText(events[len(events)-1]), "范围") {
			t.Fatalf("outside response = %#v", events)
		}
	}
}

func TestMkdirRequiresAdmin(t *testing.T) {
	root := t.TempDir()
	renderer := card.NewFakeRenderer()
	svc := NewService(config.Config{DefaultAgent: "claude", DefaultWorkDir: root}, renderer, newFakeRunner(), audit.NewRecorder())
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(p *access.Policy) { p.Admins = []string{"ou_admin"} }); err != nil {
		t.Fatal(err)
	}
	svc.Access = store
	target := filepath.Join(root, "denied")
	if err := svc.handleMkdirCommand(context.Background(), Message{ID: "mkdir", Sender: "ou_user"}, Command{Type: CommandMkdir, Text: target}, config.RuntimePreference{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("non-admin target exists: %v", err)
	}
}

func TestCreateWorkspaceBoundedDirRejectsReplacedSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "mutable")
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := createWorkspaceBoundedDir(filepath.Join(link, "escaped"), []string{root}); err == nil {
		t.Fatal("createWorkspaceBoundedDir followed a replaced symlink outside the root")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped")); !os.IsNotExist(err) {
		t.Fatalf("outside directory unexpectedly created: %v", err)
	}
}

func TestCreateWorkspaceBoundedDirPinsRootBeforePathReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	moved := filepath.Join(parent, "workspace-opened")
	outside := t.TempDir()
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	var hookErr error
	created, err := createWorkspaceBoundedDirAfterOpen("nested", []string{root}, func(string) {
		if renameErr := os.Rename(root, moved); renameErr != nil {
			hookErr = renameErr
			return
		}
		hookErr = os.Symlink(outside, root)
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if created != filepath.Join(root, "nested") {
		t.Fatalf("created path = %q", created)
	}
	if info, err := os.Stat(filepath.Join(moved, "nested")); err != nil || !info.IsDir() {
		t.Fatalf("pinned root did not receive directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "nested")); !os.IsNotExist(err) {
		t.Fatalf("replacement root escaped workspace: %v", err)
	}
}
