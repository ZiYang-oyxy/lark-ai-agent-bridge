package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"lark-agent-bridge/internal/testfw"
)

func main() {
	var (
		sourceDir  = flag.String("source", ".", "Bridge源码根目录路径")
		goBin      = flag.String("go-bin", "go", "Go二进制路径")
		testDir    = flag.String("test-dir", "tests", "测试用例根目录")
		tagsStr    = flag.String("tags", "", "要运行的测试标签，逗号分隔（如smoke,config）")
		smoke      = flag.Bool("smoke", false, "仅运行冒烟测试（等价于--tags smoke）")
		regression = flag.Bool("regression", false, "运行全量回归测试（等价于--tags smoke,regression）")
		reportJSON      = flag.String("report-json", "", "可选:测试结束时把结构化 Report 写到此路径(JSON)。适合作为发布凭证 sidecar,发布脚本可读回 all_passed 字段。")
		emitChecklist   = flag.String("emit-l3-checklist", "", "可选:从已加载的用例集生成 L3 E2E 手工/半自动 checklist(Markdown)到此路径。只输出用例,不执行测试(用 --smoke/--regression 决定用例范围)。")
		checklistOnly   = flag.Bool("checklist-only", false, "配合 --emit-l3-checklist:只生成清单,跳过 simulate 执行。")
	)
	flag.Parse()

	runner, err := testfw.NewRunner(*sourceDir, *goBin)
	if err != nil {
		fmt.Printf("初始化测试运行器失败: %v\n", err)
		os.Exit(1)
	}

	// 确定要运行的标签
	var tags []string
	if *smoke {
		tags = append(tags, "smoke")
	} else if *regression {
		tags = append(tags, "smoke", "regression")
	} else if *tagsStr != "" {
		tags = strings.Split(*tagsStr, ",")
	}

	// 加载测试用例。smoke 与 regression 两个子目录都是可选的:
	// 目录不存在时跳过(而非报错),否则 --smoke 在没有 regression/ 目录时会整体失败。
	var allTests []testfw.TestCase
	for _, sub := range []string{"smoke", "regression"} {
		dir := *testDir + "/" + sub
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			continue // 目录缺失或不是目录:跳过
		}
		cases, loadErr := runner.LoadTestDir(dir, tags)
		if loadErr != nil {
			fmt.Printf("加载 %s 测试用例失败: %v\n", sub, loadErr)
			os.Exit(1)
		}
		allTests = append(allTests, cases...)
	}

	if len(allTests) == 0 {
		fmt.Println("未找到匹配的测试用例")
		os.Exit(0)
	}

	// L3 checklist:只依赖用例集本身,不需要执行 simulate。
	if *emitChecklist != "" {
		items := testfw.BuildL3Checklist(allTests, nil)
		if err := testfw.WriteL3Checklist(items, *emitChecklist); err != nil {
			fmt.Printf("生成 L3 checklist 失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("📋 L3 checklist 已写入: %s (%d 条)\n", *emitChecklist, len(items))
		if *checklistOnly {
			return
		}
	}

	fmt.Printf("找到 %d 个测试用例，开始执行...\n\n", len(allTests))
	startedAt := time.Now()
	results := runner.RunAll(allTests)
	finishedAt := time.Now()

	// 先写 JSON 报告(即使 PrintReport 后续会 os.Exit(1) 也不影响),
	// 让发布脚本/CI 可以拿到结构化 all_passed。
	if *reportJSON != "" {
		rep := testfw.BuildReport(results, startedAt, finishedAt, tags, *sourceDir)
		rep.GoVersion = runtime.Version()
		if err := testfw.WriteReport(rep, *reportJSON); err != nil {
			fmt.Printf("写入 JSON 报告失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("📄 JSON 报告已写入: %s\n", *reportJSON)
	}

	testfw.PrintReport(results) // 失败会 os.Exit(1)
}
