package persist

import "reflect"

// 分层合并：把多层配置来源（configPatch）按优先级叠加，再落到强类型 Config。
//
// 优先级（低 → 高）：Defaults() < base < runtime_override < chat_override < env。
// “未设”用指针 nil 表达，绝不用零值当“未设”——避免 override 里一个 false/0/"" 误覆盖下层有效值。
//
// merge/resolve 用反射按字段名递归，结构约定：
//   - configPatch 与 Config 字段一一对应（同名）。
//   - Config 的子结构（如 AgentConfig）对应 patch 里的 *xxxPatch 指针字段。
//   - Config 的叶子字段（string/int/bool）对应 patch 里的同名指针字段（*string/*int/*bool）。

// mergePatches 把 patches 按给定顺序（低 → 高）叠加，返回新的合并 configPatch。
// 高优先层的非 nil 字段覆盖低优先层；nil 字段不覆盖。输入中的 nil patch 被跳过。
func mergePatches(patches ...*configPatch) *configPatch {
	out := &configPatch{}
	dst := reflect.ValueOf(out).Elem()
	for _, p := range patches {
		if p == nil {
			continue
		}
		mergePatchInto(dst, reflect.ValueOf(p).Elem())
	}
	return out
}

// mergePatchInto 把 src（一层 patch 结构值）叠加进 dst（累积 patch 结构值）。
// 两者同类型。字段要么是 *叶子（指针），要么是 *子patch（指针到 struct）。
func mergePatchInto(dst, src reflect.Value) {
	for i := 0; i < src.NumField(); i++ {
		sf := src.Field(i)
		if sf.Kind() != reflect.Ptr || sf.IsNil() {
			continue // src 未设该字段 → 不覆盖
		}
		df := dst.Field(i)
		// 子 patch（指针到 struct）：递归深度合并。
		if sf.Elem().Kind() == reflect.Struct {
			if df.IsNil() {
				df.Set(reflect.New(df.Type().Elem()))
			}
			mergePatchInto(df.Elem(), sf.Elem())
			continue
		}
		// 叶子指针：整体覆盖（复制指针指向的新值，避免别名共享）。
		nv := reflect.New(sf.Type().Elem())
		nv.Elem().Set(sf.Elem())
		df.Set(nv)
	}
}

// resolve 把内置默认（defaults）与合并后的 patch 落成最终强类型 Config。
// patch 非 nil 的叶子字段覆盖 default，nil 保留 default。
func resolve(defaults Config, merged *configPatch) Config {
	out := defaults
	if merged == nil {
		return out
	}
	applyPatchToStruct(reflect.ValueOf(&out).Elem(), reflect.ValueOf(merged).Elem())
	return out
}

// applyPatchToStruct 把 patch 结构值（全指针镜像）落到 target 强类型结构值。
// target 字段与 patch 字段同名同序：叶子指针 → 赋值；子 patch 指针 → 递归。
func applyPatchToStruct(target, patch reflect.Value) {
	for i := 0; i < patch.NumField(); i++ {
		pf := patch.Field(i)
		if pf.Kind() != reflect.Ptr || pf.IsNil() {
			continue
		}
		tf := target.Field(i)
		if pf.Elem().Kind() == reflect.Struct {
			applyPatchToStruct(tf, pf.Elem())
			continue
		}
		tf.Set(pf.Elem())
	}
}
