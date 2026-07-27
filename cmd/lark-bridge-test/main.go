package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

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

	fmt.Printf("找到 %d 个测试用例，开始执行...\n\n", len(allTests))
	results := runner.RunAll(allTests)
	testfw.PrintReport(results)
}
