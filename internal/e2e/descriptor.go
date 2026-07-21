package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	commitPattern     = regexp.MustCompile(`^[0-9a-fA-F]{6,64}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-fA-F]{4,64}$`)
	remoteHostPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+$`)
)

func LoadDeployment(path string) (Deployment, error) {
	var deployment Deployment
	if err := decodeStrictFile(path, &deployment); err != nil {
		return Deployment{}, fmt.Errorf("load deployment: %w", err)
	}
	if err := validateDeployment(deployment); err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

func LoadConfig(path string) (Config, error) {
	var config Config
	if err := decodeStrictFile(path, &config); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func decodeStrictFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func validateDeployment(deployment Deployment) error {
	if deployment.SchemaVersion != 1 {
		return fmt.Errorf("unsupported deployment schema %d", deployment.SchemaVersion)
	}
	if deployment.CandidatePID <= 0 {
		return fmt.Errorf("candidate_pid must be positive")
	}
	if !commitPattern.MatchString(deployment.SourceCommit) {
		return fmt.Errorf("source_commit must be a hexadecimal commit id")
	}
	if !digestPattern.MatchString(deployment.BinarySHA256) {
		return fmt.Errorf("binary_sha256 must use sha256:<hex>")
	}
	for name, value := range map[string]string{
		"transaction": deployment.Transaction,
		"workspace":   deployment.Workspace,
		"state_dir":   deployment.StateDir,
		"fixture_dir": deployment.FixtureDir,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return fmt.Errorf("%s must be a clean absolute path", name)
		}
	}
	if deployment.FixtureDir != filepath.Join(deployment.StateDir, "fixtures") {
		return fmt.Errorf("fixture_dir must be below state_dir")
	}
	transactionDir := filepath.Join(deployment.StateDir, "deployments")
	if filepath.Dir(deployment.Transaction) != transactionDir || !strings.HasPrefix(filepath.Base(deployment.Transaction), "txn.") {
		return fmt.Errorf("transaction must be an exact state deployment path")
	}
	return nil
}

func validateConfig(config Config) error {
	if config.SchemaVersion != 1 {
		return fmt.Errorf("unsupported config schema %d", config.SchemaVersion)
	}
	for name, value := range map[string]string{
		"profile":           config.Profile,
		"expected_bot_name": config.ExpectedBotName,
		"app_id":            config.AppID,
		"chat_id":           config.ChatID,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s must be non-empty and single-line", name)
		}
	}
	if !remoteHostPattern.MatchString(config.RemoteHost) {
		return fmt.Errorf("remote_host contains unsafe characters")
	}
	if !filepath.IsAbs(config.AuditPath) || filepath.Clean(config.AuditPath) != config.AuditPath {
		return fmt.Errorf("audit_path must be a clean absolute path")
	}
	if config.ControllerPath != "" && (!filepath.IsAbs(config.ControllerPath) || filepath.Clean(config.ControllerPath) != config.ControllerPath) {
		return fmt.Errorf("controller_path must be a clean absolute path")
	}
	if config.PollIntervalMS < 10 || config.PollIntervalMS > 5000 {
		return fmt.Errorf("poll_interval_ms must be between 10 and 5000")
	}
	if config.StepTimeoutMS < 1000 || config.StepTimeoutMS > 300000 {
		return fmt.Errorf("step_timeout_ms must be between 1000 and 300000")
	}
	return nil
}
