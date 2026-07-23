package devmode

import (
	"path/filepath"
	"testing"
)

func TestStoreNilIsOff(t *testing.T) {
	var s *Store
	if s.Prerelease() {
		t.Fatal("nil store must report prerelease off")
	}
	if s.Get() != (State{}) {
		t.Fatal("nil store must report zero state")
	}
	if _, err := s.SetPrerelease(true); err == nil {
		t.Fatal("SetPrerelease on nil store must error")
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev-mode.json")

	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Prerelease() {
		t.Fatal("fresh store must default to off")
	}
	if _, err := s.SetPrerelease(true); err != nil {
		t.Fatal(err)
	}

	// Reopen simulates the restart that an rc upgrade triggers: the flag must
	// survive, otherwise the prerelease channel would silently reset to stable.
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Prerelease() {
		t.Fatal("prerelease flag must persist across reopen")
	}

	if _, err := reopened.SetPrerelease(false); err != nil {
		t.Fatal(err)
	}
	again, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Prerelease() {
		t.Fatal("disabling prerelease must persist across reopen")
	}
}

func TestOpenStoreRejectsEmptyPath(t *testing.T) {
	if _, err := OpenStore("  "); err == nil {
		t.Fatal("empty path must error")
	}
}
