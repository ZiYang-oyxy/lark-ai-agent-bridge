package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
)

func TestCanonicalWorkDirResolvesSymlink(t *testing.T) {
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalWorkDir(filepath.Join(link, "."))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("canonical workdir = %q, want %q", got, want)
	}
}

func TestCatalogRecentIsolatedSortedAndLimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "session-catalog.json")
	catalog, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	workDir := mustCanonicalWorkDir(t, t.TempDir())
	otherDir := mustCanonicalWorkDir(t, t.TempDir())
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		if err := catalog.Upsert(CatalogEntry{
			SessionID: fmt.Sprintf("s-%02d", i), Agent: agent.Claude,
			WorkDir: workDir, UpdatedAt: now.Add(time.Duration(i) * time.Minute),
			BridgeInstructionsVersion: "v1", Summary: fmt.Sprintf("prompt %d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []CatalogEntry{
		{SessionID: "codex", Agent: agent.Codex, WorkDir: workDir, UpdatedAt: now.Add(time.Hour)},
		{SessionID: "other", Agent: agent.Claude, WorkDir: otherDir, UpdatedAt: now.Add(time.Hour)},
	} {
		if err := catalog.Upsert(entry); err != nil {
			t.Fatal(err)
		}
	}

	identity := CatalogIdentity{Agent: agent.Claude, WorkDir: workDir}
	got := catalog.Recent(identity, 10)
	if len(got) != 10 || got[0].SessionID != "s-11" || got[9].SessionID != "s-02" {
		t.Fatalf("recent = %#v", got)
	}
	if _, ok := catalog.Resolve(identity, "codex"); ok {
		t.Fatal("cross-agent session resolved")
	}
	if _, ok := catalog.Resolve(identity, "other"); ok {
		t.Fatal("cross-workdir session resolved")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("catalog mode = %o, want 600", gotMode)
	}
}

func TestCatalogUpsertAndTouchReplaceExistingEntry(t *testing.T) {
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "session-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	workDir := mustCanonicalWorkDir(t, t.TempDir())
	identity := CatalogIdentity{Agent: agent.Claude, WorkDir: workDir}
	old := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	entry := CatalogEntry{SessionID: "same", Agent: agent.Claude, WorkDir: workDir, UpdatedAt: old, Summary: "old", BridgeInstructionsVersion: "v1"}
	if err := catalog.Upsert(entry); err != nil {
		t.Fatal(err)
	}
	entry.Summary = "new"
	entry.UpdatedAt = old.Add(time.Minute)
	if err := catalog.Upsert(entry); err != nil {
		t.Fatal(err)
	}
	touchedAt := old.Add(2 * time.Minute)
	touched, err := catalog.Touch(identity, "same", touchedAt)
	if err != nil {
		t.Fatal(err)
	}
	if touched.Summary != "new" || !touched.UpdatedAt.Equal(touchedAt) || len(catalog.Recent(identity, 10)) != 1 {
		t.Fatalf("touched = %#v recent = %#v", touched, catalog.Recent(identity, 10))
	}
	if _, err := catalog.Touch(identity, "missing", touchedAt); err == nil {
		t.Fatal("touch missing session succeeded")
	}
}

func TestOpenCatalogMissingAndStrictDecode(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	if _, err := OpenCatalog(missing); err != nil {
		t.Fatalf("missing catalog: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("open missing catalog mutated filesystem: %v", err)
	}

	for name, data := range map[string]string{
		"malformed": `{`,
		"schema":    `{"schema_version":99,"entries":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenCatalog(path); err == nil {
				t.Fatalf("OpenCatalog(%s) succeeded", data)
			}
		})
	}
}

func mustCanonicalWorkDir(t *testing.T, path string) string {
	t.Helper()
	got, err := CanonicalWorkDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
