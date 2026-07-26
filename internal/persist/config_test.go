package persist

import (
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
