package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReleaseTestEvidenceCreatesReusesAndRejectsTamperedLog(t *testing.T) {
	repo, goBin, countFile := releaseEvidenceFixture(t)
	t.Chdir(repo)
	t.Setenv("LAB_RELEASE_GO_BIN", goBin)
	t.Setenv("FAKE_GO_COUNT", countFile)
	t.Setenv("FAKE_GO_ROOT", filepath.Join(filepath.Dir(goBin), "toolchain"))
	t.Setenv("E2E_PREFERENCE_STORE", "/live/preferences.json")
	t.Setenv("E2E_REPLY_STORE", "/live/replies.json")
	t.Setenv("E2E_MEDIA_CACHE_DIR", "/live/media")
	t.Setenv("E2E_SESSION_STORE", "/live/sessions.json")
	t.Setenv("GOCACHE", filepath.Join(repo, ".cache-a"))

	first, err := ensureReleaseTestEvidence(&bytes.Buffer{})
	if err != nil || first.Status != "created" {
		t.Fatalf("first ensure = %#v, %v", first, err)
	}
	t.Setenv("GOCACHE", filepath.Join(repo, ".cache-b"))
	second, err := ensureReleaseTestEvidence(&bytes.Buffer{})
	if err != nil || second.Status != "reused" || second.Path != first.Path {
		t.Fatalf("second ensure = %#v, %v", second, err)
	}
	if got := releaseEvidenceRunCount(t, countFile); got != 1 {
		t.Fatalf("go test runs = %d, want 1", got)
	}

	evidence := readReleaseTestEvidence(t, first.Path)
	if err := os.WriteFile(evidence.LogFile, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := ensureReleaseTestEvidence(&bytes.Buffer{})
	if err != nil || third.Status != "created" {
		t.Fatalf("ensure after log tamper = %#v, %v", third, err)
	}
	if got := releaseEvidenceRunCount(t, countFile); got != 2 {
		t.Fatalf("go test runs after tamper = %d, want 2", got)
	}
	for _, path := range []string{third.Path, readReleaseTestEvidence(t, third.Path).LogFile} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("private mode for %s = %v, %v", path, info.Mode().Perm(), statErr)
		}
	}
}

func TestReleaseTestEvidenceInvalidatesToolchainAndRejectsDirtyWorktree(t *testing.T) {
	repo, goBin, countFile := releaseEvidenceFixture(t)
	t.Chdir(repo)
	t.Setenv("LAB_RELEASE_GO_BIN", goBin)
	t.Setenv("FAKE_GO_COUNT", countFile)
	t.Setenv("FAKE_GO_ROOT", filepath.Join(filepath.Dir(goBin), "toolchain"))

	first, err := ensureReleaseTestEvidence(&bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(goBin, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n# toolchain changed\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := ensureReleaseTestEvidence(&bytes.Buffer{})
	if err != nil || second.Status != "created" || second.Fingerprint == first.Fingerprint {
		t.Fatalf("ensure after toolchain change = %#v, %v", second, err)
	}
	if got := releaseEvidenceRunCount(t, countFile); got != 2 {
		t.Fatalf("go test runs after toolchain change = %d, want 2", got)
	}

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureReleaseTestEvidence(&bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "clean worktree") {
		t.Fatalf("dirty worktree error = %v", err)
	}
	if got := releaseEvidenceRunCount(t, countFile); got != 2 {
		t.Fatalf("dirty worktree ran tests: %d", got)
	}
	releaseEvidenceGit(t, repo, "add", "README.md")
	releaseEvidenceGit(t, repo, "commit", "-qm", "test: next clean commit")
	third, err := ensureReleaseTestEvidence(&bytes.Buffer{})
	if err != nil || third.Status != "created" || third.Fingerprint == second.Fingerprint {
		t.Fatalf("ensure after clean commit change = %#v, %v", third, err)
	}
	if got := releaseEvidenceRunCount(t, countFile); got != 3 {
		t.Fatalf("go test runs after commit change = %d, want 3", got)
	}
}

func TestRunTagCreatesEvidenceThatEnsureReuses(t *testing.T) {
	repo, goBin, countFile := releaseEvidenceFixture(t)
	t.Chdir(repo)
	t.Setenv("LAB_RELEASE_GO_BIN", goBin)
	t.Setenv("FAKE_GO_COUNT", countFile)
	t.Setenv("FAKE_GO_ROOT", filepath.Join(filepath.Dir(goBin), "toolchain"))

	note := filepath.Join(repo, "docs", "releases", "v1.0.0.md")
	if err := os.MkdirAll(filepath.Dir(note), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("# v1.0.0\n\n## Features\n\n- fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	releaseEvidenceGit(t, repo, "add", "docs/releases/v1.0.0.md")
	releaseEvidenceGit(t, repo, "commit", "-qm", "docs: add release note")

	if err := runTag([]string{"v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	outcome, err := ensureReleaseTestEvidence(&bytes.Buffer{})
	if err != nil || outcome.Status != "reused" {
		t.Fatalf("ensure after tag = %#v, %v", outcome, err)
	}
	if got := releaseEvidenceRunCount(t, countFile); got != 1 {
		t.Fatalf("tag + ensure go test runs = %d, want 1", got)
	}
	tagType := exec.Command("git", "-C", repo, "cat-file", "-t", "v1.0.0")
	output, err := tagType.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "tag" {
		t.Fatalf("tag type = %q, %v", output, err)
	}
}

func TestReleaseTestEvidenceFailureDoesNotCreatePassingReceipt(t *testing.T) {
	repo, goBin, countFile := releaseEvidenceFixture(t)
	t.Chdir(repo)
	t.Setenv("LAB_RELEASE_GO_BIN", goBin)
	t.Setenv("FAKE_GO_COUNT", countFile)
	t.Setenv("FAKE_GO_ROOT", filepath.Join(filepath.Dir(goBin), "toolchain"))
	t.Setenv("FAKE_GO_FAIL", "1")

	if _, err := ensureReleaseTestEvidence(&bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "go test ./... failed") {
		t.Fatalf("failure error = %v", err)
	}
	evidenceDir, err := releaseTestEvidenceDir()
	if err != nil {
		t.Fatal(err)
	}
	jsonFiles, err := filepath.Glob(filepath.Join(evidenceDir, "*.json"))
	if err != nil || len(jsonFiles) != 0 {
		t.Fatalf("passing evidence after failure = %v, %v", jsonFiles, err)
	}
	logs, err := filepath.Glob(filepath.Join(evidenceDir, "*.log"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("diagnostic logs after failure = %v, %v", logs, err)
	}
}

// go test 通过但 testfw regression all_passed=false 时,evidence 必须失败,
// 不能落下 passing 的 receipt。sidecar 是独立于 go test exit code 的失败信号。
func TestReleaseTestEvidenceTestfwFailureBlocksPassingReceipt(t *testing.T) {
	repo, goBin, countFile := releaseEvidenceFixture(t)
	t.Chdir(repo)
	t.Setenv("LAB_RELEASE_GO_BIN", goBin)
	t.Setenv("FAKE_GO_COUNT", countFile)
	t.Setenv("FAKE_GO_ROOT", filepath.Join(filepath.Dir(goBin), "toolchain"))
	t.Setenv("FAKE_TESTFW_FAIL", "1")

	_, err := ensureReleaseTestEvidence(&bytes.Buffer{})
	if err == nil {
		t.Fatal("expected ensure to fail when testfw all_passed=false")
	}
	msg := err.Error()
	if !strings.Contains(msg, "testfw") && !strings.Contains(msg, "lark-bridge-test") {
		t.Fatalf("expected testfw-related error, got %v", err)
	}
	// go test 本身跑成功过,run count 应为 1(sidecar path 不计数)。
	if got := releaseEvidenceRunCount(t, countFile); got != 1 {
		t.Fatalf("go test runs = %d, want 1", got)
	}
	evidenceDir, err := releaseTestEvidenceDir()
	if err != nil {
		t.Fatal(err)
	}
	// 主 evidence json 不应被写入(只有全绿才写)。
	jsonFiles, err := filepath.Glob(filepath.Join(evidenceDir, "*-testfw-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jsonFiles) != 1 {
		t.Fatalf("testfw sidecar count = %d, want 1", len(jsonFiles))
	}
	mainEvidence, err := filepath.Glob(filepath.Join(evidenceDir, "*[0-9a-f].json"))
	if err != nil {
		t.Fatal(err)
	}
	// 排除 sidecar,主 evidence 应为空。
	remaining := 0
	for _, p := range mainEvidence {
		if !strings.HasSuffix(p, "-testfw-report.json") {
			remaining++
		}
	}
	if remaining != 0 {
		t.Fatalf("main evidence written despite testfw failure: %v", mainEvidence)
	}
}

func TestReleaseTestEvidenceConcurrentEnsureRunsTestsOnce(t *testing.T) {
	repo, goBin, countFile := releaseEvidenceFixture(t)
	t.Chdir(repo)
	t.Setenv("LAB_RELEASE_GO_BIN", goBin)
	t.Setenv("FAKE_GO_COUNT", countFile)
	t.Setenv("FAKE_GO_ROOT", filepath.Join(filepath.Dir(goBin), "toolchain"))
	t.Setenv("FAKE_GO_SLEEP", "0.3")

	type result struct {
		outcome releaseTestEvidenceOutcome
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outcome, err := ensureReleaseTestEvidence(&bytes.Buffer{})
			results <- result{outcome: outcome, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	statuses := map[string]int{}
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		statuses[result.outcome.Status]++
	}
	if statuses["created"] != 1 || statuses["reused"] != 1 {
		t.Fatalf("concurrent statuses = %#v", statuses)
	}
	if got := releaseEvidenceRunCount(t, countFile); got != 1 {
		t.Fatalf("concurrent go test runs = %d, want 1", got)
	}
}

func TestReleaseTestEvidenceLockRemovalPreservesReplacementOwner(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "evidence.lock")
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockPath, "owner"), []byte("new-owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	releaseEvidenceLock(lockPath, "old-owner\n")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("replacement lock was removed: %v", err)
	}
	releaseEvidenceLock(lockPath, "new-owner\n")
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("owned lock still exists: %v", err)
	}
}

func releaseEvidenceFixture(t *testing.T) (repo, goBin, countFile string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	releaseEvidenceGit(t, repo, "init", "-q")
	releaseEvidenceGit(t, repo, "config", "user.name", "release-test")
	releaseEvidenceGit(t, repo, "config", "user.email", "release-test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	releaseEvidenceGit(t, repo, "add", "README.md")
	releaseEvidenceGit(t, repo, "commit", "-qm", "test: seed")

	binDir := t.TempDir()
	goBin = filepath.Join(binDir, "go")
	countFile = filepath.Join(binDir, "count")
	toolchainDir := filepath.Join(binDir, "toolchain")
	toolDir := filepath.Join(toolchainDir, "pkg", "tool")
	if err := os.MkdirAll(filepath.Join(toolchainDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(toolchainDir, "bin", "go"): "selected go fixture\n",
		filepath.Join(toolDir, "compile"):        "compiler fixture\n",
		filepath.Join(toolDir, "link"):           "linker fixture\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  version)
    echo 'go version go1.26.3 fixture/arch'
    ;;
  env)
    cat <<JSON
{"CGO_ENABLED":"1","GOARCH":"arm64","GOEXPERIMENT":"","GOFLAGS":"","GOOS":"darwin","GOROOT":"$FAKE_GO_ROOT","GOTOOLDIR":"$FAKE_GO_ROOT/pkg/tool","GOTOOLCHAIN":"auto","GOVERSION":"go1.26.3","GOWORK":""}
JSON
    ;;
  test)
    for name in E2E_PREFERENCE_STORE E2E_REPLY_STORE E2E_MEDIA_CACHE_DIR E2E_SESSION_STORE; do
      if [ -n "${!name+x}" ]; then
        echo "polluted test env: $name" >&2
        exit 42
      fi
    done
    echo run >>"$FAKE_GO_COUNT"
    sleep "${FAKE_GO_SLEEP:-0}"
    [ "${FAKE_GO_FAIL:-0}" = 0 ] || exit 23
    echo 'ok fixture'
    ;;
  run)
    # evidence.go 在 go test 通过后额外调 go run ./cmd/lark-bridge-test ... --report-json path
    # 生成 testfw regression sidecar。fixture 里没有真正的 bridge 源码,只需伪造出
    # 合法的 all_passed=true 报告即可让主流程继续。此路径不计入 FAKE_GO_COUNT。
    shift # 丢掉 "./cmd/lark-bridge-test"
    report_path=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --report-json)
          report_path="$2"; shift 2 ;;
        --report-json=*)
          report_path="${1#--report-json=}"; shift ;;
        *)
          shift ;;
      esac
    done
    if [ -z "$report_path" ]; then
      echo "fake go run: missing --report-json" >&2
      exit 3
    fi
    if [ "${FAKE_TESTFW_FAIL:-0}" != 0 ]; then
      printf '{"schema_version":1,"all_passed":false,"total":1,"passed":0,"failed":1}\n' > "$report_path"
      exit 1
    fi
    printf '{"schema_version":1,"all_passed":true,"total":1,"passed":1,"failed":0}\n' > "$report_path"
    ;;
  *)
    echo "unexpected fake go args: $*" >&2
    exit 2
    ;;
esac
`
	if err := os.WriteFile(goBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, goBin, countFile
}

func releaseEvidenceGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func releaseEvidenceRunCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errorsIsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run\n")
}

func errorsIsNotExist(err error) bool { return err != nil && os.IsNotExist(err) }

func readReleaseTestEvidence(t *testing.T, path string) releaseTestEvidence {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var evidence releaseTestEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.SchemaVersion != releaseTestEvidenceSchema {
		t.Fatal(fmt.Errorf("schema version = %d", evidence.SchemaVersion))
	}
	return evidence
}
