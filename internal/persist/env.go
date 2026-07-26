package persist

import (
	"reflect"
	"strconv"
)

// envPatch 反射遍历 configPatch 树上所有带 env tag 的字段，
// 用 lookup 查值，命中就把值解析成叶子指针类型填入。
//
// 只认白名单（有 env tag）字段；secret 字段的注入也走这里。
// 无 env tag 的字段即便设了 LAB_XXX 也无效。
func envPatch(lookup func(string) (string, bool)) *configPatch {
	out := &configPatch{}
	fillEnv(reflect.ValueOf(out).Elem(), lookup)
	return out
}

// fillEnv 递归 patch 结构：
//   - 指针到子 struct（如 *agentPatch）：只有子树里至少有一个字段被填时才实例化。
//   - 指针到叶子（*string/*int/*bool）：读该字段 env tag，命中就 new 并赋值。
func fillEnv(sv reflect.Value, lookup func(string) (string, bool)) {
	st := sv.Type()
	for i := 0; i < sv.NumField(); i++ {
		fv := sv.Field(i)
		ft := st.Field(i)
		if fv.Kind() != reflect.Ptr {
			continue
		}
		// 指针到 struct → 子 patch。
		if fv.Type().Elem().Kind() == reflect.Struct {
			tmp := reflect.New(fv.Type().Elem())
			fillEnv(tmp.Elem(), lookup)
			if !isPatchEmpty(tmp.Elem()) {
				fv.Set(tmp)
			}
			continue
		}
		// 指针到叶子。
		tag := ft.Tag.Get("env")
		if tag == "" {
			continue
		}
		raw, ok := lookup(tag)
		if !ok {
			continue
		}
		leaf := reflect.New(fv.Type().Elem())
		if !setLeaf(leaf.Elem(), raw) {
			continue // 解析失败：忽略该条 env（fail-open，避免起服务因坏 env 崩）
		}
		fv.Set(leaf)
	}
}

// isPatchEmpty 判断子 patch struct 所有指针字段是否都是 nil。
func isPatchEmpty(sv reflect.Value) bool {
	for i := 0; i < sv.NumField(); i++ {
		fv := sv.Field(i)
		if fv.Kind() == reflect.Ptr && !fv.IsNil() {
			return false
		}
	}
	return true
}

// setLeaf 按目标叶子类型解析字符串 env 值。返回是否解析成功。
func setLeaf(dst reflect.Value, raw string) bool {
	switch dst.Kind() {
	case reflect.String:
		dst.SetString(raw)
		return true
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return false
		}
		dst.SetBool(b)
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return false
		}
		dst.SetInt(n)
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return false
		}
		dst.SetUint(n)
		return true
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return false
		}
		dst.SetFloat(f)
		return true
	default:
		return false
	}
}
