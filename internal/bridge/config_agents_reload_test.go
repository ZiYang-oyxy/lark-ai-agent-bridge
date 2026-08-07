package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
)

// A running serve loads agents.json once at startup into svc.Agents. Editing
// the file after that (e.g. changing a bin's desc) must still show up on the
// next /config render — configFormAtRevision re-reads agents.json every call
// and falls back to svc.Agents only when the read fails.
func TestConfigFormRereadsAgentsJSONOnEachRender(t *testing.T) {
	svc, _ := localConfigService(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	writeAgents := func(desc string) {
		body := `{
"schema_version": 1,
"agents": [
  {
    "kind": "` + string(agent.Claude) + `",
    "label": "Claude Code",
    "homes": [],
    "bins": [{"label": "cc5", "path": "bin/cc5", "desc": "` + desc + `"}]
  }
]
}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeAgents("OLD-DESC")
	loaded, err := config.LoadAgentsConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc.Agents = loaded
	svc.Config.AgentsConfigPath = path

	form := svc.configForm(config.RuntimePreference{Agent: string(agent.Claude)})
	got := findBinLabel(form.AgentBins, "cc5")
	if !strings.Contains(got, "OLD-DESC") {
		t.Fatalf("first render cc5 label = %q, want it to contain OLD-DESC", got)
	}

	writeAgents("NEW-DESC")

	form2 := svc.configForm(config.RuntimePreference{Agent: string(agent.Claude)})
	got2 := findBinLabel(form2.AgentBins, "cc5")
	if !strings.Contains(got2, "NEW-DESC") {
		t.Fatalf("second render cc5 label = %q, want it to contain NEW-DESC (fresh reload)", got2)
	}
	if strings.Contains(got2, "OLD-DESC") {
		t.Fatalf("second render still shows OLD-DESC: %q", got2)
	}
}

// If agents.json becomes unreadable/invalid mid-run, the picker must fall back
// to the startup snapshot on svc.Agents rather than showing an empty list.
func TestConfigFormFallsBackToSnapshotOnReloadError(t *testing.T) {
	svc, _ := localConfigService(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	valid := `{
"schema_version": 1,
"agents": [{"kind": "` + string(agent.Claude) + `", "label": "Claude Code", "homes": [], "bins": [
  {"label": "cc5", "path": "bin/cc5", "desc": "SNAPSHOT-DESC"}
]}]
}`
	if err := os.WriteFile(path, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadAgentsConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc.Agents = loaded
	svc.Config.AgentsConfigPath = path

	if err := os.WriteFile(path, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	form := svc.configForm(config.RuntimePreference{Agent: string(agent.Claude)})
	got := findBinLabel(form.AgentBins, "cc5")
	if !strings.Contains(got, "SNAPSHOT-DESC") {
		t.Fatalf("fallback cc5 label = %q, want it to contain SNAPSHOT-DESC from svc.Agents snapshot", got)
	}
}

// If agents.json is deleted at runtime, /config must not silently swap in the
// built-in default catalogue — it should keep showing the startup snapshot
// until agents.json comes back.
func TestConfigFormFallsBackToSnapshotWhenAgentsJSONMissing(t *testing.T) {
	svc, _ := localConfigService(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	valid := `{
"schema_version": 1,
"agents": [{"kind": "` + string(agent.Claude) + `", "label": "Claude Code", "homes": [], "bins": [
  {"label": "cc5", "path": "bin/cc5", "desc": "SNAPSHOT-DESC"}
]}]
}`
	if err := os.WriteFile(path, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadAgentsConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc.Agents = loaded
	svc.Config.AgentsConfigPath = path

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	form := svc.configForm(config.RuntimePreference{Agent: string(agent.Claude)})
	got := findBinLabel(form.AgentBins, "cc5")
	if !strings.Contains(got, "SNAPSHOT-DESC") {
		t.Fatalf("missing-file fallback cc5 label = %q, want it to contain SNAPSHOT-DESC from svc.Agents (not built-in default)", got)
	}
}

func findBinLabel(options []card.SelectOption, value string) string {
	for _, opt := range options {
		if opt.Value == value {
			return opt.Label
		}
	}
	return ""
}
