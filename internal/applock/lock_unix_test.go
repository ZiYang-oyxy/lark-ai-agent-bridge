//go:build unix

package applock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAcquireRejectsSecondHolderForSameApp(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir, "cli_same")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := Acquire(dir, "cli_same"); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire error = %v", err)
	}
	other, err := Acquire(dir, "cli_other")
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	if matches, _ := filepath.Glob(filepath.Join(dir, "*cli_same*")); len(matches) != 0 {
		t.Fatalf("lock filename leaked App ID: %v", matches)
	}
}

func TestAcquireCanReuseLockAfterClose(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir, "cli_same")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir, "cli_same")
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
}
