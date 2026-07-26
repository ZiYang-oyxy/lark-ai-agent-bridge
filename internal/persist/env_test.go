package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mapLookup 提供表驱动的 env 查询。
func mapLookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// TestEnvPatchWhitelist envPatch 只填“有 env tag”的字段；secret 字段也走这里。
func TestEnvPatchWhitelist(t *testing.T) {
	p := envPatch(mapLookup(map[string]string{
		"LAB_LARK_APP_ID":     "cli_env_id",
		"LAB_LARK_APP_SECRET": "env_secret",
		"LAB_AGENT_MODEL":     "env-model",
		"LAB_UNKNOWN":         "should-be-ignored",
	}))
	if p.Lark == nil || p.Lark.AppID == nil || *p.Lark.AppID != "cli_env_id" {
		t.Fatalf("Lark.AppID = %v, want cli_env_id", p.Lark)
	}
	if p.Lark.AppSecret == nil || *p.Lark.AppSecret != "env_secret" {
		t.Fatalf("Lark.AppSecret = %v", p.Lark.AppSecret)
	}
	if p.Agent == nil || deref(p.Agent.Model) != "env-model" {
		t.Fatalf("Agent.Model = %v", p.Agent)
	}
	if p.Agent.Effort != nil {
		t.Fatalf("Agent.Effort should be nil (no env set), got %v", p.Agent.Effort)
	}
	if p.Behavior != nil {
		t.Fatalf("Behavior should be nil (no env set), got %+v", p.Behavior)
	}
}

// TestEnvPatchTypedFields env 值按叶子类型解析（bool/int）。
func TestEnvPatchTypedFields(t *testing.T) {
	p := envPatch(mapLookup(map[string]string{
		"LAB_BEHAVIOR_REPLY_ENABLED": "false",
		"LAB_BEHAVIOR_MAX_TURNS":     "42",
	}))
	if p.Behavior == nil {
		t.Fatalf("Behavior nil")
	}
	if p.Behavior.ReplyEnabled == nil || *p.Behavior.ReplyEnabled != false {
		t.Fatalf("ReplyEnabled = %v, want false", p.Behavior.ReplyEnabled)
	}
	if p.Behavior.MaxTurns == nil || *p.Behavior.MaxTurns != 42 {
		t.Fatalf("MaxTurns = %v, want 42", p.Behavior.MaxTurns)
	}
}

// TestEnvNotWrittenToDisk 设 env → Current() 生效但 config.json 里找不到该值。
func TestEnvNotWrittenToDisk(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	// 预先写一份 base（合法）配置。
	base := configDoc{
		SchemaVersion: ConfigSchemaVersion,
		Base:          &configPatch{Agent: &agentPatch{Model: ptrStr("base-m"), Effort: ptrStr("medium")}},
	}
	if err := writeConfigDoc(e, base); err != nil {
		t.Fatalf("writeConfigDoc: %v", err)
	}

	const secretVal = "cli_secret_XYZ"
	m, err := loadConfigWith(e, mapLookup(map[string]string{
		"LAB_LARK_APP_ID":     secretVal,
		"LAB_LARK_APP_SECRET": secretVal,
	}))
	if err != nil {
		t.Fatalf("loadConfigWith: %v", err)
	}
	cur := m.Current()
	if cur.Lark.AppID != secretVal || cur.Lark.AppSecret != secretVal {
		t.Fatalf("env not injected into Current(): %+v", cur.Lark)
	}
	// config.json 内容里不能出现 secret 值。
	raw, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if strings.Contains(string(raw), secretVal) {
		t.Fatalf("secret leaked to config.json: %s", raw)
	}
}

// TestReloadRejectsInvalidKeepsOld 热重载给非法值 → 返回 error 且 Current() 保留旧值。
func TestReloadRejectsInvalidKeepsOld(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	m, err := loadConfigWith(e, mapLookup(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	oldModel := m.Current().Agent.Model

	// 非法：MaxTurns = 0（Validate 拒绝）。
	err = m.SetRuntimeOverride(&configPatch{Behavior: &behaviorPatch{MaxTurns: ptrInt(0)}})
	if err == nil {
		t.Fatalf("SetRuntimeOverride accepted invalid MaxTurns=0")
	}
	if m.Current().Agent.Model != oldModel {
		t.Fatalf("invalid reload mutated Current(): %+v", m.Current())
	}
	// 磁盘也不应写入非法值。
	doc, err := readConfigDoc(e)
	if err != nil {
		t.Fatalf("readConfigDoc: %v", err)
	}
	if doc.RuntimeOverride != nil && doc.RuntimeOverride.Behavior != nil && doc.RuntimeOverride.Behavior.MaxTurns != nil {
		t.Fatalf("invalid runtime_override leaked to disk: %+v", doc.RuntimeOverride)
	}
}

// TestReloadOnlyTouchesOverrideSection 热重载合法值 → base 区段不变，只写 runtime_override。
func TestReloadOnlyTouchesOverrideSection(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	// 预先写入 base。
	initial := configDoc{
		SchemaVersion: ConfigSchemaVersion,
		Base:          &configPatch{Agent: &agentPatch{Model: ptrStr("base-m"), Effort: ptrStr("low")}},
	}
	if err := writeConfigDoc(e, initial); err != nil {
		t.Fatalf("writeConfigDoc: %v", err)
	}
	m, err := loadConfigWith(e, mapLookup(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := m.SetRuntimeOverride(&configPatch{Agent: &agentPatch{Effort: ptrStr("high")}}); err != nil {
		t.Fatalf("SetRuntimeOverride: %v", err)
	}
	if got := m.Current().Agent.Effort; got != "high" {
		t.Fatalf("Current().Effort = %q, want high", got)
	}
	doc, err := readConfigDoc(e)
	if err != nil {
		t.Fatalf("readConfigDoc: %v", err)
	}
	if doc.Base == nil || deref(doc.Base.Agent.Model) != "base-m" || deref(doc.Base.Agent.Effort) != "low" {
		t.Fatalf("base was mutated: %+v", doc.Base)
	}
	if doc.RuntimeOverride == nil || doc.RuntimeOverride.Agent == nil || deref(doc.RuntimeOverride.Agent.Effort) != "high" {
		t.Fatalf("runtime_override not written: %+v", doc.RuntimeOverride)
	}
}

// TestEnvIgnoredForUntaggedField 无 env tag 的字段设 LAB_XXX 无效。
func TestEnvIgnoredForUntaggedField(t *testing.T) {
	// BotOpenID 没有 env tag，即便设 LAB_LARK_BOT_OPEN_ID 也不生效。
	p := envPatch(mapLookup(map[string]string{
		"LAB_LARK_BOT_OPEN_ID": "should-not-load",
	}))
	if p.Lark != nil && p.Lark.BotOpenID != nil {
		t.Fatalf("untagged field loaded from env: %v", p.Lark.BotOpenID)
	}
}

// TestForChatAppliesChatOverride ForChat 叠加 chat_override 后的生效值。
func TestForChatAppliesChatOverride(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := writeConfigDoc(e, configDoc{
		SchemaVersion: ConfigSchemaVersion,
		ChatOverrides: map[string]*configPatch{
			"oc_1": {Agent: &agentPatch{Model: ptrStr("chat-model")}},
		},
	}); err != nil {
		t.Fatalf("writeConfigDoc: %v", err)
	}
	m, err := loadConfigWith(e, mapLookup(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := m.Current().Agent.Model; got != Defaults().Agent.Model {
		t.Fatalf("global Current().Model = %q, want default", got)
	}
	if got := m.ForChat("oc_1").Agent.Model; got != "chat-model" {
		t.Fatalf("ForChat(oc_1).Model = %q, want chat-model", got)
	}
	if got := m.ForChat("oc_other").Agent.Model; got != Defaults().Agent.Model {
		t.Fatalf("ForChat(oc_other).Model = %q, want default", got)
	}
}

// TestManagerCurrentRaceFree 并发读 Current() 与 SetRuntimeOverride 之下 -race 无数据竞争。
func TestManagerCurrentRaceFree(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	m, err := loadConfigWith(e, mapLookup(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	// 一堆读者。
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.Current().Agent.Model
					_ = m.ForChat("oc").Agent.Model
				}
			}
		}()
	}
	// 一堆写者。
	for i := 0; i < 20; i++ {
		if err := m.SetRuntimeOverride(&configPatch{Behavior: &behaviorPatch{MaxTurns: ptrInt(i + 1)}}); err != nil {
			t.Fatalf("SetRuntimeOverride: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

// 附：确保 secret 值真的从未落到 disk（json 结构层面的补丁）。
func TestConfigDocMarshalSecretNever(t *testing.T) {
	p := &configPatch{Lark: &larkPatch{AppID: ptrStr("cli_leak"), BotOpenID: ptrStr("ou_x")}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "cli_leak") || strings.Contains(string(raw), "app_id") {
		t.Fatalf("patch leaked secret or its key: %s", raw)
	}
	if !strings.Contains(string(raw), "ou_x") {
		t.Fatalf("non-secret dropped: %s", raw)
	}
}
