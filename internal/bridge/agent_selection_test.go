package bridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/session"
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

func TestResolveAgentBinHomeDoesNotReuseForeignAgentPreset(t *testing.T) {
	agents := config.AgentsConfig{
		SchemaVersion: config.AgentsSchemaVersion,
		Agents: []config.AgentDef{
			{Kind: "claude", Homes: []config.AgentHome{{Label: config.DefaultHomeLabel}}, Bins: []config.AgentBin{{Label: config.DefaultBinLabel}}},
			{Kind: "codex", Homes: []config.AgentHome{{Label: "codex-home", Path: "/h/codex"}}, Bins: []config.AgentBin{{Label: "cx4", Path: "/b/cx4"}}},
		},
	}
	svc := NewService(config.Config{ClaudeBin: "/opt/wrap/claude", DefaultWorkDir: t.TempDir()}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Agents = agents

	bin, home := svc.resolveAgentBinHome(agent.Claude, config.RuntimePreference{Agent: "codex", AgentHome: "codex-home", AgentBin: "cx4"})

	if bin != "/opt/wrap/claude" || home != "" {
		t.Fatalf("resolved foreign preset as bin=%q home=%q, want Claude defaults", bin, home)
	}
}

func TestServiceFreezesResolvedAgentBinAndHomeAtEnqueue(t *testing.T) {
	agents := config.AgentsConfig{
		SchemaVersion: config.AgentsSchemaVersion,
		Agents: []config.AgentDef{{
			Kind:  "claude",
			Homes: []config.AgentHome{{Label: config.DefaultHomeLabel}, {Label: "home-a", Path: "/h/a"}, {Label: "home-b", Path: "/h/b"}},
			Bins:  []config.AgentBin{{Label: config.DefaultBinLabel}, {Label: "cc4", Path: "/b/cc4"}, {Label: "cc5", Path: "/b/cc5"}},
		}},
	}
	defaults := config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, Agent: "claude", AgentHome: "home-a", AgentBin: "cc4"}
	store, err := config.OpenPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"), defaults, nil, agents.Agents...)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(config.Config{DefaultAgent: "claude", DefaultWorkDir: t.TempDir()}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Agents = agents
	svc.Preferences = store
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "first", ChatID: "chat", Sender: "u", Text: "one", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, Agent: "claude", AgentHome: "home-b", AgentBin: "cc5"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "second", ChatID: "chat", Sender: "u", Text: "two", Time: now.Add(time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	got, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"})
	if !ok || len(got.Queue) != 2 {
		t.Fatalf("session = %#v", got)
	}
	if got.Queue[0].AgentBin != "/b/cc4" || got.Queue[0].AgentHome != "/h/a" || got.Queue[1].AgentBin != "/b/cc5" || got.Queue[1].AgentHome != "/h/b" {
		t.Fatalf("frozen presets = %#v", got.Queue)
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
	if result.Event.ConfigForm != nil {
		t.Fatalf("successful config save must close the form, got %#v", result.Event.ConfigForm)
	}
}

func TestAgentModeSaveSwitchesAgentAndClearsPresets(t *testing.T) {
	agents := config.AgentsConfig{
		SchemaVersion: config.AgentsSchemaVersion,
		Agents: []config.AgentDef{
			{Kind: "claude", Homes: []config.AgentHome{{Label: "claude-home", Path: "/h/claude"}}, Bins: []config.AgentBin{{Label: config.DefaultBinLabel}, {Label: "cc4", Path: "/b/cc4"}}},
			{Kind: "codex", Bins: []config.AgentBin{{Label: config.DefaultBinLabelFor("codex")}, {Label: "cx3", Path: "/b/cx3"}}},
		},
	}
	defaults := config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, Agent: "claude", AgentHome: "claude-home", AgentBin: "cc4"}
	store, err := config.OpenPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"), defaults, nil, agents.Agents...)
	if err != nil {
		t.Fatal(err)
	}
	renderer := card.NewFakeRenderer()
	svc := NewService(config.Config{}, renderer, newFakeRunner(), audit.NewRecorder())
	svc.Agents = agents
	svc.Preferences = store
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "agent-mode-card", ActionID: "agent_mode.save", Actor: "user", FormValues: map[string]string{"agent": "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.Agent != "codex" || got.AgentHome != "" || got.AgentBin != "" {
		t.Fatalf("preference = %#v", got)
	}
	if result.Event == nil || result.Event.AgentModeForm == nil || result.Event.AgentModeForm.Agent != "codex" {
		t.Fatalf("agent mode result = %#v", result.Event)
	}
}

func TestConfigSaveWithoutAgentKeepsCurrentAgentMode(t *testing.T) {
	agents := config.AgentsConfig{SchemaVersion: config.AgentsSchemaVersion, Agents: []config.AgentDef{
		{Kind: "codex", Bins: []config.AgentBin{{Label: config.DefaultBinLabelFor("codex")}, {Label: "cx3", Path: "/b/cx3"}}},
	}}
	defaults := config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, Agent: "codex", AgentBin: "cx3"}
	store, err := config.OpenPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"), defaults, nil, agents.Agents...)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(config.Config{}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Agents = agents
	svc.Preferences = store
	// Model is not read from the form (it stays at the default); effort is
	// read from the form, so the submitted "high" replaces the current "low".
	_, err = svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "config-card", ActionID: "config.save", Actor: "user", FormValues: map[string]string{
		"model": "default", "effort": "high", "reply_mode": "append", "conversation_mode": "chat", "agent_bin": "cx3",
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.Agent != "codex" || got.AgentBin != "cx3" || got.Effort != "high" {
		t.Fatalf("preference = %#v", got)
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

// 零值 Config(CodexBin 未设)时,codex 未选定 bin 退回裸名 "codex",不劣于原行为。
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

// 与 claude 对称:配置了 Config.CodexBin(经 LAB_CODEX_BIN 注入绝对路径)时,
// codex 未选定 bin 兜底到该配置值,而非裸名 —— 修复 Test/服务进程 PATH 不含
// workspace bin 时 codex exec "codex" not found in $PATH 的失败。
func TestResolveAgentBinHomeDefaultsCodexToConfigBin(t *testing.T) {
	svc := NewService(config.Config{ClaudeBin: "/opt/wrap/cc", CodexBin: "/opt/wrap/cx", DefaultWorkDir: t.TempDir()}, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Agents = config.AgentsConfig{
		SchemaVersion: config.AgentsSchemaVersion,
		Agents: []config.AgentDef{{
			Kind:  "codex",
			Label: "Codex CLI",
			Homes: []config.AgentHome{{Label: config.DefaultHomeLabel}},
			Bins:  []config.AgentBin{{Label: config.DefaultBinLabel}},
		}},
	}
	bin, home := svc.resolveAgentBinHome(agent.Codex, config.RuntimePreference{Agent: "codex", AgentBin: config.DefaultBinLabel})
	if bin != "/opt/wrap/cx" || home != "" {
		t.Fatalf("resolved = %q/%q, want /opt/wrap/cx/empty", bin, home)
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
	t.Setenv("LAB_SCHEDULE_CLI", "/stale/bridge")
	env := childEnv("", []string{"CODEX_HOME=/data/codex"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CODEX_HOME=/data/codex") {
		t.Fatalf("extra env missing: %q", joined)
	}
	if !strings.Contains(joined, "PWD=/keep") {
		t.Fatalf("with no workdir, inherited PWD should be kept: %q", joined)
	}
	if strings.Contains(joined, "LAB_SCHEDULE_CLI=") {
		t.Fatal("inherited schedule CLI leaked")
	}
}
