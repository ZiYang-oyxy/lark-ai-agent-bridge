package testfw

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSmokeSuite 把 tests/smoke 冒烟套件接入标准 `go test` 流程。
//
// 目的:发布凭证(cmd/lark-bridge-release 的 test-evidence)跑的是 `go test ./...`,
// 而冒烟套件本身是独立的 lark-bridge-test 二进制。通过这个测试把 smoke 纳入
// `go test ./...`,冒烟套件就自动成为发布门禁的一部分 —— 无需改动发布关键路径。
//
// 每个 step 会 `go run ./cmd/lark-agent-bridge`(编译+执行),较慢,故:
//   - `go test -short` 时跳过(本地快速迭代用);
//   - 完整 `go test ./...`(发布凭证、CI)会执行。
func TestSmokeSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke 套件较慢,-short 模式跳过")
	}

	repoRoot := moduleRoot(t)
	goBin := goBinary()

	runner, err := NewRunner(repoRoot, goBin)
	if err != nil {
		t.Fatalf("创建 runner 失败: %v", err)
	}

	smokeDir := filepath.Join(repoRoot, "tests", "smoke")
	cases, err := runner.LoadTestDir(smokeDir, []string{"smoke"})
	if err != nil {
		t.Fatalf("加载 smoke 用例失败: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("未加载到任何 smoke 用例")
	}

	for _, res := range runner.RunAll(cases) {
		if res.Error != nil {
			t.Errorf("用例 %q 执行错误: %v", res.Case.Name, res.Error)
			continue
		}
		for _, f := range res.Failed {
			t.Errorf("用例 %q 步骤%d (%s): %s", res.Case.Name, f.Step, f.StepInput, f.Message)
		}
	}
}

// moduleRoot 用 `go env GOMOD` 定位仓库根(go.mod 所在目录),
// 不依赖测试的 CWD 或写死的 ../.. 相对路径。
func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command(goBinary(), "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD 失败: %v", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		t.Skip("非 module 模式,跳过 smoke 套件")
	}
	return filepath.Dir(gomod)
}

// goBinary 返回可用的 go 可执行路径:优先 PATH,其次服务器固定路径。
func goBinary() string {
	if _, err := exec.LookPath("go"); err == nil {
		return "go"
	}
	const linuxGo = "/usr/local/go/bin/go"
	if _, err := os.Stat(linuxGo); err == nil {
		return linuxGo
	}
	return "go"
}
