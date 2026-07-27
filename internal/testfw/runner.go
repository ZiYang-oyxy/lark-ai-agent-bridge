package testfw

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Runner 测试运行器
type Runner struct {
	SourceDir string // Bridge源码根目录
	GoBin     string // Go二进制路径
}

// NewRunner 创建测试运行器
func NewRunner(sourceDir, goBin string) (*Runner, error) {
	if goBin == "" {
		goBin = "go"
	}
	// 转换源码目录为绝对路径
	absSourceDir, err := filepath.Abs(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("获取源码目录绝对路径失败: %w", err)
	}
	return &Runner{
		SourceDir: absSourceDir,
		GoBin:     goBin,
	}, nil
}

// LoadTestCase 从YAML文件加载单个测试用例
func (r *Runner) LoadTestCase(path string) (*TestCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取测试用例 %s 失败: %w", path, err)
	}
	var tc TestCase
	if err := yaml.Unmarshal(data, &tc); err != nil {
		return nil, fmt.Errorf("解析测试用例 %s 失败: %w", path, err)
	}
	return &tc, nil
}

// LoadTestDir 加载目录下所有匹配标签的测试用例
func (r *Runner) LoadTestDir(dir string, tags []string) ([]TestCase, error) {
	var tests []TestCase
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml")) {
			return nil
		}
		tc, err := r.LoadTestCase(path)
		if err != nil {
			return err
		}
		if len(tags) > 0 && !matchTags(tc.Tags, tags) {
			return nil
		}
		tests = append(tests, *tc)
		return nil
	})
	return tests, err
}

// matchTags 检查用例标签是否包含所有需要的标签
func matchTags(caseTags, requiredTags []string) bool {
	tagSet := make(map[string]bool)
	for _, t := range caseTags {
		tagSet[strings.ToLower(t)] = true
	}
	for _, rt := range requiredTags {
		if !tagSet[strings.ToLower(rt)] {
			return false
		}
	}
	return true
}

// RunStep 执行单个测试步骤（发送文本消息到simulate）
func (r *Runner) RunStep(input string, workDir string, isGroup bool) (*SimulateOutput, error) {
	// 转换工作目录为绝对路径
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("获取工作目录绝对路径失败: %w", err)
	}
	if err := os.MkdirAll(absWorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建工作目录失败: %w", err)
	}

	args := []string{
		"run", "./cmd/lark-agent-bridge",
		"simulate",
		"--text", input,
		"--default-workdir", absWorkDir,
	}
	if isGroup {
		args = append(args, "--group")
	}
	cmd := exec.Command(r.GoBin, args...)
	cmd.Dir = r.SourceDir
	goCacheDir := filepath.Join(r.SourceDir, ".cache/go-build")
	cmd.Env = append(os.Environ(),
		"E2E_PREFERENCE_STORE="+filepath.Join(absWorkDir, "preferences.json"),
		"E2E_REPLY_STORE="+filepath.Join(absWorkDir, "replies.json"),
		"E2E_MEDIA_CACHE_DIR="+filepath.Join(absWorkDir, "media-cache"),
		"E2E_SESSION_STORE="+filepath.Join(absWorkDir, "sessions"),
		"GOCACHE="+goCacheDir,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("simulate命令执行失败: %w\nstderr: %s", err, stderr.String())
	}

	var out SimulateOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("解析simulate输出失败: %w\n输出: %s", err, stdout.String())
	}
	return &out, nil
}

// RunTestCase 执行单个测试用例
func (r *Runner) RunTestCase(tc TestCase) TestResult {
	result := TestResult{
		Case:    tc,
		Passed:  true,
		Failed:  make([]FailedAssert, 0),
		Outputs: make([]SimulateOutput, 0, len(tc.Steps)),
	}

	// 创建临时工作目录
	workDir, err := os.MkdirTemp("", "bridge-test-*")
	if err != nil {
		result.Error = fmt.Errorf("创建临时目录失败: %w", err)
		result.Passed = false
		return result
	}
	defer os.RemoveAll(workDir)

	for i, step := range tc.Steps {
		output, err := r.RunStep(step.Input, workDir, step.Group)
		if err != nil {
			result.Error = fmt.Errorf("步骤 %d (%s) 执行失败: %w", i+1, step.Input, err)
			result.Passed = false
			return result
		}
		result.Outputs = append(result.Outputs, *output)

		for _, assert := range step.Asserts {
			ok, msg := RunAssert(assert, output)
			if !ok {
				result.Passed = false
				result.Failed = append(result.Failed, FailedAssert{
					Step:      i + 1,
					StepInput: step.Input,
					Assert:    assert,
					Message:   msg,
				})
			}
		}
	}

	return result
}

// RunAll 执行所有测试用例
func (r *Runner) RunAll(tests []TestCase) []TestResult {
	results := make([]TestResult, 0, len(tests))
	for _, tc := range tests {
		res := r.RunTestCase(tc)
		results = append(results, res)
	}
	return results
}

// PrintReport 打印测试报告
func PrintReport(results []TestResult) {
	passed := 0
	failed := 0
	total := len(results)

	for _, res := range results {
		if res.Passed {
			passed++
			fmt.Printf("✅ PASS: %s\n", res.Case.Name)
		} else {
			failed++
			fmt.Printf("❌ FAIL: %s\n", res.Case.Name)
			if res.Error != nil {
				fmt.Printf("   错误: %v\n", res.Error)
			}
			for _, f := range res.Failed {
				fmt.Printf("   步骤%d (%s): %s\n", f.Step, f.StepInput, f.Message)
			}
		}
	}

	fmt.Printf("\n=== 测试汇总 ===\n")
	fmt.Printf("总计: %d, 通过: %d, 失败: %d\n", total, passed, failed)
	if failed == 0 {
		fmt.Println("🎉 所有测试通过!")
	} else {
		fmt.Printf("💥 %d 个测试失败\n", failed)
		os.Exit(1)
	}
}
