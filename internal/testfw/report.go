package testfw

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Report 是一次 test run 的结构化产物,可绑到发布凭证或存档。
// 不带过程日志,只带结论——保证在多轮迭代下 diff 干净。
type Report struct {
	Version      int          `json:"version"`
	StartedAt    string       `json:"started_at"`
	FinishedAt   string       `json:"finished_at"`
	DurationMs   int64        `json:"duration_ms"`
	Total        int          `json:"total"`
	Passed       int          `json:"passed"`
	Failed       int          `json:"failed"`
	AllPassed    bool         `json:"all_passed"`
	Tags         []string     `json:"tags,omitempty"`
	Source       string       `json:"source,omitempty"`
	GoVersion    string       `json:"go_version,omitempty"`
	Cases        []CaseReport `json:"cases"`
}

// CaseReport 是单个测试用例的结构化结论。
type CaseReport struct {
	Name        string          `json:"name"`
	Tags        []string        `json:"tags,omitempty"`
	Passed      bool            `json:"passed"`
	StepCount   int             `json:"step_count"`
	FailedSteps []FailedStepRow `json:"failed_steps,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// FailedStepRow 是一条失败断言的最小描述,用于 report 里的定位。
// 不复制 SimulateOutput(体积大且对报告消费者无用)。
type FailedStepRow struct {
	StepIndex int    `json:"step_index"`
	Label     string `json:"label"`
	Assert    Assert `json:"assert"`
	Message   string `json:"message"`
}

// BuildReport 从 RunAll 的结果构造 Report。startedAt/finishedAt 由调用方
// 传入(避免包内引入不确定时钟依赖),tags/source 是可选元信息。
func BuildReport(results []TestResult, startedAt, finishedAt time.Time, tags []string, source string) Report {
	rep := Report{
		Version:    1,
		StartedAt:  startedAt.UTC().Format(time.RFC3339),
		FinishedAt: finishedAt.UTC().Format(time.RFC3339),
		DurationMs: finishedAt.Sub(startedAt).Milliseconds(),
		Total:      len(results),
		Tags:       tags,
		Source:     source,
		Cases:      make([]CaseReport, 0, len(results)),
	}
	for _, res := range results {
		cr := CaseReport{
			Name:      res.Case.Name,
			Tags:      res.Case.Tags,
			Passed:    res.Passed,
			StepCount: len(res.Case.Steps),
		}
		if res.Error != nil {
			cr.Error = res.Error.Error()
		}
		for _, f := range res.Failed {
			cr.FailedSteps = append(cr.FailedSteps, FailedStepRow{
				StepIndex: f.Step,
				Label:     f.StepInput,
				Assert:    f.Assert,
				Message:   f.Message,
			})
		}
		if res.Passed {
			rep.Passed++
		} else {
			rep.Failed++
		}
		rep.Cases = append(rep.Cases, cr)
	}
	rep.AllPassed = rep.Failed == 0
	return rep
}

// WriteReport 把 Report 写到 path(原子:先写临时文件再 rename)。
// 目录不存在时自动创建。适合作为发布凭证 sidecar。
func WriteReport(rep Report, path string) error {
	if path == "" {
		return fmt.Errorf("report path is empty")
	}
	dir := ""
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			dir = path[:i]
			break
		}
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir report dir: %w", err)
		}
	}
	buf, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return fmt.Errorf("write report tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename report: %w", err)
	}
	return nil
}
