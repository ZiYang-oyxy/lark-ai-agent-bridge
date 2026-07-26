package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptrStr(s string) *string { return &s }
func ptrInt(i int) *int       { return &i }
func ptrBool(b bool) *bool    { return &b }

// TestMergePatchesOrder 高优先层的非 nil 字段覆盖低优先层。
func TestMergePatchesOrder(t *testing.T) {
	base := &configPatch{Agent: &agentPatch{Model: ptrStr("base-model"), Effort: ptrStr("low")}}
	runtime := &configPatch{Agent: &agentPatch{Model: ptrStr("runtime-model")}}
	env := &configPatch{Agent: &agentPatch{Effort: ptrStr("high")}}

	merged := mergePatches(base, runtime, env) // 低 → 高
	if merged.Agent == nil {
		t.Fatalf("merged.Agent nil")
	}
	if got := deref(merged.Agent.Model); got != "runtime-model" {
		t.Fatalf("Model = %q, want runtime-model (runtime overrides base)", got)
	}
	if got := deref(merged.Agent.Effort); got != "high" {
		t.Fatalf("Effort = %q, want high (env overrides base)", got)
	}
}

// TestMergePatchesZeroOverridesNil override 显式设零值能覆盖下层真值；nil 不覆盖。
func TestMergePatchesZeroOverridesNil(t *testing.T) {
	base := &configPatch{
		Behavior: &behaviorPatch{ReplyEnabled: ptrBool(true), MaxTurns: ptrInt(10)},
	}
	// override 显式把 ReplyEnabled 设 false（零值），MaxTurns 不动（nil）。
	override := &configPatch{
		Behavior: &behaviorPatch{ReplyEnabled: ptrBool(false)},
	}
	merged := mergePatches(base, override)
	if merged.Behavior == nil {
		t.Fatalf("merged.Behavior nil")
	}
	if got := merged.Behavior.ReplyEnabled; got == nil || *got != false {
		t.Fatalf("ReplyEnabled = %v, want explicit false (zero overrides)", got)
	}
	if got := merged.Behavior.MaxTurns; got == nil || *got != 10 {
		t.Fatalf("MaxTurns = %v, want 10 (nil does not override)", got)
	}
}

// TestMergePatchesNilInputs 跳过 nil patch 输入，不 panic。
func TestMergePatchesNilInputs(t *testing.T) {
	only := &configPatch{Agent: &agentPatch{Model: ptrStr("x")}}
	merged := mergePatches(nil, only, nil)
	if merged.Agent == nil || deref(merged.Agent.Model) != "x" {
		t.Fatalf("nil inputs mishandled: %+v", merged)
	}
}

// TestResolveNoOverrideEqualsDefaults 无任何 override 时最终值 == Defaults()。
func TestResolveNoOverrideEqualsDefaults(t *testing.T) {
	def := Defaults()
	got := resolve(def, &configPatch{})
	if got.Agent.Model != def.Agent.Model || got.Agent.Effort != def.Agent.Effort {
		t.Fatalf("resolve with empty patch = %+v, want defaults %+v", got.Agent, def.Agent)
	}
	if got.Behavior.ReplyEnabled != def.Behavior.ReplyEnabled {
		t.Fatalf("resolve behavior = %+v, want defaults %+v", got.Behavior, def.Behavior)
	}
}

// TestResolveAppliesPatch patch 的非 nil 字段落到最终 Config；nil 字段保留 default。
func TestResolveAppliesPatch(t *testing.T) {
	def := Defaults()
	patch := &configPatch{Agent: &agentPatch{Model: ptrStr("custom")}}
	got := resolve(def, patch)
	if got.Agent.Model != "custom" {
		t.Fatalf("Model = %q, want custom", got.Agent.Model)
	}
	if got.Agent.Effort != def.Agent.Effort {
		t.Fatalf("Effort = %q, want default %q (patch nil)", got.Agent.Effort, def.Agent.Effort)
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestConfigSecretNotSerialized 设了 secret 值后，序列化 configDoc 的 JSON 里搜不到该值。
func TestConfigSecretNotSerialized(t *testing.T) {
	const secretVal = "cli_super_secret_value_12345"
	doc := configDoc{
		SchemaVersion: ConfigSchemaVersion,
		Base: &configPatch{
			Lark: &larkPatch{
				AppID:     ptrStr(secretVal),
				AppSecret: ptrStr(secretVal),
				BotOpenID: ptrStr("ou_bot_open"),
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), secretVal) {
		t.Fatalf("serialized doc leaked secret: %s", raw)
	}
	// 非 secret 字段仍应出现。
	if !strings.Contains(string(raw), "ou_bot_open") {
		t.Fatalf("non-secret bot_open_id missing from doc: %s", raw)
	}
}

// TestConfigSecretNotLoadedFromFile 从含 secret 字段名的 JSON 加载时，secret 字段保持为空。
func TestConfigSecretNotLoadedFromFile(t *testing.T) {
	// 人为构造一份带 secret 名的 JSON（如手工篡改或旧格式），secret 不该被吸入。
	raw := []byte(`{
		"schema_version": 1,
		"base": { "lark": { "app_id": "LEAKED", "app_secret": "LEAKED", "bot_open_id": "ou_x" } }
	}`)
	var doc configDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Base == nil || doc.Base.Lark == nil {
		t.Fatalf("expected base.lark present")
	}
	if doc.Base.Lark.AppID != nil {
		t.Fatalf("AppID loaded from file = %q, want nil (secret must not load)", *doc.Base.Lark.AppID)
	}
	if doc.Base.Lark.AppSecret != nil {
		t.Fatalf("AppSecret loaded from file = %q, want nil", *doc.Base.Lark.AppSecret)
	}
	if doc.Base.Lark.BotOpenID == nil || *doc.Base.Lark.BotOpenID != "ou_x" {
		t.Fatalf("BotOpenID (non-secret) should load, got %v", doc.Base.Lark.BotOpenID)
	}
}

// TestConfigValidateRejectsInvalid Validate 拒绝非法枚举/负数，返回字段级错误。
func TestConfigValidateRejectsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"negative max turns", func(c *Config) { c.Behavior.MaxTurns = -1 }},
		{"zero max turns", func(c *Config) { c.Behavior.MaxTurns = 0 }},
		{"bad effort enum", func(c *Config) { c.Agent.Effort = "turbo" }},
		{"empty model", func(c *Config) { c.Agent.Model = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Defaults()
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("Validate accepted invalid config (%s)", tc.name)
			}
		})
	}
	// Defaults 本身必须合法。
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("Defaults().Validate() = %v, want nil", err)
	}
}

// TestConfigWriteDocPerm 写出的 config.json 权限为 0600。
func TestConfigWriteDocPerm(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	doc := configDoc{SchemaVersion: ConfigSchemaVersion, Base: &configPatch{Agent: &agentPatch{Model: ptrStr("m")}}}
	if err := writeConfigDoc(e, doc); err != nil {
		t.Fatalf("writeConfigDoc: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config.json perm = %o, want 600", perm)
	}
}

// TestConfigDocRoundTrip 分区段文档往返：写盘再读回，各层 patch 结构保持。
func TestConfigDocRoundTrip(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	doc := configDoc{
		SchemaVersion:   ConfigSchemaVersion,
		Base:            &configPatch{Agent: &agentPatch{Model: ptrStr("base-m")}},
		RuntimeOverride: &configPatch{Behavior: &behaviorPatch{MaxTurns: ptrInt(5)}},
		ChatOverrides:   map[string]*configPatch{"oc_1": {Agent: &agentPatch{Effort: ptrStr("high")}}},
	}
	if err := writeConfigDoc(e, doc); err != nil {
		t.Fatalf("writeConfigDoc: %v", err)
	}
	got, err := readConfigDoc(e)
	if err != nil {
		t.Fatalf("readConfigDoc: %v", err)
	}
	if got.Base == nil || deref(got.Base.Agent.Model) != "base-m" {
		t.Fatalf("base lost: %+v", got.Base)
	}
	if got.RuntimeOverride == nil || got.RuntimeOverride.Behavior == nil || *got.RuntimeOverride.Behavior.MaxTurns != 5 {
		t.Fatalf("runtime_override lost: %+v", got.RuntimeOverride)
	}
	if got.ChatOverrides["oc_1"] == nil || deref(got.ChatOverrides["oc_1"].Agent.Effort) != "high" {
		t.Fatalf("chat_override lost: %+v", got.ChatOverrides)
	}
}
