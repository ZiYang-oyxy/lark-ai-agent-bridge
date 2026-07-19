package bridge

import (
	"strings"
	"testing"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

func TestResolveAgentBinHomeDefaultsToConfigBin(t *testing.T) {
	svc := NewService(config.Config{DefaultAgent: "claude", ClaudeBin: "/opt/wrap/cc", DefaultWorkDir: t.TempDir()}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	// Empty preference (no agent/home/<USER>) resolves to the configured bin and
	// the default (empty) home — the pre-feature behaviour.
	bin, home := svc.resolveAgentBinHome(agent.Claude, config.RuntimePreference{Agent: "claude"})
	if bin != "/opt/wrap/cc" {
		t.Fatalf("bin = %q, want configured claude bin", bin)
	}
	if home != "" {
		t.Fatalf("home = %q, want empty (default)", home)
	}
}

func TestResolveAgentBinHomeUsesCataloguePresets(t *testing.T) {
	svc := NewService(config.Config{DefaultAgent: "claude", ClaudeBin: "cc", DefaultWorkDir: t.TempDir()}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Agents = config.AgentsConfig{
		SchemaVersion: config.AgentsSchemaVersion,
		Agents: []config.AgentDef{{
			Kind:  "claude",
			Label: "Claude Code",
			Homes: []config.AgentHome{{Label: config.DefaultHomeLabel}, {Label: "隔离", Path: "/data/home-a"}},
			Bins:  []config.AgentBin{{Label: config.DefaultBinLabel}, {Label: "裸 claude", Path: "/usr/local/bin/claude"}},
		}},
	}
	bin, home := svc.resolveAgentBinHome(agent.Claude, config.RuntimePreference{Agent: "claude", AgentBin: "裸 claude", AgentHome: "隔离"})
	if bin != "/usr/local/bin/claude" {
		t.Fatalf("bin = %q, want preset path", bin)
	}
	if home != "/data/home-a" {
		t.Fatalf("home = %q, want preset path", home)
	}
	// The default bin preset (empty path) still falls back to Config.ClaudeBin.
	bin, home = svc.resolveAgentBinHome(agent.Claude, config.RuntimePreference{Agent: "claude", AgentBin: config.DefaultBinLabel, AgentHome: config.DefaultHomeLabel})
	if bin != "cc" || home != "" {
		t.Fatalf("default presets = %q/%q, want cc/empty", bin, home)
	}
}

func TestChildEnvOverridesAndPWD(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/old")
	t.Setenv("PWD", "/somewhere")
	env := childEnv("/work/dir", []string{"CLAUDE_CONFIG_DIR=/new"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CLAUDE_CONFIG_DIR=/new") {
		t.Fatalf("override missing: %q", joined)
	}
	if strings.Contains(joined, "CLAUDE_CONFIG_DIR=/old") {
		t.Fatalf("stale value not replaced: %q", joined)
	}
	if strings.Contains(joined, "PWD=/somewhere") {
		t.Fatalf("inherited PWD not stripped: %q", joined)
	}
	if !strings.Contains(joined, "PWD=/work/dir") {
		t.Fatalf("workdir PWD missing: %q", joined)
	}
}

func TestChildEnvNoWorkDirKeepsInheritedPWDButAppliesExtra(t *testing.T) {
	t.Setenv("PWD", "/keep")
	env := childEnv("", []string{"CODEX_HOME=/data/codex"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CODEX_HOME=/data/codex") {
		t.Fatalf("extra env missing: %q", joined)
	}
	if !strings.Contains(joined, "PWD=/keep") {
		t.Fatalf("with no workdir, inherited PWD should be kept: %q", joined)
	}
}
