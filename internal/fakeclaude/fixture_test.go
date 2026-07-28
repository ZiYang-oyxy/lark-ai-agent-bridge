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

func TestResolvedImageNameDefault(t *testing.T) {
	f := Fixture{WriteImage: true}
	inv := NewInvocation([]string{"claude", "E2E_IMG_001 prompt"})
	got := f.ResolvedImageName(inv)
	want := "e2e-output-E2E_IMG_001.png"
	if got != want {
		t.Fatalf("default image name: want %q, got %q", want, got)
	}
}

func TestResolvedImageNameCustom(t *testing.T) {
	f := Fixture{WriteImage: true, ImageName: "custom-${marker}-page.png"}
	inv := NewInvocation([]string{"claude", "E2E_X prompt"})
	if got := f.ResolvedImageName(inv); got != "custom-E2E_X-page.png" {
		t.Fatalf("custom image name: got %q", got)
	}
}

func TestResolvedImageNameEmptyWhenNoWrite(t *testing.T) {
	f := Fixture{WriteImage: false, ImageName: "shouldnotshow.png"}
	inv := NewInvocation([]string{"claude", "E2E_X"})
	if got := f.ResolvedImageName(inv); got != "" {
		t.Fatalf("expected empty when WriteImage=false, got %q", got)
	}
}

func TestRenderEmitSubstitutesMarkerAndImageName(t *testing.T) {
	f := Fixture{
		WriteImage: true,
		Emit: []FixtureEmit{
			{Line: `{"type":"result","result":"![${marker}](./${image_name})"}`},
		},
	}
	inv := NewInvocation([]string{"claude", "E2E_PIC_9 hint"})
	got := f.RenderEmit(0, inv)
	want := `{"type":"result","result":"![E2E_PIC_9](./e2e-output-E2E_PIC_9.png)"}`
	if got != want {
		t.Fatalf("render: want %q, got %q", want, got)
	}
}

func TestRenderEmitOutOfRange(t *testing.T) {
	f := Fixture{Emit: []FixtureEmit{{Line: "x"}}}
	inv := NewInvocation([]string{"claude", "prompt"})
	if got := f.RenderEmit(5, inv); got != "" {
		t.Fatalf("out-of-range should return empty, got %q", got)
	}
}

func TestTestImagePNGDecodesToPNGHeader(t *testing.T) {
	data, err := TestImagePNG()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(data) < 8 {
		t.Fatalf("too short: %d", len(data))
	}
	pngSig := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	for i, b := range pngSig {
		if data[i] != b {
			t.Fatalf("byte %d: want %#x got %#x", i, b, data[i])
		}
	}
}

func TestLoadDirAcceptsExtensionFields(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "50-image.json", `{
		"name": "img_intent",
		"match": {"marker_pattern": "E2E_IMG"},
		"write_image": true,
		"image_name": "custom-${marker}.png",
		"hang": false,
		"post_delay_sec": 0.5,
		"emit": [
			{"line": "{\"type\":\"result\",\"result\":\"![${marker}](./${image_name})\"}"}
		]
	}`)
	fixtures, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("expected 1 fixture")
	}
	f := fixtures[0]
	if !f.WriteImage {
		t.Fatalf("WriteImage should be true")
	}
	if f.ImageName != "custom-${marker}.png" {
		t.Fatalf("ImageName: %q", f.ImageName)
	}
	if f.Hang {
		t.Fatalf("Hang should be false")
	}
	if f.PostDelay != 0.5 {
		t.Fatalf("PostDelay: %v", f.PostDelay)
	}
}
