package testfw

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildReport_AllPassed 锁死"全绿"用例集产出的 Report 字段。
func TestBuildReport_AllPassed(t *testing.T) {
	results := []TestResult{
		{Case: TestCase{Name: "A", Tags: []string{"smoke"}, Steps: []Step{{Input: "/help"}, {Input: "/stop"}}}, Passed: true},
		{Case: TestCase{Name: "B", Tags: []string{"regression"}, Steps: []Step{{Input: "/config"}}}, Passed: true},
	}
	start := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Second)

	rep := BuildReport(results, start, end, []string{"smoke", "regression"}, "/tmp/src")

	if rep.Version != 1 {
		t.Errorf("Version = %d, want 1", rep.Version)
	}
	if !rep.AllPassed || rep.Passed != 2 || rep.Failed != 0 || rep.Total != 2 {
		t.Errorf("聚合数值错: %+v", rep)
	}
	if rep.DurationMs != 2000 {
		t.Errorf("DurationMs = %d, want 2000", rep.DurationMs)
	}
	if rep.StartedAt != "2026-07-27T10:00:00Z" {
		t.Errorf("StartedAt = %q", rep.StartedAt)
	}
	if len(rep.Cases) != 2 || rep.Cases[0].StepCount != 2 || rep.Cases[1].StepCount != 1 {
		t.Errorf("Cases 结构错: %+v", rep.Cases)
	}
	for _, c := range rep.Cases {
		if !c.Passed || len(c.FailedSteps) != 0 || c.Error != "" {
			t.Errorf("全绿 case 应无 failed_steps/error: %+v", c)
		}
	}
}

// TestBuildReport_WithFailures 锁死失败断言的透传。
func TestBuildReport_WithFailures(t *testing.T) {
	results := []TestResult{
		{
			Case:   TestCase{Name: "X", Steps: []Step{{Input: "/config"}}},
			Passed: false,
			Failed: []FailedAssert{{
				Step:      1,
				StepInput: "/config",
				Assert:    Assert{Type: "event_type", Expected: "message"},
				Message:   "期望 event 类型 'message',实际 'config'",
			}},
		},
		{
			Case:   TestCase{Name: "Y", Steps: []Step{{Input: "/help"}}},
			Passed: false,
			Error:  errors.New("simulate 执行失败"),
		},
	}
	rep := BuildReport(results, time.Now(), time.Now(), nil, "")

	if rep.AllPassed || rep.Passed != 0 || rep.Failed != 2 {
		t.Errorf("失败聚合错: %+v", rep)
	}
	if len(rep.Cases[0].FailedSteps) != 1 {
		t.Fatalf("失败断言应透传")
	}
	if rep.Cases[0].FailedSteps[0].Message == "" {
		t.Errorf("Message 不应为空")
	}
	if rep.Cases[1].Error != "simulate 执行失败" {
		t.Errorf("Error 未透传: %q", rep.Cases[1].Error)
	}
}

// TestWriteReport_AtomicJSON 锁死写盘契约:JSON 可回读、all_passed 字段可被外部消费。
func TestWriteReport_AtomicJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reports", "testfw.json") // 故意加一层不存在的子目录
	rep := Report{Version: 1, AllPassed: true, Total: 3, Passed: 3}
	if err := WriteReport(rep, path); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(buf, &back); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if back["all_passed"] != true {
		t.Errorf("all_passed 未落盘: %v", back)
	}
	// tmp 文件不应残留
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp 文件残留: %v", err)
	}
}

// TestWriteReport_EmptyPathRejected 反证:空路径必须拒绝(避免静默不写)。
func TestWriteReport_EmptyPathRejected(t *testing.T) {
	err := WriteReport(Report{}, "")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("空路径应报错,实际: %v", err)
	}
}
