package testfw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildL3Checklist_SkipsActionSteps(t *testing.T) {
	falseP := false
	cases := []TestCase{{
		Name: "案例A",
		Steps: []Step{
			{Input: "/help", Asserts: []Assert{{Type: "segment_contains", Text: "会话"}}},
			{Action: "stop"}, // 应被跳过
			{Input: "/stop"},
			{Input: "/help", Group: true, Mentioned: &falseP}, // 群未@:不应期望 audit(过滤)
		},
	}}
	items := BuildL3Checklist(cases, nil)
	if len(items) != 3 {
		t.Fatalf("action step 应跳过,期望 3 条 input step,实际 %d: %+v", len(items), items)
	}
	// 找到"群未@"那条,验证不应有 audit 期望(过滤场景没 cardkit)
	var groupUnmentioned *L3ChecklistItem
	for i := range items {
		if strings.Contains(items[i].CaseName, "step 4") {
			groupUnmentioned = &items[i]
		}
	}
	if groupUnmentioned == nil {
		t.Fatalf("找不到群未@的 step")
	}
	for _, a := range groupUnmentioned.Assertions {
		if strings.Contains(a, "cardkit_create") {
			t.Errorf("群未@场景不应期望 cardkit_create audit: %+v", groupUnmentioned)
		}
	}
	// step 1 (/help) 应含用户可见文案断言
	if !strings.Contains(strings.Join(items[0].Assertions, " · "), "会话") {
		t.Errorf("segment_contains 未映射到 visible 断言: %+v", items[0])
	}
}

func TestWriteL3Checklist_Markdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "L3.md")
	items := []L3ChecklistItem{
		{CaseName: "A [step 1]", Command: "/help", Assertions: []string{"audit: cardkit_create + cardkit_reply", "visible: 卡片正文含 \"会话\""}},
	}
	if err := WriteL3Checklist(items, path); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(buf)
	if !strings.Contains(content, "Bridge L3 E2E Checklist") {
		t.Errorf("缺标题")
	}
	if !strings.Contains(content, "`/help`") {
		t.Errorf("缺命令: %s", content)
	}
	if !strings.Contains(content, "cardkit_create") {
		t.Errorf("缺 audit 期望")
	}
}
