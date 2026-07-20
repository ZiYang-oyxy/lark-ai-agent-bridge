package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AgentsSchemaVersion is the on-disk schema version for agents.json.
const AgentsSchemaVersion = 1

// DefaultHomeLabel and DefaultBinLabel are the labels used for the built-in
// "use the host default" home/bin entries. Selecting them means no config-dir
// environment variable is injected and the kind's default executable name is
// used, i.e. the behaviour before this feature existed.
const (
	DefaultHomeLabel = "默认"
	DefaultBinLabel  = "主机 claude"
)

// AgentHome is a named home/config directory preset for an agent. An empty Path
// means "use the agent's default home" (no config-dir env is injected). Desc is
// a short human description shown next to the label in the /config card.
type AgentHome struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Desc  string `json:"desc,omitempty"`
}

// AgentBin is a named executable preset for an agent. An empty Path falls back
// to the kind's default executable name (e.g. "claude"). Desc is a short human
// description (e.g. the underlying model) shown next to the label.
type AgentBin struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Desc  string `json:"desc,omitempty"`
}

// SelectOption is a value/label pair for a card dropdown: Value is the stored
// key (a preset label) and Label is the richer display text (e.g. "ark4 · 豆包").
type SelectOption struct {
	Value string
	Label string
}

// AgentDef describes a selectable agent: its kind plus the home/bin presets a
// user can pick from in the /config card.
type AgentDef struct {
	Kind  string      `json:"kind"`
	Label string      `json:"label"`
	Homes []AgentHome `json:"homes"`
	Bins  []AgentBin  `json:"bins"`
}

// AgentsConfig is the parsed agents.json: the catalogue of selectable agents.
type AgentsConfig struct {
	SchemaVersion int        `json:"schema_version"`
	Agents        []AgentDef `json:"agents"`
	// WorkDir is the base used to resolve relative preset paths at lookup time.
	// It is set by LoadAgentsConfig based on the agents.json location so paths
	// in the file can be workspace-relative (e.g. "bin/ark4") and stay valid
	// when the workspace is moved between machines.
	WorkDir string `json:"-"`
}

var knownAgentKinds = map[string]struct{}{
	"claude": {},
	"codex":  {},
}

// DefaultBinLabelFor returns the implicit host executable label for an agent
// kind. DefaultBinLabel remains the Claude-compatible legacy constant.
func DefaultBinLabelFor(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "codex") {
		return "主机 codex"
	}
	return DefaultBinLabel
}

// DefaultAgentsConfig returns the built-in catalogue used when agents.json is
// absent or invalid: a single claude agent whose only home is the host default
// and whose only bin is the host claude. This preserves the pre-feature
// behaviour (no env injected, default executable name).
func DefaultAgentsConfig() AgentsConfig {
	return AgentsConfig{
		SchemaVersion: AgentsSchemaVersion,
		Agents: []AgentDef{
			{
				Kind:  "claude",
				Label: "Claude Code",
				Homes: []AgentHome{{Label: DefaultHomeLabel, Path: "", Desc: "宿主默认配置目录"}},
				Bins:  []AgentBin{{Label: DefaultBinLabel, Path: "", Desc: "bridge 默认可执行"}},
			},
		},
	}
}

// LoadAgentsConfig reads and validates agents.json at path. A missing file, a
// read error, or an invalid document all fall back to DefaultAgentsConfig; the
// returned error is non-nil only in the fallback-with-reason cases so callers
// can log a warning without aborting startup. The AgentsConfig is always usable.
//
// The returned WorkDir is derived from path (its grandparent directory) and is
// used to resolve relative preset paths at lookup time. Callers may override
// WorkDir afterwards if they resolve it differently.
func LoadAgentsConfig(path string) (AgentsConfig, error) {
	workDir := deriveWorkDir(path)
	fallback := func() AgentsConfig {
		def := DefaultAgentsConfig()
		def.WorkDir = workDir
		return def
	}
	if strings.TrimSpace(path) == "" {
		return fallback(), nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fallback(), nil
	}
	if err != nil {
		return fallback(), fmt.Errorf("read agents config: %w", err)
	}
	var cfg AgentsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fallback(), fmt.Errorf("decode agents config: %w", err)
	}
	normalized, err := normalizeAgentsConfig(cfg)
	if err != nil {
		return fallback(), err
	}
	normalized.WorkDir = workDir
	return normalized, nil
}

// deriveWorkDir returns the workspace directory that owns an agents.json at
// path. agents.json lives at <workdir>/.lark-agent-bridge/agents.json, so the
// workdir is two levels up. Empty path yields "".
func deriveWorkDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Dir(filepath.Dir(path))
}

func normalizeAgentsConfig(cfg AgentsConfig) (AgentsConfig, error) {
	if cfg.SchemaVersion != AgentsSchemaVersion {
		return AgentsConfig{}, fmt.Errorf("unsupported agents schema %d", cfg.SchemaVersion)
	}
	if len(cfg.Agents) == 0 {
		return AgentsConfig{}, errors.New("agents config has no agents")
	}
	out := AgentsConfig{SchemaVersion: AgentsSchemaVersion, Agents: make([]AgentDef, 0, len(cfg.Agents))}
	seenKinds := make(map[string]struct{}, len(cfg.Agents))
	for _, def := range cfg.Agents {
		kind := strings.ToLower(strings.TrimSpace(def.Kind))
		if _, ok := knownAgentKinds[kind]; !ok {
			return AgentsConfig{}, fmt.Errorf("agent kind %q is not supported", def.Kind)
		}
		if _, dup := seenKinds[kind]; dup {
			return AgentsConfig{}, fmt.Errorf("agent kind %q is defined more than once", kind)
		}
		seenKinds[kind] = struct{}{}
		label := strings.TrimSpace(def.Label)
		if label == "" {
			label = kind
		}
		homes, err := normalizeHomes(kind, def.Homes)
		if err != nil {
			return AgentsConfig{}, err
		}
		bins, err := normalizeBins(kind, def.Bins)
		if err != nil {
			return AgentsConfig{}, err
		}
		out.Agents = append(out.Agents, AgentDef{Kind: kind, Label: label, Homes: homes, Bins: bins})
	}
	return out, nil
}

func normalizeHomes(kind string, homes []AgentHome) ([]AgentHome, error) {
	out := make([]AgentHome, 0, len(homes)+1)
	seen := make(map[string]struct{}, len(homes)+1)
	// Always guarantee a "default" home so users can revert to host defaults.
	out = append(out, AgentHome{Label: DefaultHomeLabel, Path: "", Desc: "宿主默认配置目录"})
	seen[DefaultHomeLabel] = struct{}{}
	for _, home := range homes {
		label := strings.TrimSpace(home.Label)
		path := cleanPath(home.Path)
		if label == "" {
			return nil, fmt.Errorf("agent %q has a home with an empty label", kind)
		}
		if label == DefaultHomeLabel {
			// The default entry is implicit; ignore any explicit redefinition.
			continue
		}
		if _, dup := seen[label]; dup {
			return nil, fmt.Errorf("agent %q has duplicate home label %q", kind, label)
		}
		seen[label] = struct{}{}
		out = append(out, AgentHome{Label: label, Path: path, Desc: strings.TrimSpace(home.Desc)})
	}
	return out, nil
}

func normalizeBins(kind string, bins []AgentBin) ([]AgentBin, error) {
	out := make([]AgentBin, 0, len(bins)+1)
	seen := make(map[string]struct{}, len(bins)+1)
	defaultLabel := DefaultBinLabelFor(kind)
	out = append(out, AgentBin{Label: defaultLabel, Path: "", Desc: "bridge 默认可执行"})
	seen[defaultLabel] = struct{}{}
	for _, bin := range bins {
		label := strings.TrimSpace(bin.Label)
		path := cleanPath(bin.Path)
		if label == "" {
			return nil, fmt.Errorf("agent %q has a bin with an empty label", kind)
		}
		if label == defaultLabel {
			continue
		}
		if _, dup := seen[label]; dup {
			return nil, fmt.Errorf("agent %q has duplicate bin label %q", kind, label)
		}
		seen[label] = struct{}{}
		out = append(out, AgentBin{Label: label, Path: path, Desc: strings.TrimSpace(bin.Desc)})
	}
	return out, nil
}

// cleanPath trims whitespace but preserves relative form so relative presets
// stay portable. An empty path is preserved (meaning "default"). Actual
// absolute-path resolution happens at lookup time via resolvePath.
func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// resolvePath turns a stored preset path into an absolute path for use by
// exec. Empty -> empty (means "default"). Absolute paths pass through. A
// leading "~/" or bare "~" expands to $HOME. Otherwise the path is treated as
// relative to workDir; if workDir is empty the value is returned as-is (best
// effort — exec will surface the failure).
func resolvePath(workDir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if workDir == "" {
		return path
	}
	return filepath.Join(workDir, path)
}

// Find returns the AgentDef for kind, or false.
func (c AgentsConfig) Find(kind string) (AgentDef, bool) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	for _, def := range c.Agents {
		if def.Kind == kind {
			return def, true
		}
	}
	return AgentDef{}, false
}

// Kinds returns the agent kinds in catalogue order.
func (c AgentsConfig) Kinds() []string {
	kinds := make([]string, 0, len(c.Agents))
	for _, def := range c.Agents {
		kinds = append(kinds, def.Kind)
	}
	return kinds
}

// HomeLabels returns the home preset labels for kind (empty if unknown).
func (c AgentsConfig) HomeLabels(kind string) []string {
	def, ok := c.Find(kind)
	if !ok {
		return nil
	}
	labels := make([]string, 0, len(def.Homes))
	for _, home := range def.Homes {
		labels = append(labels, home.Label)
	}
	return labels
}

// BinLabels returns the bin preset labels for kind (empty if unknown).
func (c AgentsConfig) BinLabels(kind string) []string {
	def, ok := c.Find(kind)
	if !ok {
		return nil
	}
	labels := make([]string, 0, len(def.Bins))
	for _, bin := range def.Bins {
		labels = append(labels, bin.Label)
	}
	return labels
}

// HomePath resolves a home label to its absolute path for kind. An empty or
// unknown label resolves to "" (the default home, no env injected). Relative
// preset paths in agents.json are resolved against c.WorkDir; "~/..." expands
// to $HOME. ok reports whether the label was a known preset (empty/default is
// always considered known).
func (c AgentsConfig) HomePath(kind, label string) (path string, ok bool) {
	label = strings.TrimSpace(label)
	def, found := c.Find(kind)
	if !found {
		return "", label == "" || label == DefaultHomeLabel
	}
	if label == "" {
		return "", true
	}
	for _, home := range def.Homes {
		if home.Label == label {
			return resolvePath(c.WorkDir, home.Path), true
		}
	}
	return "", false
}

// BinPath resolves a bin label to its path for kind. An empty or unknown label
// resolves to "" (fall back to the default executable name). ok reports whether
// the label was a known preset.
func (c AgentsConfig) BinPath(kind, label string) (path string, ok bool) {
	label = strings.TrimSpace(label)
	def, found := c.Find(kind)
	if !found {
		return "", label == "" || label == DefaultBinLabelFor(kind)
	}
	if label == "" {
		return "", true
	}
	for _, bin := range def.Bins {
		if bin.Label == label {
			return resolvePath(c.WorkDir, bin.Path), true
		}
	}
	return "", false
}

// optionLabel joins a value and its description into display text. An empty
// desc yields just the value.
func optionLabel(value, desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return value
	}
	return value + " · " + desc
}

// AgentOptions returns the selectable agents as value(kind)/label(kind · desc)
// options, where the description is the agent's display Label.
func (c AgentsConfig) AgentOptions() []SelectOption {
	options := make([]SelectOption, 0, len(c.Agents))
	for _, def := range c.Agents {
		options = append(options, SelectOption{Value: def.Kind, Label: optionLabel(def.Kind, def.Label)})
	}
	return options
}

// HomeOptions returns the home presets for kind as value(label)/label(label · desc)
// options (empty if the kind is unknown).
func (c AgentsConfig) HomeOptions(kind string) []SelectOption {
	def, ok := c.Find(kind)
	if !ok {
		return nil
	}
	options := make([]SelectOption, 0, len(def.Homes))
	for _, home := range def.Homes {
		options = append(options, SelectOption{Value: home.Label, Label: optionLabel(home.Label, home.Desc)})
	}
	return options
}

// BinOptions returns the bin presets for kind as value(label)/label(label · desc)
// options (empty if the kind is unknown).
func (c AgentsConfig) BinOptions(kind string) []SelectOption {
	def, ok := c.Find(kind)
	if !ok {
		return nil
	}
	options := make([]SelectOption, 0, len(def.Bins))
	for _, bin := range def.Bins {
		options = append(options, SelectOption{Value: bin.Label, Label: optionLabel(bin.Label, bin.Desc)})
	}
	return options
}
