package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRejectsNonAbsolute(t *testing.T) {
	if r := ResolveWorkingDirectory("relative/path", "/home/<USER>"); r.OK {
		t.Fatalf("relative path should be rejected: %+v", r)
	}
	if r := ResolveWorkingDirectory("relative/path", "/home/<USER>"); r.UserVisible == "" {
		t.Fatal("relative path rejection should carry a UserVisible message")
	}
	if r := ResolveWorkingDirectory("", "/home/<USER>"); r.OK {
		t.Fatal("empty should be rejected")
	}
	if r := ResolveWorkingDirectory("   ", "/home/<USER>"); r.OK || r.UserVisible == "" {
		t.Fatalf("blank should be rejected with a message: %+v", r)
	}
}

func TestResolveRejectsBlacklist(t *testing.T) {
	home := t.TempDir()
	// Fixed high-risk paths that exist on macOS. Each should be rejected with a
	// non-empty Chinese reason. Using fixed absolute paths (not TempDir) keeps
	// this independent of where t.TempDir() lands (e.g. /var/folders).
	fixed := []string{
		"/",              // filesystem root
		"/tmp",           // temp root
		"/private/tmp",   // temp root (macOS)
		"/usr",           // system dir
		"/etc",           // system dir
		"/bin",           // system dir
		"/var",           // system dir
		"/System",        // system dir
		"/Library",       // system dir
		"/Applications",  // system dir
		"/private",       // system dir
		"/sbin",          // system dir
		"/Volumes",       // volume root
		"/Volumes/Macintosh HD", // direct child of /Volumes
	}
	for _, p := range fixed {
		r := ResolveWorkingDirectory(p, home)
		if r.OK {
			t.Fatalf("blacklisted %q should be rejected: %+v", p, r)
		}
		if r.UserVisible == "" {
			t.Fatalf("blacklisted %q should carry a reason", p)
		}
	}

	// home root, home's parent, ~/Desktop, ~/Downloads are all home-relative.
	homeRelative := []string{
		home,                                // home root
		filepath.Dir(home),                  // parent of home
		filepath.Join(home, "Desktop"),      // too broad
		filepath.Join(home, "Downloads"),    // too broad
	}
	for _, p := range homeRelative {
		if r := ResolveWorkingDirectory(p, home); r.OK {
			t.Fatalf("home-relative blacklisted %q should be rejected: %+v", p, r)
		}
	}

	// os.TempDir() root is also rejected.
	if r := ResolveWorkingDirectory(os.TempDir(), home); r.OK {
		t.Fatalf("os.TempDir() root %q should be rejected: %+v", os.TempDir(), r)
	}
}

func TestResolveExpandsTildeAndRealpath(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "work", "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	r := ResolveWorkingDirectory("~/work/proj", home)
	if !r.OK || !r.Exists {
		t.Fatalf("expected ok+exists: %+v", r)
	}
	// realpath 解析后应等于 proj 的 realpath
	want, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	if r.Realpath != want {
		t.Fatalf("realpath = %q, want %q", r.Realpath, want)
	}

	// bare "~" expands to home itself — which is blacklisted (home root).
	if r := ResolveWorkingDirectory("~", home); r.OK {
		t.Fatalf("bare ~ (home root) should be rejected: %+v", r)
	}
}

func TestResolveNonexistentPassesFormatButNotExists(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "work", "new-proj")
	r := ResolveWorkingDirectory(target, home)
	if !r.OK {
		t.Fatalf("well-formed nonexistent path should be OK for create-flow: %+v", r)
	}
	if r.Exists {
		t.Fatal("should report Exists=false")
	}
	// Realpath should carry the canonical (symlink-resolved) existing-ancestor
	// prefix plus the not-yet-created tail, so it stays an absolute path
	// pointing at target. Here only `home` exists; work/new-proj is the tail.
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(realHome, "work", "new-proj"); r.Realpath != want {
		t.Fatalf("Realpath = %q, want %q", r.Realpath, want)
	}
}

func TestResolveEmptyHomeRejectsTilde(t *testing.T) {
	// An empty (or non-absolute) home must not let a ~ path degrade into an
	// unvalidated relative path that escapes the blacklist.
	for _, in := range []string{"~/foo", "~"} {
		r := ResolveWorkingDirectory(in, "")
		if r.OK {
			t.Fatalf("%q with empty home should be rejected: %+v", in, r)
		}
		if r.UserVisible == "" {
			t.Fatalf("%q with empty home should carry a message", in)
		}
	}
	// Non-absolute home is equally unsafe.
	if r := ResolveWorkingDirectory("~/foo", "relative/home"); r.OK {
		t.Fatalf("~ with non-absolute home should be rejected: %+v", r)
	}
}

func TestResolveSymlinkBypassRejected(t *testing.T) {
	home := t.TempDir()

	// 1. A literal symlinked system dir: /etc -> /private/etc on macOS. The
	//    input "/etc" is canonicalized to /private/etc, and the blacklist entry
	//    is canonicalized the same way, so the bypass is caught.
	if r := ResolveWorkingDirectory("/etc", home); r.OK {
		t.Fatalf("/etc (symlink to /private/etc) should be rejected: %+v", r)
	}

	// 2. A user-created symlink pointing at a blacklisted target must not let a
	//    caller reach that target through the link. Point link -> /usr (system
	//    dir). Resolving the link yields /usr, which is blacklisted.
	link := filepath.Join(home, "escape")
	if err := os.Symlink("/usr", link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if r := ResolveWorkingDirectory(link, home); r.OK {
		t.Fatalf("symlink to /usr should be rejected after canonicalization: %+v", r)
	}
	if r := ResolveWorkingDirectory("~/escape", home); r.OK {
		t.Fatalf("~ symlink to /usr should be rejected: %+v", r)
	}
}

func TestResolveRejectsExistingNonDirectory(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "work", "afile.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := ResolveWorkingDirectory("~/work/afile.txt", home)
	if r.OK {
		t.Fatalf("existing non-directory should be rejected: %+v", r)
	}
	if r.UserVisible == "" {
		t.Fatal("non-directory rejection should carry a message")
	}
}
