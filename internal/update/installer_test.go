package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstallerPrepareResolvesExecutableAndStagesBesideIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bridge-real")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bridge")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var stageDir string
	installer := Installer{
		Executable: func() (string, error) { return link, nil },
		Stage: func(_ context.Context, asset Asset, dir string) (Staged, error) {
			stageDir = dir
			path := filepath.Join(dir, ".staged")
			return Staged{Path: path, Asset: asset}, os.WriteFile(path, []byte("new"), 0o600)
		},
	}
	prepared, err := installer.Prepare(t.Context(), Asset{Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(prepared.Abort)
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.target != realTarget || stageDir != filepath.Dir(realTarget) {
		t.Fatalf("target=%q stageDir=%q", prepared.target, stageDir)
	}
}

func TestPreparedReplaceKeepsPreviousAndExecFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bridge")
	staged := filepath.Join(dir, ".staged")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	execErr := errors.New("exec failed")
	prepared := &Prepared{
		target:   target,
		staged:   staged,
		previous: target + ".previous",
		exec: func(path string, argv, env []string) error {
			if path != target || len(argv) != 2 || len(env) != 1 {
				t.Fatalf("exec args path=%q argv=%v env=%v", path, argv, env)
			}
			return execErr
		},
		sleep: func(time.Duration) {},
	}
	if err := prepared.Replace(); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, target, "new")
	assertFileContent(t, target+".previous", "old")
	if mode := fileMode(t, target); mode != 0o755 {
		t.Fatalf("new binary mode = %o", mode)
	}
	if err := prepared.Restart([]string{"bridge", "serve"}, []string{"A=B"}); !errors.Is(err, execErr) {
		t.Fatalf("Restart() error = %v", err)
	}
	assertFileContent(t, target, "old")
}

func TestPreparedAbortRemovesOnlyUninstalledStage(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".staged")
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared := &Prepared{staged: path}
	prepared.Abort()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file still exists: %v", err)
	}
	prepared.Abort()
}

// detachRestart is the production default and the exact spot that used to drift
// (syscall.Exec in a goroutine failing silently). These lock in that it (a)
// launches the new binary as a real process and exits the current one on
// success, and (b) surfaces the error instead of swallowing it on failure.
func TestDetachRestartStartsProcessAndExits(t *testing.T) {
	bin := "/bin/true"
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no %s on this host", bin)
	}
	prevExit := osExit
	exitCode := -1
	osExit = func(code int) { exitCode = code }
	t.Cleanup(func() { osExit = prevExit })

	if err := detachRestart(bin, []string{bin}, []string{"A=B"}); err != nil {
		t.Fatalf("detachRestart() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("osExit code = %d, want 0", exitCode)
	}
}

func TestDetachRestartRequestsSystemdRestart(t *testing.T) {
	bin := "/bin/true"
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no %s on this host", bin)
	}
	prevExit := osExit
	exitCode := -1
	osExit = func(code int) { exitCode = code }
	t.Cleanup(func() { osExit = prevExit })

	env := []string{"A=B", "INVOCATION_ID=systemd-invocation"}
	if err := detachRestart(bin, []string{bin}, env); err != nil {
		t.Fatalf("detachRestart() error = %v", err)
	}
	if exitCode != systemdRestartExitCode {
		t.Fatalf("osExit code = %d, want %d", exitCode, systemdRestartExitCode)
	}
}

func TestEnvContainsNonEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want bool
	}{
		{name: "present", env: []string{"INVOCATION_ID=abc"}, want: true},
		{name: "empty", env: []string{"INVOCATION_ID="}, want: false},
		{name: "missing", env: []string{"OTHER=abc"}, want: false},
		{name: "prefix only", env: []string{"INVOCATION_ID_EXTRA=abc"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := envContainsNonEmpty(tc.env, "INVOCATION_ID"); got != tc.want {
				t.Fatalf("envContainsNonEmpty() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetachRestartReturnsErrorWhenLaunchFails(t *testing.T) {
	prevExit := osExit
	osExit = func(int) { t.Fatal("osExit must not be called when launch fails") }
	t.Cleanup(func() { osExit = prevExit })

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := detachRestart(missing, []string{missing}, nil); err == nil {
		t.Fatal("detachRestart() error = nil, want launch failure")
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
