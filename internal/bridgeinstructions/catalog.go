package bridgeinstructions

import (
	"embed"
	"errors"
	"fmt"
	"sync"
)

const (
	CurrentVersion = "v3"
)

//go:embed bridge-instructions/*.md
var resources embed.FS

var files = map[string]string{
	"v1": "bridge-instructions/feishu-runtime-v1.md",
	"v2": "bridge-instructions/feishu-runtime-v2.md",
	"v3": "bridge-instructions/feishu-runtime-v3.md",
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

// Runtime owns the immutable in-memory instruction catalog used by every
// agent invocation.
type Runtime struct {
	mu       sync.RWMutex
	contents map[string]string
}

// NewRuntime loads every embedded instruction version once.
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

func newRuntime(catalog map[string]string) (*Runtime, error) {
	if len(catalog) == 0 {
		return nil, errors.New("bridge instructions catalog is empty")
	}
	contents := make(map[string]string, len(catalog))
	for version, content := range catalog {
		if !safeVersion(version) {
			return nil, fmt.Errorf("invalid bridge instructions version %q", version)
		}
		contents[version] = content
	}
	return &Runtime{contents: contents}, nil
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

// Close releases the in-memory catalog. It is idempotent.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contents = nil
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
