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
	releaseTestEvidenceSchema      = 1
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
	GoBinary       string            `json:"go_binary"`
	GoBinarySHA256 string            `json:"go_binary_sha256"`
	GoVersion      string            `json:"go_version"`
	GoEnv          map[string]string `json:"go_env"`
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
	const envNames = "GOVERSION GOOS GOARCH CGO_ENABLED GOWORK GOFLAGS GOTOOLCHAIN GOEXPERIMENT"
	envJSON, err := releaseCommandOutput(goPath, append([]string{"env", "-json"}, strings.Fields(envNames)...)...)
	if err != nil {
		return releaseTestToolchain{}, err
	}
	goEnv := map[string]string{}
	if err := json.Unmarshal([]byte(envJSON), &goEnv); err != nil {
		return releaseTestToolchain{}, fmt.Errorf("decode go env: %w", err)
	}
	return releaseTestToolchain{
		GoBinary:       goPath,
		GoBinarySHA256: goSHA,
		GoVersion:      strings.TrimSpace(version),
		GoEnv:          goEnv,
	}, nil
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
	return nil
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
