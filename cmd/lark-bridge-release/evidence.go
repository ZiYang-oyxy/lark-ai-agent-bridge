package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const (
	// schema v2:additionally captures testfw regression report sidecar
	// (all_passed + report path + SHA). identity/suite/command 保持不变以
	// 让 fingerprint 稳定,老 evidence 因 schema 不匹配自然失效重跑。
	releaseTestEvidenceSchema      = 2
	releaseTestSuite               = "release-l1-v1"
	releaseTestEnvironmentContract = "unset-runtime-state-v1"
	releaseTestLockStaleAfter      = 15 * time.Minute
	releaseTestLockWait            = 10 * time.Minute
)

var releaseRuntimePathVariables = []string{
	"E2E_PREFERENCE_STORE",
	"E2E_REPLY_STORE",
	"E2E_MEDIA_CACHE_DIR",
	"E2E_SESSION_STORE",
}

type releaseTestToolchain struct {
	GoBinary               string            `json:"go_binary"`
	GoBinarySHA256         string            `json:"go_binary_sha256"`
	SelectedGoBinary       string            `json:"selected_go_binary"`
	SelectedGoBinarySHA256 string            `json:"selected_go_binary_sha256"`
	CompilerBinarySHA256   string            `json:"compiler_binary_sha256"`
	LinkerBinarySHA256     string            `json:"linker_binary_sha256"`
	GoVersion              string            `json:"go_version"`
	GoEnv                  map[string]string `json:"go_env"`
}

type releaseTestIdentity struct {
	Suite               string               `json:"suite"`
	EnvironmentContract string               `json:"environment_contract"`
	Commit              string               `json:"commit"`
	Tree                string               `json:"tree"`
	Command             []string             `json:"command"`
	Toolchain           releaseTestToolchain `json:"toolchain"`
}

type releaseTestEvidence struct {
	SchemaVersion int                 `json:"schema_version"`
	Fingerprint   string              `json:"fingerprint"`
	Identity      releaseTestIdentity `json:"identity"`
	Result        string              `json:"result"`
	ExitCode      int                 `json:"exit_code"`
	StartedAt     string              `json:"started_at"`
	FinishedAt    string              `json:"finished_at"`
	LogFile       string              `json:"log_file"`
	LogSHA256     string              `json:"log_sha256"`

	// testfw regression sidecar(schema v2 起写入):在 `go test ./...` 通过后额外
	// 跑 lark-bridge-test --tags regression 生成的结构化报告。all_passed=false
	// 视为 evidence 独立失败信号,不依赖 `go test` exit code。
	// TestfwReport 是 sidecar 的绝对路径(位于 evidenceDir 内),TestfwReportSHA256
	// 用来在 reuse 时校验文件未被篡改。TestfwAllPassed 是 sidecar 里 Report.AllPassed
	// 的直接映射,写入 evidence 时必为 true(否则 ensure 已在写入前失败退出)。
	TestfwReport       string `json:"testfw_report,omitempty"`
	TestfwReportSHA256 string `json:"testfw_report_sha256,omitempty"`
	TestfwAllPassed    bool   `json:"testfw_all_passed,omitempty"`
}

type releaseTestEvidenceOutcome struct {
	Status      string
	Path        string
	Fingerprint string
}

func runTestEvidence(args []string) error {
	if len(args) != 1 || args[0] != "ensure" {
		return errors.New("usage: lark-bridge-release test-evidence ensure")
	}
	_, err := ensureReleaseTestEvidence(os.Stdout)
	return err
}

func ensureReleaseTestEvidence(out io.Writer) (releaseTestEvidenceOutcome, error) {
	if err := requireCleanWorktree(); err != nil {
		return releaseTestEvidenceOutcome{}, err
	}
	identity, err := currentReleaseTestIdentity()
	if err != nil {
		return releaseTestEvidenceOutcome{}, err
	}
	fingerprint, err := releaseTestFingerprint(identity)
	if err != nil {
		return releaseTestEvidenceOutcome{}, err
	}
	evidenceDir, err := releaseTestEvidenceDir()
	if err != nil {
		return releaseTestEvidenceOutcome{}, err
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		return releaseTestEvidenceOutcome{}, err
	}
	if err := os.Chmod(evidenceDir, 0o700); err != nil {
		return releaseTestEvidenceOutcome{}, err
	}

	base := identity.Commit + "-" + fingerprint
	evidencePath := filepath.Join(evidenceDir, base+".json")
	lockPath := evidencePath + ".lock"
	deadline := time.Now().Add(releaseTestLockWait)
	waiting := false
	for {
		if validReleaseTestEvidence(evidencePath, evidenceDir, identity, fingerprint) == nil {
			outcome := releaseTestEvidenceOutcome{Status: "reused", Path: evidencePath, Fingerprint: fingerprint}
			fmt.Fprintf(out, "TEST_EVIDENCE_REUSED path=%s commit=%s fingerprint=%s\n", evidencePath, identity.Commit, fingerprint)
			return outcome, nil
		}
		if err := os.Mkdir(lockPath, 0o700); err == nil {
			break
		} else if !errors.Is(err, os.ErrExist) {
			return releaseTestEvidenceOutcome{}, fmt.Errorf("acquire release test evidence lock: %w", err)
		}
		removed, err := removeStaleEvidenceLock(lockPath, time.Now())
		if err != nil {
			return releaseTestEvidenceOutcome{}, err
		}
		if removed {
			continue
		}
		if time.Now().After(deadline) {
			return releaseTestEvidenceOutcome{}, fmt.Errorf("timed out waiting for release test evidence lock: %s", lockPath)
		}
		if !waiting {
			fmt.Fprintf(out, "[release-test] waiting for matching evidence: %s\n", fingerprint)
			waiting = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	lockOwner := fmt.Sprintf("pid=%d nonce=%d\n", os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(lockPath, "owner"), []byte(lockOwner), 0o600); err != nil {
		_ = os.RemoveAll(lockPath)
		return releaseTestEvidenceOutcome{}, fmt.Errorf("write release test evidence lock owner: %w", err)
	}
	defer releaseEvidenceLock(lockPath, lockOwner)

	// A concurrent process may have completed between the last read and lock acquisition.
	if validReleaseTestEvidence(evidencePath, evidenceDir, identity, fingerprint) == nil {
		outcome := releaseTestEvidenceOutcome{Status: "reused", Path: evidencePath, Fingerprint: fingerprint}
		fmt.Fprintf(out, "TEST_EVIDENCE_REUSED path=%s commit=%s fingerprint=%s\n", evidencePath, identity.Commit, fingerprint)
		return outcome, nil
	}

	started := time.Now().UTC()
	logFile, err := os.CreateTemp(evidenceDir, fmt.Sprintf("%s-%s-%d-*.log", base, started.Format("20060102T150405Z"), os.Getpid()))
	if err != nil {
		return releaseTestEvidenceOutcome{}, fmt.Errorf("create release test log: %w", err)
	}
	logPath := logFile.Name()
	writer := io.MultiWriter(out, logFile)
	cmd := exec.Command(identity.Toolchain.GoBinary, "test", "./...")
	cmd.Env = sanitizedReleaseTestEnvironment()
	cmd.Stdout, cmd.Stderr = writer, writer
	runErr := cmd.Run()
	closeErr := logFile.Close()
	if runErr != nil {
		return releaseTestEvidenceOutcome{}, fmt.Errorf("go test ./... failed (log %s): %w", logPath, runErr)
	}
	if closeErr != nil {
		return releaseTestEvidenceOutcome{}, fmt.Errorf("close release test log: %w", closeErr)
	}
	logSHA, err := fileSHA256(logPath)
	if err != nil {
		return releaseTestEvidenceOutcome{}, err
	}

	// testfw regression sidecar:在 `go test ./...` 通过后跑 lark-bridge-test
	// --tags regression 生成结构化报告,让发布凭证纳入 simulate 层跨命令的
	// end-to-end 断言。all_passed=false 视为独立失败信号(不改主 fingerprint)。
	// smoke 已在 go test ./... 里通过 TestSmokeSuite 强跑,此处只补 regression tag。
	sidecarPath := filepath.Join(evidenceDir, base+"-testfw-report.json")
	// 老文件残留会污染 sha 校验,先清掉再让子进程原子写。
	if err := os.Remove(sidecarPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return releaseTestEvidenceOutcome{}, fmt.Errorf("clean stale testfw sidecar: %w", err)
	}
	testfwCmd := exec.Command(identity.Toolchain.GoBinary, "run", "./cmd/lark-bridge-test",
		"--source", ".",
		"--go-bin", identity.Toolchain.GoBinary,
		"--tags", "regression",
		"--report-json", sidecarPath,
	)
	testfwCmd.Env = sanitizedReleaseTestEnvironment()
	testfwCmd.Stdout, testfwCmd.Stderr = out, out
	if err := testfwCmd.Run(); err != nil {
		return releaseTestEvidenceOutcome{}, fmt.Errorf("lark-bridge-test regression failed: %w", err)
	}
	testfwAllPassed, err := readTestfwAllPassed(sidecarPath)
	if err != nil {
		return releaseTestEvidenceOutcome{}, fmt.Errorf("read testfw sidecar: %w", err)
	}
	if !testfwAllPassed {
		return releaseTestEvidenceOutcome{}, fmt.Errorf("testfw regression reported all_passed=false (sidecar %s)", sidecarPath)
	}
	if err := os.Chmod(sidecarPath, 0o600); err != nil {
		return releaseTestEvidenceOutcome{}, err
	}
	sidecarSHA, err := fileSHA256(sidecarPath)
	if err != nil {
		return releaseTestEvidenceOutcome{}, err
	}

	evidence := releaseTestEvidence{
		SchemaVersion: releaseTestEvidenceSchema,
		Fingerprint:   fingerprint,
		Identity:      identity,
		Result:        "passed",
		ExitCode:      0,
		StartedAt:     started.Format(time.RFC3339),
		FinishedAt:    time.Now().UTC().Format(time.RFC3339),
		LogFile:       logPath,
		LogSHA256:     logSHA,

		TestfwReport:       sidecarPath,
		TestfwReportSHA256: sidecarSHA,
		TestfwAllPassed:    true,
	}
	if err := writeJSONAtomic(evidencePath, evidence); err != nil {
		return releaseTestEvidenceOutcome{}, fmt.Errorf("write release test evidence: %w", err)
	}
	if err := os.Chmod(evidencePath, 0o600); err != nil {
		return releaseTestEvidenceOutcome{}, err
	}
	if err := validReleaseTestEvidence(evidencePath, evidenceDir, identity, fingerprint); err != nil {
		return releaseTestEvidenceOutcome{}, fmt.Errorf("validate written release test evidence: %w", err)
	}
	outcome := releaseTestEvidenceOutcome{Status: "created", Path: evidencePath, Fingerprint: fingerprint}
	fmt.Fprintf(out, "TEST_EVIDENCE_CREATED path=%s commit=%s fingerprint=%s\n", evidencePath, identity.Commit, fingerprint)
	return outcome, nil
}

func currentReleaseTestIdentity() (releaseTestIdentity, error) {
	commit, err := gitOutput("rev-parse", "HEAD")
	if err != nil {
		return releaseTestIdentity{}, err
	}
	tree, err := gitOutput("rev-parse", "HEAD^{tree}")
	if err != nil {
		return releaseTestIdentity{}, err
	}
	toolchain, err := currentReleaseTestToolchain()
	if err != nil {
		return releaseTestIdentity{}, err
	}
	return releaseTestIdentity{
		Suite:               releaseTestSuite,
		EnvironmentContract: releaseTestEnvironmentContract,
		Commit:              strings.TrimSpace(commit),
		Tree:                strings.TrimSpace(tree),
		Command:             []string{"go", "test", "./..."},
		Toolchain:           toolchain,
	}, nil
}

func currentReleaseTestToolchain() (releaseTestToolchain, error) {
	goName := os.Getenv("LAB_RELEASE_GO_BIN")
	if goName == "" {
		goName = "go"
	}
	goPath, err := exec.LookPath(goName)
	if err != nil {
		return releaseTestToolchain{}, fmt.Errorf("resolve go binary: %w", err)
	}
	goPath, err = filepath.Abs(goPath)
	if err != nil {
		return releaseTestToolchain{}, err
	}
	if evaluated, evalErr := filepath.EvalSymlinks(goPath); evalErr == nil {
		goPath = evaluated
	}
	goSHA, err := fileSHA256(goPath)
	if err != nil {
		return releaseTestToolchain{}, fmt.Errorf("hash go binary: %w", err)
	}
	version, err := releaseCommandOutput(goPath, "version")
	if err != nil {
		return releaseTestToolchain{}, err
	}
	const envNames = "GOVERSION GOROOT GOTOOLDIR GOOS GOARCH CGO_ENABLED GOWORK GOFLAGS GOTOOLCHAIN GOEXPERIMENT"
	envJSON, err := releaseCommandOutput(goPath, append([]string{"env", "-json"}, strings.Fields(envNames)...)...)
	if err != nil {
		return releaseTestToolchain{}, err
	}
	goEnv := map[string]string{}
	if err := json.Unmarshal([]byte(envJSON), &goEnv); err != nil {
		return releaseTestToolchain{}, fmt.Errorf("decode go env: %w", err)
	}
	selectedGoPath, err := resolvedToolPath(filepath.Join(goEnv["GOROOT"], "bin", "go"))
	if err != nil {
		return releaseTestToolchain{}, fmt.Errorf("resolve selected go binary: %w", err)
	}
	selectedGoSHA, err := fileSHA256(selectedGoPath)
	if err != nil {
		return releaseTestToolchain{}, fmt.Errorf("hash selected go binary: %w", err)
	}
	compilerSHA, err := fileSHA256(filepath.Join(goEnv["GOTOOLDIR"], "compile"))
	if err != nil {
		return releaseTestToolchain{}, fmt.Errorf("hash selected go compiler: %w", err)
	}
	linkerSHA, err := fileSHA256(filepath.Join(goEnv["GOTOOLDIR"], "link"))
	if err != nil {
		return releaseTestToolchain{}, fmt.Errorf("hash selected go linker: %w", err)
	}
	return releaseTestToolchain{
		GoBinary:               goPath,
		GoBinarySHA256:         goSHA,
		SelectedGoBinary:       selectedGoPath,
		SelectedGoBinarySHA256: selectedGoSHA,
		CompilerBinarySHA256:   compilerSHA,
		LinkerBinarySHA256:     linkerSHA,
		GoVersion:              strings.TrimSpace(version),
		GoEnv:                  goEnv,
	}, nil
}

func resolvedToolPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty tool path")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if evaluated, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
		path = evaluated
	}
	return path, nil
}

func releaseCommandOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = sanitizedReleaseTestEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func sanitizedReleaseTestEnvironment() []string {
	env := os.Environ()
	filtered := env[:0]
	for _, item := range env {
		keep := true
		for _, name := range releaseRuntimePathVariables {
			if strings.HasPrefix(item, name+"=") {
				keep = false
				break
			}
		}
		if keep {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func releaseTestEvidenceDir() (string, error) {
	commonDir, err := gitOutput("rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	commonDir = strings.TrimSpace(commonDir)
	if !filepath.IsAbs(commonDir) {
		root, rootErr := gitOutput("rev-parse", "--show-toplevel")
		if rootErr != nil {
			return "", rootErr
		}
		commonDir = filepath.Join(strings.TrimSpace(root), commonDir)
	}
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(commonDir), "release-state", "test-evidence"), nil
}

func releaseTestFingerprint(identity releaseTestIdentity) (string, error) {
	data, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func validReleaseTestEvidence(path, evidenceDir string, identity releaseTestIdentity, fingerprint string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var evidence releaseTestEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		return err
	}
	if evidence.SchemaVersion != releaseTestEvidenceSchema || evidence.Fingerprint != fingerprint ||
		evidence.Result != "passed" || evidence.ExitCode != 0 || !reflect.DeepEqual(evidence.Identity, identity) {
		return errors.New("release test evidence identity or result mismatch")
	}
	if err := privateFile(path); err != nil {
		return err
	}
	logPath, err := filepath.Abs(evidence.LogFile)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(evidenceDir, logPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("release test evidence log escapes evidence directory")
	}
	if err := privateFile(logPath); err != nil {
		return err
	}
	gotLogSHA, err := fileSHA256(logPath)
	if err != nil {
		return err
	}
	if gotLogSHA != evidence.LogSHA256 {
		return errors.New("release test evidence log hash mismatch")
	}

	// testfw sidecar 校验(schema v2 起必须存在):evidence 记的路径必须在 evidenceDir 内、
	// SHA 匹配、all_passed=true。任一失败即视作 evidence 失效,触发重跑。
	if evidence.TestfwReport == "" || !evidence.TestfwAllPassed {
		return errors.New("release test evidence missing testfw sidecar")
	}
	sidecarPath, err := filepath.Abs(evidence.TestfwReport)
	if err != nil {
		return err
	}
	sidecarRel, err := filepath.Rel(evidenceDir, sidecarPath)
	if err != nil || sidecarRel == "." || sidecarRel == ".." || strings.HasPrefix(sidecarRel, ".."+string(filepath.Separator)) {
		return errors.New("release test evidence testfw sidecar escapes evidence directory")
	}
	if err := privateFile(sidecarPath); err != nil {
		return err
	}
	gotSidecarSHA, err := fileSHA256(sidecarPath)
	if err != nil {
		return err
	}
	if gotSidecarSHA != evidence.TestfwReportSHA256 {
		return errors.New("release test evidence testfw sidecar hash mismatch")
	}
	if allPassed, err := readTestfwAllPassed(sidecarPath); err != nil || !allPassed {
		if err != nil {
			return fmt.Errorf("release test evidence testfw sidecar unreadable: %w", err)
		}
		return errors.New("release test evidence testfw sidecar reports all_passed=false")
	}
	return nil
}

// readTestfwAllPassed 读取 testfw 报告 sidecar,只抽取 all_passed 字段。
// sidecar 结构定义在 internal/testfw/report.go,这里刻意不 import 该包以避免
// 发布凭证工具依赖测试框架实现。字段名与 Report.AllPassed 的 JSON tag 对齐。
func readTestfwAllPassed(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var probe struct {
		AllPassed bool `json:"all_passed"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false, err
	}
	return probe.AllPassed, nil
}

func privateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("release test evidence file is not private: %s", path)
	}
	return nil
}

func removeStaleEvidenceLock(path string, now time.Time) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if now.Sub(info.ModTime()) <= releaseTestLockStaleAfter {
		return false, nil
	}
	quarantine := fmt.Sprintf("%s.stale-%d-%d", path, os.Getpid(), now.UnixNano())
	if err := os.Rename(path, quarantine); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("quarantine stale release test evidence lock: %w", err)
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return false, fmt.Errorf("remove stale release test evidence lock: %w", err)
	}
	return true, nil
}

func releaseEvidenceLock(path, owner string) {
	data, err := os.ReadFile(filepath.Join(path, "owner"))
	if err == nil && string(data) == owner {
		_ = os.RemoveAll(path)
	}
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
