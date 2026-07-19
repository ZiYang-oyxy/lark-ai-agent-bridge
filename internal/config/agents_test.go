package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentsConfigMissingFileReturnsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	cfg, err := LoadAgentsConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Kind != "claude" {
		t.Fatalf("expected default single claude agent, got %#v", cfg.Agents)
	}
	if got := cfg.HomeLabels("claude"); len(got) != 1 || got[0] != DefaultHomeLabel {
		t.Fatalf("default home labels = %#v", got)
	}
	if got := cfg.BinLabels("claude"); len(got) != 1 || got[0] != DefaultBinLabel {
		t.Fatalf("default bin labels = %#v", got)
	}
}

func TestLoadAgentsConfigEmptyPathReturnsDefault(t *testing.T) {
	cfg, err := LoadAgentsConfig("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Agents) != 1 {
		t.Fatalf("expected default, got %#v", cfg.Agents)
	}
}

func TestLoadAgentsConfigValidDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	doc := `{
      "schema_version": 1,
      "agents": [
        {
          "kind": "claude",
          "label": "Claude Code",
          "homes": [{"label": "隔离", "path": "/tmp/home-a"}],
          "bins": [{"label": "裸 claude", "path": "/usr/local/bin/claude"}]
        }
      ]
    }`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAgentsConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The default home/bin are always prepended.
	homes := cfg.HomeLabels("claude")
	if len(homes) != 2 || homes[0] != DefaultHomeLabel || homes[1] != "隔离" {
		t.Fatalf("home labels = %#v", homes)
	}
	if path, ok := cfg.HomePath("claude", "隔离"); !ok || path != "/tmp/home-a" {
		t.Fatalf("HomePath 隔离 = %q ok=%v", path, ok)
	}
	if path, ok := cfg.BinPath("claude", "裸 claude"); !ok || path != "/usr/local/bin/claude" {
		t.Fatalf("BinPath 裸 claude = %q ok=%v", path, ok)
	}
	// Empty/default labels resolve to "".
	if path, ok := cfg.HomePath("claude", ""); !ok || path != "" {
		t.Fatalf("HomePath empty = %q ok=%v", path, ok)
	}
	if path, ok := cfg.HomePath("claude", DefaultHomeLabel); !ok || path != "" {
		t.Fatalf("HomePath default = %q ok=%v", path, ok)
	}
	// Unknown label is not ok.
	if _, ok := cfg.HomePath("claude", "does-not-exist"); ok {
		t.Fatalf("unknown home label unexpectedly ok")
	}
}

func TestLoadAgentsConfigInvalidFallsBackToDefault(t *testing.T) {
	cases := map[string]string{
		"bad json":       `{`,
		"wrong schema":   `{"schema_version": 2, "agents": [{"kind":"claude"}]}`,
		"no agents":      `{"schema_version": 1, "agents": []}`,
		"unknown kind":   `{"schema_version": 1, "agents": [{"kind":"gemini"}]}`,
		"empty home lbl": `{"schema_version": 1, "agents": [{"kind":"claude","homes":[{"label":"","path":"/x"}]}]}`,
		"dup kind":       `{"schema_version": 1, "agents": [{"kind":"claude"},{"kind":"claude"}]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agents.json")
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadAgentsConfig(path)
			if err == nil {
				t.Fatalf("expected fallback error for %s", name)
			}
			if len(cfg.Agents) != 1 || cfg.Agents[0].Kind != "claude" {
				t.Fatalf("expected default fallback, got %#v", cfg.Agents)
			}
		})
	}
}

func TestLoadAgentsConfigPreservesBinDescAndOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	doc := `{
      "schema_version": 1,
      "agents": [
        {
          "kind": "claude",
          "label": "Claude Code",
          "bins": [
            {"label": "ark4", "path": "/w/bin/ark4", "desc": "豆包 seed-2-1-pro"},
            {"label": "cc4", "path": "/w/bin/cc4", "desc": "claude-opus-4-8"}
          ]
        }
      ]
    }`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAgentsConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := cfg.BinOptions("claude")
	// Default bin is prepended, then ark4, cc4.
	if len(opts) != 3 {
		t.Fatalf("bin options = %#v", opts)
	}
	if opts[0].Value != DefaultBinLabel || opts[0].Label != DefaultBinLabel+" · bridge 默认可执行" {
		t.Fatalf("default bin option = %#v", opts[0])
	}
	if opts[1].Value != "ark4" || opts[1].Label != "ark4 · 豆包 seed-2-1-pro" {
		t.Fatalf("ark4 option = %#v", opts[1])
	}
	if opts[2].Value != "cc4" || opts[2].Label != "cc4 · claude-opus-4-8" {
		t.Fatalf("cc4 option = %#v", opts[2])
	}
	// Agent options carry the display label as description.
	aopts := cfg.AgentOptions()
	if len(aopts) != 1 || aopts[0].Value != "claude" || aopts[0].Label != "claude · Claude Code" {
		t.Fatalf("agent options = %#v", aopts)
	}
}

func TestLoadAgentsConfigResolvesRelativeAndTildePaths(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, ".lark-agent-bridge", "agents.json")
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{
      "schema_version": 1,
      "agents": [{
        "kind": "claude",
        "label": "Claude Code",
        "homes": [
          {"label": "workspace",        "path": "state/claude-home"},
          {"label": "workspace-dot",    "path": "./state/claude-home"},
          {"label": "abs",              "path": "/abs/home"},
          {"label": "home-tilde",       "path": "~/preset/home"}
        ],
        "bins": [
          {"label": "workspace-bin",    "path": "bin/ark4"},
          {"label": "abs-bin",          "path": "/usr/local/bin/claude"}
        ]
      }]
    }`
	if err := os.WriteFile(agentsPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAgentsConfig(agentsPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.WorkDir != dir {
		t.Fatalf("WorkDir = %q, want %q", cfg.WorkDir, dir)
	}
	// relative -> joined with workdir
	if got, ok := cfg.HomePath("claude", "workspace"); !ok || got != filepath.Join(dir, "state/claude-home") {
		t.Fatalf("relative home = %q ok=%v", got, ok)
	}
	if got, ok := cfg.HomePath("claude", "workspace-dot"); !ok || got != filepath.Join(dir, "state/claude-home") {
		t.Fatalf("./relative home = %q ok=%v", got, ok)
	}
	// absolute -> pass-through (cleaned)
	if got, ok := cfg.HomePath("claude", "abs"); !ok || got != "/abs/home" {
		t.Fatalf("abs home = %q ok=%v", got, ok)
	}
	// ~ -> $HOME expansion
	home, _ := os.UserHomeDir()
	if got, ok := cfg.HomePath("claude", "home-tilde"); !ok || got != filepath.Join(home, "preset/home") {
		t.Fatalf("tilde home = %q ok=%v", got, ok)
	}
	// bin relative -> joined
	if got, ok := cfg.BinPath("claude", "workspace-bin"); !ok || got != filepath.Join(dir, "bin/ark4") {
		t.Fatalf("relative bin = %q ok=%v", got, ok)
	}
	if got, ok := cfg.BinPath("claude", "abs-bin"); !ok || got != "/usr/local/bin/claude" {
		t.Fatalf("abs bin = %q ok=%v", got, ok)
	}
	// Empty label still resolves to "" (means "default").
	if got, ok := cfg.HomePath("claude", ""); !ok || got != "" {
		t.Fatalf("empty label = %q ok=%v", got, ok)
	}
}

func TestAgentsConfigCodexReserved(t *testing.T) {
	// Codex is reserved and must be rejected from agents.json for now.
	path := filepath.Join(t.TempDir(), "agents.json")
	doc := `{"schema_version":1,"agents":[{"kind":"codex"}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentsConfig(path); err == nil {
		t.Fatal("expected codex to be rejected")
	}
}
