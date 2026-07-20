package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvidenceCreatesPrivateRedactedBundle(t *testing.T) {
	root := t.TempDir()
	evidence, err := NewEvidence(root, "run-1", testDeployment(), "stop")
	if err != nil {
		t.Fatal(err)
	}
	if mode := fileMode(t, evidence.Dir()); mode != 0o700 {
		t.Fatalf("dir mode = %o", mode)
	}

	secret := "token=super-private"
	if err := evidence.AppendAction(ActionRecord{Step: "send", State: "failed", Detail: secret}); err != nil {
		t.Fatal(err)
	}
	if err := evidence.AppendAudit(json.RawMessage(`{"authorization":"Bearer abc.def","action":"cardkit_update"}`)); err != nil {
		t.Fatal(err)
	}
	if err := evidence.WriteCard("reply-1", []byte(`{"authorization":"Bearer abc.def"}`)); err != nil {
		t.Fatal(err)
	}
	assertTreeContainsNoString(t, evidence.Dir(), "super-private")
	assertTreeContainsNoString(t, evidence.Dir(), "abc.def")
	assertAllRegularFilesMode(t, evidence.Dir(), 0o600)
}

func TestEvidenceFinishPreservesPrimaryAndCleanupFailures(t *testing.T) {
	evidence := newTestEvidence(t)
	result := Result{
		Scenario: "stop",
		Status:   "failed",
		Failure:  &Failure{Class: FailureAssertion, Step: "ready", Message: "marker missing"},
		CleanupFailures: []Failure{
			{Class: FailureCleanup, Step: "disarm", Message: "permission denied"},
		},
	}
	if err := evidence.Finish(result); err != nil {
		t.Fatal(err)
	}
	got := readResult(t, filepath.Join(evidence.Dir(), "result.json"))
	if got.SchemaVersion != 1 || got.Failure.Step != "ready" || len(got.CleanupFailures) != 1 || got.CleanupFailures[0].Step != "disarm" {
		t.Fatalf("result = %#v", got)
	}
}

func TestEvidenceRejectsUnsafeOrExistingRunID(t *testing.T) {
	root := t.TempDir()
	for _, runID := range []string{"../escape", "has space", "", strings.Repeat("x", 129)} {
		if _, err := NewEvidence(root, runID, testDeployment(), "stop"); err == nil {
			t.Fatalf("NewEvidence accepted %q", runID)
		}
	}
	if _, err := NewEvidence(root, "same-run", testDeployment(), "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEvidence(root, "same-run", testDeployment(), "stop"); err == nil {
		t.Fatal("NewEvidence reused an existing run")
	}
}

func TestEvidenceRejectsUnsafeCardNameAndMalformedAudit(t *testing.T) {
	evidence := newTestEvidence(t)
	if err := evidence.WriteCard("../outside", []byte(`{}`)); err == nil {
		t.Fatal("WriteCard accepted unsafe name")
	}
	if err := evidence.AppendAudit(json.RawMessage(`{`)); err == nil {
		t.Fatal("AppendAudit accepted malformed JSON")
	}
}

func testDeployment() Deployment {
	return Deployment{
		SchemaVersion: 1,
		Transaction:   "/state/deployments/txn.1",
		CandidatePID:  42,
		SourceCommit:  "abc123",
		BinarySHA256:  "sha256:abcd",
		Workspace:     "/workspace",
		StateDir:      "/state",
		FixtureDir:    "/state/fixtures",
	}
}

func newTestEvidence(t *testing.T) *Evidence {
	t.Helper()
	evidence, err := NewEvidence(t.TempDir(), "run-1", testDeployment(), "stop")
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func assertTreeContainsNoString(t *testing.T, root, forbidden string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), forbidden) {
			t.Errorf("%s contains forbidden value %q", path, forbidden)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertAllRegularFilesMode(t *testing.T, root string, want os.FileMode) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if got := fileMode(t, path); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readResult(t *testing.T, path string) Result {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
