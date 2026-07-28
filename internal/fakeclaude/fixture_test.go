package fakeclaude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

func TestNewInvocationExtractsLastMarker(t *testing.T) {
	// 两个独立 marker(用空格隔开):取最后一个,与 shell shim `grep -Eo | tail -n 1` 一致。
	argv := []string{"claude", "--model", "claude-3", "--append-system-prompt-file", "/tmp/i", "E2E_RUN_ID_007 prefix E2E_STREAM_005 suffix"}
	inv := NewInvocation(argv)
	if inv.Marker != "E2E_STREAM_005" {
		t.Fatalf("marker: want last E2E_STREAM_005, got %q", inv.Marker)
	}
	if inv.Prompt != argv[len(argv)-1] {
		t.Fatalf("prompt: want last argv, got %q", inv.Prompt)
	}
}

func TestNewInvocationGreedyMarker(t *testing.T) {
	// 与 shell shim `E2E_[A-Za-z0-9_-]+` 贪婪一致:内嵌下划线的组合识别成单个 marker。
	argv := []string{"claude", "E2E_RUN_QUEUE_E2E_STREAM prompt"}
	inv := NewInvocation(argv)
	if inv.Marker != "E2E_RUN_QUEUE_E2E_STREAM" {
		t.Fatalf("marker: want greedy E2E_RUN_QUEUE_E2E_STREAM, got %q", inv.Marker)
	}
}

func TestNewInvocationNoMarker(t *testing.T) {
	inv := NewInvocation([]string{"claude", "hello world"})
	if inv.Marker != "" {
		t.Fatalf("marker should be empty, got %q", inv.Marker)
	}
	if inv.Prompt != "hello world" {
		t.Fatalf("prompt mismatch: %q", inv.Prompt)
	}
}

func TestFixtureMatchesMarker(t *testing.T) {
	f := Fixture{Match: FixtureMatch{MarkerPattern: `E2E_BRIDGE_IMAGE_INTENT.*`}}
	inv := NewInvocation([]string{"claude", "E2E_BRIDGE_IMAGE_INTENT_007 prompt"})
	if !f.Matches(inv) {
		t.Fatalf("expected match for E2E_BRIDGE_IMAGE_INTENT_007")
	}
	inv2 := NewInvocation([]string{"claude", "E2E_STREAM_005 prompt"})
	if f.Matches(inv2) {
		t.Fatalf("expected no match for unrelated marker")
	}
}

func TestFixtureMatchesPromptContains(t *testing.T) {
	f := Fixture{Match: FixtureMatch{PromptContains: "Reply with exactly OK"}}
	inv := NewInvocation([]string{"claude", "Reply with exactly OK. Do not use tools."})
	if !f.Matches(inv) {
		t.Fatalf("expected match for prompt-contains fixture")
	}
}

func TestFixtureMatchesBothConditions(t *testing.T) {
	f := Fixture{Match: FixtureMatch{
		MarkerPattern:  "E2E_CANARY.*",
		PromptContains: "canary check",
	}}
	inv := NewInvocation([]string{"claude", "canary check E2E_CANARY_1"})
	if !f.Matches(inv) {
		t.Fatalf("both conditions should match")
	}
	inv2 := NewInvocation([]string{"claude", "canary check without marker"})
	if f.Matches(inv2) {
		t.Fatalf("marker missing should reject the fixture")
	}
}

func TestLoadDirStableOrder(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "20-second.json", `{
		"name": "second",
		"match": {"marker_pattern": "E2E_SECOND"},
		"emit": [{"line": "{\"type\":\"result\"}"}]
	}`)
	writeFixture(t, dir, "10-first.json", `{
		"name": "first",
		"match": {"marker_pattern": "E2E_FIRST"},
		"emit": [{"line": "{\"type\":\"result\"}"}]
	}`)
	fixtures, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(fixtures) != 2 {
		t.Fatalf("expected 2 fixtures, got %d", len(fixtures))
	}
	if fixtures[0].Name != "first" || fixtures[1].Name != "second" {
		t.Fatalf("expected alphabetical order first,second; got %s,%s", fixtures[0].Name, fixtures[1].Name)
	}
}

func TestLoadDirRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "bad.json", `{"name": "missing-match", "emit": []}`)
	if _, err := LoadDir(dir); err == nil {
		t.Fatalf("expected error for fixture without match")
	}
}

func TestResolveFallsBackToDefault(t *testing.T) {
	inv := NewInvocation([]string{"claude", "unknown E2E_NOTREGISTERED prompt"})
	def := Resolve(nil, inv)
	if def.Name != "default" {
		t.Fatalf("expected default fallback, got %s", def.Name)
	}
	if !strings.Contains(def.Emit[0].Line, "FAKE_E2E_STARTED") {
		t.Fatalf("default emit should carry FAKE_E2E_STARTED marker, got %q", def.Emit[0].Line)
	}
	if !strings.Contains(def.Emit[0].Line, "E2E_NOTREGISTERED") {
		t.Fatalf("default emit should echo the invocation marker, got %q", def.Emit[0].Line)
	}
}

func TestValidateInstructionRequiresMarker(t *testing.T) {
	tmp := t.TempDir()
	inst := filepath.Join(tmp, "i.md")
	if err := os.WriteFile(inst, []byte("Feishu Bridge Runtime Instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	argv := []string{"claude", "--append-system-prompt-file", inst, "normal user prompt"}
	if err := ValidateInstruction(argv); err != nil {
		t.Fatalf("valid case failed: %v", err)
	}
}

func TestValidateInstructionRejectsLeak(t *testing.T) {
	tmp := t.TempDir()
	inst := filepath.Join(tmp, "i.md")
	if err := os.WriteFile(inst, []byte("Feishu Bridge Runtime Instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	argv := []string{"claude", "--append-system-prompt-file", inst, "prompt including Feishu Bridge Runtime Instructions text"}
	if err := ValidateInstruction(argv); err == nil {
		t.Fatalf("leak should be rejected")
	}
}

func TestValidateInstructionRejectsMissingFile(t *testing.T) {
	argv := []string{"claude", "--append-system-prompt-file", "/no/such/file", "user prompt"}
	if err := ValidateInstruction(argv); err == nil {
		t.Fatalf("missing instruction file should error")
	}
}
