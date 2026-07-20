package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var standardParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

func NormalizeProposal(proposal Proposal, now time.Time) (NormalizedRule, error) {
	proposal.Timezone = strings.TrimSpace(proposal.Timezone)
	proposal.CronExpr = strings.Join(strings.Fields(proposal.CronExpr), " ")
	proposal.Prompt = strings.TrimSpace(proposal.Prompt)
	proposal.Description = strings.TrimSpace(proposal.Description)
	if proposal.Prompt == "" {
		return NormalizedRule{}, fmt.Errorf("prompt is required")
	}
	if proposal.Timezone == "" {
		return NormalizedRule{}, fmt.Errorf("timezone is required")
	}
	location, err := time.LoadLocation(proposal.Timezone)
	if err != nil {
		return NormalizedRule{}, fmt.Errorf("invalid timezone %q: %w", proposal.Timezone, err)
	}

	rule := NormalizedRule{Proposal: proposal}
	switch proposal.Kind {
	case KindCron:
		if proposal.CronExpr == "" {
			return NormalizedRule{}, fmt.Errorf("cron expression is required")
		}
		if !proposal.ScheduledAt.IsZero() {
			return NormalizedRule{}, fmt.Errorf("cron proposal cannot include scheduled_at")
		}
		schedule, err := parseCron(proposal.CronExpr, proposal.Timezone)
		if err != nil {
			return NormalizedRule{}, err
		}
		cursor := now.In(location)
		for range 3 {
			cursor = schedule.Next(cursor)
			if cursor.IsZero() {
				return NormalizedRule{}, fmt.Errorf("cron expression has no future occurrence")
			}
			rule.Next = append(rule.Next, cursor)
		}
		rule.Description = cronDescription(proposal.CronExpr, proposal.Description)
	case KindTimer:
		if proposal.CronExpr != "" {
			return NormalizedRule{}, fmt.Errorf("timer proposal cannot include cron expression")
		}
		if proposal.ScheduledAt.IsZero() {
			return NormalizedRule{}, fmt.Errorf("scheduled_at is required")
		}
		if !proposal.ScheduledAt.After(now) {
			return NormalizedRule{}, fmt.Errorf("scheduled_at must be in the future")
		}
		rule.ScheduledAt = proposal.ScheduledAt.UTC()
		rule.Next = []time.Time{proposal.ScheduledAt.In(location)}
		if proposal.Description != "" {
			rule.Description = proposal.Description
		} else {
			rule.Description = "一次性任务（" + proposal.ScheduledAt.In(location).Format("2006-01-02 15:04") + "）"
		}
	default:
		return NormalizedRule{}, fmt.Errorf("invalid schedule kind %q", proposal.Kind)
	}
	return rule, nil
}

func parseCron(expr, timezone string) (cron.Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must contain exactly 5 fields")
	}
	schedule, err := standardParser.Parse("CRON_TZ=" + timezone + " " + expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return schedule, nil
}

func cronDescription(expr, fallback string) string {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fallback
	}
	minute, minuteErr := strconv.Atoi(fields[0])
	hour, hourErr := strconv.Atoi(fields[1])
	if minuteErr == nil && hourErr == nil && fields[2] == "*" && fields[3] == "*" {
		at := fmt.Sprintf("%02d:%02d", hour, minute)
		switch fields[4] {
		case "*":
			return "每天 " + at
		case "1-5":
			return "每个工作日 " + at
		case "0":
			return "每周日 " + at
		case "1":
			return "每周一 " + at
		case "2":
			return "每周二 " + at
		case "3":
			return "每周三 " + at
		case "4":
			return "每周四 " + at
		case "5":
			return "每周五 " + at
		case "6":
			return "每周六 " + at
		}
	}
	if fallback != "" {
		return fallback
	}
	return "Cron `" + expr + "`"
}

func Describe(task Task, now time.Time) string {
	location, err := time.LoadLocation(task.Timezone)
	if err != nil {
		location = time.UTC
	}
	if task.Kind == KindTimer {
		return fmt.Sprintf("%s（%s，%s）", task.Description, task.ScheduledAt.In(location).Format("2006-01-02 15:04"), task.Timezone)
	}
	next := task.NextRun.In(location)
	if next.Before(now.In(location)) {
		return fmt.Sprintf("%s（%s）", task.Description, task.Timezone)
	}
	return fmt.Sprintf("%s（%s；下次 %s）", task.Description, task.Timezone, next.Format("2006-01-02 15:04"))
}
