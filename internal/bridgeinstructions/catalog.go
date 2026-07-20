package bridgeinstructions

import (
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	CurrentVersion   = "v1"
	runtimeDirPrefix = "lark-bridge-instructions-"
)

//go:embed bridge-instructions/*.md
var resources embed.FS

var files = map[string]string{
	"v1": "bridge-instructions/feishu-runtime-v1.md",
}

// Content returns the immutable embedded instructions for version.
func Content(version string) (string, bool) {
	name, ok := files[version]
	if !ok {
		return "", false
	}
	data, err := resources.ReadFile(name)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Runtime owns the process-private Claude instruction files.
type Runtime struct {
	mu       sync.RWMutex
	dir      string
	contents map[string]string
	paths    map[string]string
}

// NewRuntime materializes every embedded instruction version once.
func NewRuntime() (*Runtime, error) {
	catalog := make(map[string]string, len(files))
	for version := range files {
		content, ok := Content(version)
		if !ok {
			return nil, fmt.Errorf("read embedded bridge instructions version %q", version)
		}
		catalog[version] = content
	}
	return newRuntime(catalog)
}

func newRuntime(catalog map[string]string) (_ *Runtime, resultErr error) {
	if len(catalog) == 0 {
		return nil, errors.New("bridge instructions catalog is empty")
	}
	dir, err := os.MkdirTemp("", runtimeDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("create bridge instructions directory: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("protect bridge instructions directory: %w", err)
	}

	versions := make([]string, 0, len(catalog))
	for version := range catalog {
		if !safeVersion(version) {
			return nil, fmt.Errorf("invalid bridge instructions version %q", version)
		}
		versions = append(versions, version)
	}
	sort.Strings(versions)

	rt := &Runtime{
		dir:      dir,
		contents: make(map[string]string, len(catalog)),
		paths:    make(map[string]string, len(catalog)),
	}
	for _, version := range versions {
		content := catalog[version]
		path := filepath.Join(dir, "feishu-runtime-"+version+".md")
		if err := writeVerified(path, []byte(content)); err != nil {
			return nil, fmt.Errorf("materialize bridge instructions version %q: %w", version, err)
		}
		rt.contents[version] = content
		rt.paths[version] = path
	}
	return rt, nil
}

// Content resolves a version from this Runtime's immutable catalog.
func (r *Runtime) Content(version string) (string, error) {
	if r == nil {
		return "", errors.New("bridge instructions runtime is nil")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	content, ok := r.contents[version]
	if !ok {
		return "", unsupportedVersion(version)
	}
	return content, nil
}

// ClaudeFile returns the stable private file for version.
func (r *Runtime) ClaudeFile(version string) (string, error) {
	if r == nil {
		return "", errors.New("bridge instructions runtime is nil")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	path, ok := r.paths[version]
	if !ok {
		return "", unsupportedVersion(version)
	}
	return path, nil
}

// Close removes only the exact process-private directory created by NewRuntime.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dir == "" {
		return nil
	}
	dir := r.dir
	if !strings.HasPrefix(filepath.Base(dir), runtimeDirPrefix) || filepath.Clean(filepath.Dir(dir)) != filepath.Clean(os.TempDir()) {
		return fmt.Errorf("refusing to remove unexpected bridge instructions directory %q", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove bridge instructions directory: %w", err)
	}
	r.dir = ""
	r.paths = nil
	r.contents = nil
	return nil
}

func writeVerified(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	written, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if sha256.Sum256(written) != sha256.Sum256(data) {
		return errors.New("materialized content checksum mismatch")
	}
	return nil
}

func unsupportedVersion(version string) error {
	return fmt.Errorf("unsupported bridge instructions version %q", version)
}

func safeVersion(version string) bool {
	if version == "" {
		return false
	}
	for _, r := range version {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
