package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceStoreCwdRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	s, err := OpenWorkspaceStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, ok := s.CwdFor("chat1:thread1"); ok {
		t.Fatal("expected no cwd initially")
	}
	if err := s.SetCwd("chat1:thread1", "/proj/a"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok := s.CwdFor("chat1:thread1")
	if !ok || got != "/proj/a" {
		t.Fatalf("got %q %v, want /proj/a true", got, ok)
	}
	// 落盘后重开可读回
	s2, err := OpenWorkspaceStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, ok := s2.CwdFor("chat1:thread1"); !ok || got != "/proj/a" {
		t.Fatalf("reload got %q %v", got, ok)
	}
	// 权限 0600
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", info.Mode().Perm())
	}
}

func TestWorkspaceStoreClearCwd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	s, _ := OpenWorkspaceStore(path)
	if err := s.SetCwd("chatX", "/proj/x"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.ClearCwd("chatX"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok := s.CwdFor("chatX"); ok {
		t.Fatal("cwd not cleared")
	}
	// Clearing an unset scope is a no-op, not an error.
	if err := s.ClearCwd("never-set"); err != nil {
		t.Fatalf("clear unset: %v", err)
	}
	// Persisted clear survives a reopen.
	s2, err := OpenWorkspaceStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := s2.CwdFor("chatX"); ok {
		t.Fatal("cleared cwd came back after reopen")
	}
}

func TestWorkspaceStoreEmptyScopeRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	s, _ := OpenWorkspaceStore(path)
	if err := s.SetCwd("   ", "/proj/a"); err == nil {
		t.Fatal("expected empty scope to be rejected")
	}
}

func TestOpenWorkspaceStoreEmptyPath(t *testing.T) {
	if _, err := OpenWorkspaceStore("   "); err == nil {
		t.Fatal("expected empty path to be rejected")
	}
}

func TestOpenWorkspaceStoreMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "workspaces.json")
	s, err := OpenWorkspaceStore(path)
	if err != nil {
		t.Fatalf("open missing: %v", err)
	}
	if _, ok := s.CwdFor("any"); ok {
		t.Fatal("expected empty store")
	}
}

func TestWorkspaceStoreNamedScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	s, _ := OpenWorkspaceStore(path)
	if err := s.SaveNamed("chatA", "proj", "/proj/a"); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 同名别名在不同 scope 下不串
	if _, ok := s.UseNamed("chatB", "proj"); ok {
		t.Fatal("alias leaked across scope")
	}
	got, ok := s.UseNamed("chatA", "proj")
	if !ok || got != "/proj/a" {
		t.Fatalf("use got %q %v", got, ok)
	}
	if err := s.RemoveNamed("chatA", "proj"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := s.UseNamed("chatA", "proj"); ok {
		t.Fatal("alias not removed")
	}
}

func TestWorkspaceStoreNamedRoundTripAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	s, _ := OpenWorkspaceStore(path)
	if err := s.SaveNamed("chatA", "proj", "/proj/a"); err != nil {
		t.Fatalf("save proj: %v", err)
	}
	if err := s.SaveNamed("chatA", "docs", "/proj/docs"); err != nil {
		t.Fatalf("save docs: %v", err)
	}
	if err := s.SaveNamed("chatB", "proj", "/other/proj"); err != nil {
		t.Fatalf("save other: %v", err)
	}
	// ListNamed returns bare name -> cwd, scoped to the requesting scope only.
	list := s.ListNamed("chatA")
	if len(list) != 2 || list["proj"] != "/proj/a" || list["docs"] != "/proj/docs" {
		t.Fatalf("list chatA = %v", list)
	}
	if _, leaked := list["proj"]; leaked && list["proj"] == "/other/proj" {
		t.Fatal("chatB alias leaked into chatA list")
	}

	// Empty alias name rejected.
	if err := s.SaveNamed("chatA", "  ", "/x"); err == nil {
		t.Fatal("expected empty alias name to be rejected")
	}

	// Reopen and confirm persistence + scope isolation survive.
	s2, err := OpenWorkspaceStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, ok := s2.UseNamed("chatA", "proj"); !ok || got != "/proj/a" {
		t.Fatalf("reload chatA proj = %q %v", got, ok)
	}
	if got, ok := s2.UseNamed("chatB", "proj"); !ok || got != "/other/proj" {
		t.Fatalf("reload chatB proj = %q %v", got, ok)
	}
}

func TestWorkspaceStoreUseNamedLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspaces.json")
	// Simulate legacy data: alias stored under a bare (unscoped) name.
	legacy := map[string]any{
		"schema_version": 1,
		"named": map[string]string{
			"legacyproj": "/legacy/proj",
		},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	s, err := OpenWorkspaceStore(path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	// Scoped key misses, but the bare-name fallback hits.
	got, ok := s.UseNamed("anyscope", "legacyproj")
	if !ok || got != "/legacy/proj" {
		t.Fatalf("legacy fallback got %q %v", got, ok)
	}
}

func TestOpenWorkspaceStoreUnsupportedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := OpenWorkspaceStore(path); err == nil {
		t.Fatal("expected unsupported schema error")
	}
}
