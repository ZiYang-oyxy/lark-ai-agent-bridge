package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeCronProposalUsesExplicitTimezone(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	rule, err := NormalizeProposal(Proposal{
		Kind:        KindCron,
		CronExpr:    "0 9 * * 1-5",
		Timezone:    "Asia/Shanghai",
		Description: "工作日总结",
		Prompt:      "总结昨天的项目进展",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := rule.Next[0].Format(time.RFC3339); got != "2026-07-21T09:00:00+08:00" {
		t.Fatalf("first next = %s", got)
	}
	if got := rule.Next[2].Format(time.RFC3339); got != "2026-07-23T09:00:00+08:00" {
		t.Fatalf("third next = %s", got)
	}
	if !strings.Contains(rule.Description, "工作日") || !strings.Contains(rule.Description, "09:00") {
		t.Fatalf("description = %q", rule.Description)
	}
}

func TestNormalizeTimerProposalRequiresFutureAbsoluteTime(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	_, err := NormalizeProposal(Proposal{
		Kind:        KindTimer,
		ScheduledAt: now,
		Timezone:    "Asia/Shanghai",
		Prompt:      "提醒我开会",
	}, now)
	if err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeProposalRejectsMixedRuleKinds(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	_, err := NormalizeProposal(Proposal{
		Kind:        KindCron,
		CronExpr:    "0 9 * * *",
		ScheduledAt: now.Add(time.Hour),
		Timezone:    "UTC",
		Prompt:      "bad",
	}, now)
	if err == nil {
		t.Fatal("expected mixed-rule validation error")
	}
}

func TestRunIDIsStableInUTC(t *testing.T) {
	at := time.Date(2026, 7, 22, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	if got := RunID(KindCron, "abc12345", at); got != "cron:abc12345:2026-07-22T01:00:00Z" {
		t.Fatalf("run ID = %q", got)
	}
}
