package bridgeinstructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentCatalogContainsExplicitImageDeliveryCompletionContract(t *testing.T) {
	text, ok := Content(CurrentVersion)
	if !ok {
		t.Fatal("current version missing")
	}
	for _, want := range []string{
		"明确要求生成、绘制、展示或发送图片",
		"生成图片本身即包含通过飞书交付图片",
		"把刚才那张图发出来",
		"多个合理候选时先询问用户",
		"![简短说明](./relative-path.png)",
		"不得只说明“图片已生成”或只返回本地文件路径",
		"发送最终回复前必须自检",
		"如果前两项为“是”而第三项为“否”，不得结束回复",
		"不要自行声称图片已经发送成功",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("current instructions missing %q", want)
		}
	}
}

func TestCatalogRetainsPinnedV2Instructions(t *testing.T) {
	text, ok := Content("v2")
	if !ok {
		t.Fatal("v2 missing")
	}
	if !strings.Contains(text, "自然语言定时意图") || !strings.Contains(text, "![简短说明](./relative-path.png)") {
		t.Fatal("v2 content changed unexpectedly")
	}
}

func TestCurrentCatalogContainsScheduleProposalContract(t *testing.T) {
	if CurrentVersion != "v3" {
		t.Fatalf("CurrentVersion = %q, want v3", CurrentVersion)
	}
	text, ok := Content(CurrentVersion)
	if !ok {
		t.Fatal("current version missing")
	}
	for _, want := range []string{
		"自然语言定时意图",
		`"$LAB_SCHEDULE_CLI" schedule propose`,
		"--kind cron",
		"--kind timer",
		"不要声称任务已经创建",
		"用户确认",
		"CronCreate",
		"ScheduleWakeup",
		"严禁",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("current instructions missing %q", want)
		}
	}
}

func TestRuntimeMaterializesOnePrivateReusableClaudeFile(t *testing.T) {
	rt, err := NewRuntime()
	if err != nil {
		t.Fatal(err)
	}
	path1, err := rt.ClaudeFile(CurrentVersion)
	if err != nil {
		t.Fatal(err)
	}
	path2, err := rt.ClaudeFile(CurrentVersion)
	if err != nil {
		t.Fatal(err)
	}
	if path1 != path2 {
		t.Fatalf("paths differ: %q %q", path1, path2)
	}
	assertMode(t, filepath.Dir(path1), 0o700)
	assertMode(t, path1, 0o600)
	got, err := os.ReadFile(path1)
	if err != nil {
		t.Fatal(err)
	}
	want, ok := Content(CurrentVersion)
	if !ok {
		t.Fatal("current version missing")
	}
	if string(got) != want {
		t.Fatal("materialized content differs")
	}
	dir := filepath.Dir(path1)
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("runtime directory survived close: %v", err)
	}
}

func TestRuntimeMaterializesVersionsIndependently(t *testing.T) {
	rt, err := newRuntime(map[string]string{"v1": "one", "v2": "two"})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	path1, err := rt.ClaudeFile("v1")
	if err != nil {
		t.Fatal(err)
	}
	path2, err := rt.ClaudeFile("v2")
	if err != nil {
		t.Fatal(err)
	}
	if path1 == path2 {
		t.Fatalf("versions share path %q", path1)
	}
	if got, err := rt.Content("v1"); err != nil || got != "one" {
		t.Fatalf("v1 content = %q, %v", got, err)
	}
	if got, err := rt.Content("v2"); err != nil || got != "two" {
		t.Fatalf("v2 content = %q, %v", got, err)
	}
}

func TestRuntimeRejectsUnknownVersion(t *testing.T) {
	rt, err := NewRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.ClaudeFile("v999"); err == nil || !strings.Contains(err.Error(), "unsupported bridge instructions version") {
		t.Fatalf("error = %v", err)
	}
	if _, err := rt.Content("v999"); err == nil || !strings.Contains(err.Error(), "unsupported bridge instructions version") {
		t.Fatalf("error = %v", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%q) = %04o, want %04o", path, got, want)
	}
}
