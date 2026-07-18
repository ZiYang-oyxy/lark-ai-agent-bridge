package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const PreferenceSchemaVersion = 1

type ReplyMode string

const (
	ReplyModeAppend          ReplyMode = "append"
	ReplyModeAppendCleanCard ReplyMode = "append-clean-card"
	ReplyModeLatestCard      ReplyMode = "latest-card"
)

var builtinModels = []string{"default", "sonnet", "opus", "haiku"}

var validEfforts = map[string]struct{}{
	"default": {},
	"low":     {},
	"medium":  {},
	"high":    {},
}

type RuntimePreference struct {
	Model     string    `json:"model"`
	Effort    string    `json:"effort"`
	ReplyMode ReplyMode `json:"reply_mode,omitempty"`
}

type preferenceSnapshot struct {
	SchemaVersion int                `json:"schema_version"`
	Revision      uint64             `json:"revision"`
	Override      *RuntimePreference `json:"override,omitempty"`
}

type PreferenceStore struct {
	mu            sync.RWMutex
	path          string
	defaults      RuntimePreference
	allowedModels []string
	revision      uint64
	override      *RuntimePreference
}

func OpenPreferenceStore(path string, defaults RuntimePreference, allowedModels []string) (*PreferenceStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config: empty preference store path")
	}
	defaults = normalizeRuntimePreference(defaults)
	models, err := modelCatalog(allowedModels)
	if err != nil {
		return nil, err
	}
	if err := validateRuntimePreference(defaults, models); err != nil {
		return nil, fmt.Errorf("validate runtime preference defaults: %w", err)
	}
	store := &PreferenceStore{path: path, defaults: defaults, allowedModels: models}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read preference store: %w", err)
	}
	var snapshot preferenceSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode preference store: %w", err)
	}
	if snapshot.SchemaVersion != PreferenceSchemaVersion {
		return nil, fmt.Errorf("unsupported preference schema %d", snapshot.SchemaVersion)
	}
	if snapshot.Override != nil {
		preference := normalizeRuntimePreference(*snapshot.Override)
		if err := validateRuntimePreference(preference, models); err != nil {
			return nil, fmt.Errorf("validate stored runtime preference: %w", err)
		}
		store.override = &preference
	}
	store.revision = snapshot.Revision
	return store, nil
}

func (s *PreferenceStore) Get() RuntimePreference {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.override != nil {
		return *s.override
	}
	return s.defaults
}

func (s *PreferenceStore) Set(preference RuntimePreference) error {
	preference = normalizeRuntimePreference(preference)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateRuntimePreference(preference, s.allowedModels); err != nil {
		return err
	}
	revision := s.revision + 1
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision, Override: &preference}); err != nil {
		return err
	}
	s.override = &preference
	s.revision = revision
	return nil
}

func (s *PreferenceStore) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision := s.revision + 1
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision}); err != nil {
		return err
	}
	s.override = nil
	s.revision = revision
	return nil
}

func ValidateRuntimePreference(preference RuntimePreference, allowedModels ...string) error {
	models, err := modelCatalog(allowedModels)
	if err != nil {
		return err
	}
	return validateRuntimePreference(normalizeRuntimePreference(preference), models)
}

func validateRuntimePreference(preference RuntimePreference, allowedModels []string) error {
	allowed := false
	for _, model := range allowedModels {
		if preference.Model == model {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("model %q is not allowed", preference.Model)
	}
	if _, ok := validEfforts[preference.Effort]; !ok {
		return fmt.Errorf("effort %q is not allowed", preference.Effort)
	}
	switch preference.ReplyMode {
	case ReplyModeAppend, ReplyModeAppendCleanCard, ReplyModeLatestCard:
	default:
		return fmt.Errorf("reply mode %q is not allowed", preference.ReplyMode)
	}
	return nil
}

func normalizeRuntimePreference(preference RuntimePreference) RuntimePreference {
	preference.Model = strings.TrimSpace(preference.Model)
	preference.Effort = strings.ToLower(strings.TrimSpace(preference.Effort))
	preference.ReplyMode = ReplyMode(strings.ToLower(strings.TrimSpace(string(preference.ReplyMode))))
	if preference.ReplyMode == "" {
		preference.ReplyMode = ReplyModeAppend
	}
	for _, model := range builtinModels {
		if strings.EqualFold(preference.Model, model) {
			preference.Model = model
			break
		}
	}
	return preference
}

func modelCatalog(additions []string) ([]string, error) {
	models := append([]string(nil), builtinModels...)
	seen := make(map[string]struct{}, len(models)+len(additions))
	for _, model := range models {
		seen[model] = struct{}{}
	}
	for _, addition := range additions {
		addition = strings.TrimSpace(addition)
		if !validModelName(addition) {
			return nil, fmt.Errorf("allowed model %q is invalid", addition)
		}
		if _, ok := seen[addition]; ok {
			continue
		}
		seen[addition] = struct{}{}
		models = append(models, addition)
	}
	return models, nil
}

func validModelName(model string) bool {
	if model == "" || len(model) > 128 {
		return false
	}
	for _, r := range model {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '-', '_', '.', ':', '/':
			continue
		default:
			return false
		}
	}
	return true
}

func savePreferenceSnapshot(path string, snapshot preferenceSnapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create preference directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode preference store: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".preferences-*.tmp")
	if err != nil {
		return fmt.Errorf("create preference temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set preference temp permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write preference store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync preference store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close preference store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace preference store: %w", err)
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}
