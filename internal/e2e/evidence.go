package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"lark-agent-bridge/internal/security"
)

var evidenceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Evidence struct {
	mu          sync.Mutex
	dir         string
	cardsDir    string
	actionsPath string
	auditPath   string
}

func NewEvidence(root, runID string, deployment Deployment, scenario string) (*Evidence, error) {
	if !evidenceNamePattern.MatchString(runID) {
		return nil, fmt.Errorf("unsafe evidence run id")
	}
	if !evidenceNamePattern.MatchString(scenario) {
		return nil, fmt.Errorf("unsafe scenario name")
	}
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("evidence root must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create evidence root: %w", err)
	}
	dir := filepath.Join(root, runID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create evidence run: %w", err)
	}
	cardsDir := filepath.Join(dir, "cards")
	if err := os.Mkdir(cardsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create evidence cards: %w", err)
	}
	evidence := &Evidence{
		dir:         dir,
		cardsDir:    cardsDir,
		actionsPath: filepath.Join(dir, "actions.jsonl"),
		auditPath:   filepath.Join(dir, "audit.jsonl"),
	}
	if err := evidence.writeJSON("deployment.json", deployment); err != nil {
		return nil, fmt.Errorf("write deployment evidence: %w", err)
	}
	scenarioDoc := struct {
		SchemaVersion int    `json:"schema_version"`
		Name          string `json:"name"`
	}{SchemaVersion: 1, Name: scenario}
	if err := evidence.writeJSON("scenario.json", scenarioDoc); err != nil {
		return nil, fmt.Errorf("write scenario evidence: %w", err)
	}
	return evidence, nil
}

func (e *Evidence) Dir() string {
	return e.dir
}

func (e *Evidence) AppendAction(record ActionRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal action: %w", err)
	}
	return e.appendJSONL(e.actionsPath, data)
}

func (e *Evidence) AppendAudit(raw json.RawMessage) error {
	if !json.Valid(raw) {
		return fmt.Errorf("audit evidence is not valid JSON")
	}
	return e.appendJSONL(e.auditPath, raw)
}

func (e *Evidence) WriteCard(id string, raw []byte) error {
	if !evidenceNamePattern.MatchString(id) {
		return fmt.Errorf("unsafe card evidence name")
	}
	if !json.Valid(raw) {
		return fmt.Errorf("card evidence is not valid JSON")
	}
	clean := []byte(security.Redact(string(raw)))
	return writeAtomic(filepath.Join(e.cardsDir, id+".json"), clean, 0o600)
}

func (e *Evidence) Finish(result Result) error {
	result.SchemaVersion = 1
	return e.writeJSON("result.json", result)
}

func (e *Evidence) writeJSON(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	data = []byte(security.Redact(string(data)))
	return writeAtomic(filepath.Join(e.dir, name), data, 0o600)
}

func (e *Evidence) appendJSONL(path string, raw []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	clean := []byte(security.Redact(string(raw)))
	clean = append(clean, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(clean); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func writeAtomic(path string, data []byte, mode os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".evidence-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	return dirFile.Sync()
}
