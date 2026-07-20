package bridge

import (
	"context"
	"path/filepath"
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

func TestConfigSaveChangingAgentClearsForeignBinAndHome(t *testing.T) {
	agents := config.AgentsConfig{
		SchemaVersion: config.AgentsSchemaVersion,
		Agents: []config.AgentDef{
			{Kind: "claude", Homes: []config.AgentHome{{Label: config.DefaultHomeLabel}, {Label: "claude-home", Path: "/h/claude"}}, Bins: []config.AgentBin{{Label: config.DefaultBinLabel}, {Label: "cc4", Path: "/b/cc4"}}},
			{Kind: "codex", Homes: []config.AgentHome{{Label: config.DefaultHomeLabel}, {Label: "codex-home", Path: "/h/codex"}}, Bins: []config.AgentBin{{Label: config.DefaultBinLabelFor("codex")}, {Label: "cx3", Path: "/b/cx3"}}},
		},
	}
	defaults := config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, Agent: "claude", AgentHome: "claude-home", AgentBin: "cc4"}
	store, err := config.OpenPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"), defaults, nil, agents.Agents...)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(config.Config{}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Agents = agents
	svc.Preferences = store
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{
		SessionID: "config-card",
		ActionID:  "config.save",
		Actor:     "user",
		FormValues: map[string]string{
			"agent": "codex", "agent_home": "claude-home", "agent_bin": "cc4",
			"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if result.Event == nil || result.Event.Type != "config_saved" || got.Agent != "codex" || got.AgentHome != "" || got.AgentBin != "" {
		t.Fatalf("result/store = %#v / %#v", result, got)
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

func TestResolveAgentBinHomeDefaultsCodexToHostCodex(t *testing.T) {
	svc := NewService(config.Config{ClaudeBin: "/opt/wrap/cc", DefaultWorkDir: t.TempDir()}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Agents = config.AgentsConfig{
		SchemaVersion: config.AgentsSchemaVersion,
		Agents: []config.AgentDef{{
			Kind:  "codex",
			Label: "Codex CLI",
			Homes: []config.AgentHome{{Label: config.DefaultHomeLabel}},
			Bins:  []config.AgentBin{{Label: "主机 codex"}},
		}},
	}
	bin, home := svc.resolveAgentBinHome(agent.Codex, config.RuntimePreference{Agent: "codex"})
	if bin != "codex" || home != "" {
		t.Fatalf("resolved = %q/%q, want codex/empty", bin, home)
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
