package bridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/bridgeinstructions"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/schedule"
	"lark-agent-bridge/internal/session"
)

func (s *Service) Enqueue(_ context.Context, task schedule.Task, run schedule.Run) (schedule.EnqueueResult, error) {
	if s.isUpgradeMaintenance() {
		return schedule.EnqueueResult{}, schedule.ErrQueueFull
	}
	s.upgradeGate.RLock()
	defer s.upgradeGate.RUnlock()
	if s.isUpgradeMaintenance() {
		return schedule.EnqueueResult{}, schedule.ErrQueueFull
	}
	kind, ok := agent.ParseKind(task.Execution.Agent)
	if !ok {
		return schedule.EnqueueResult{}, fmt.Errorf("scheduled task has invalid agent %q", task.Execution.Agent)
	}
	// Scheduled executions own a task-scoped session. Reusing the conversation
	// key would inherit an unrelated AgentSessionID from ordinary chat (or an
	// E2E fixture), causing the runner to pass an invalid --resume value. The
	// task namespace keeps recurring runs continuous without mutating the user's
	// interactive conversation session.
	key := session.Key{Agent: kind, ChatID: task.Target.ChatID, Thread: "schedule:" + task.ID}
	now := time.Now()
	input := session.Input{
		ID: run.ID, Sender: task.Creator, Text: task.Prompt, ReplyToMessageID: task.Target.ReplyToMessageID,
		CardSessionID: runID(key.ID(), run.ID), WorkDir: task.Execution.WorkDir,
		RequestedModel: task.Execution.Model, RequestedEffort: task.Execution.Effort,
		AgentBin: task.Execution.AgentBin, AgentHome: task.Execution.AgentHome,
		ReplyMode: config.ReplyMode(task.Execution.ReplyMode), ConversationMode: config.ConversationMode(task.Execution.ConversationMode),
		BridgeInstructionsVersion: bridgeinstructions.CurrentVersion,
		ScheduleRunID:             run.ID, ScheduleTaskID: task.ID, IsGroup: task.Target.IsGroup,
		Time: now, State: session.InputQueued,
	}
	accepted, _, err := s.Sessions.AcceptAndEnqueue(key, input, now, s.dedupTTL(), s.dedupMaxEntries(), s.batchLimits())
	if err != nil {
		if strings.Contains(err.Error(), "pending input limit") {
			return schedule.EnqueueResult{}, fmt.Errorf("%w: %v", schedule.ErrQueueFull, err)
		}
		return schedule.EnqueueResult{}, err
	}
	if !accepted {
		return schedule.EnqueueResult{Duplicate: true}, nil
	}
	s.Audit.Record("schedule", "schedule_run_queued", run.ID, "task="+task.ID+" scope="+key.ID())
	return schedule.EnqueueResult{}, nil
}

func (s *Service) Notify(_ context.Context, task schedule.Task, message string) error {
	mode := config.ConversationMode(task.Execution.ConversationMode)
	return s.renderTextWithMode("schedule-notice-"+task.ID, task.Target.ReplyToMessageID, card.SegmentError, message, mode)
}

func (s *Service) handlePendingScheduleReply(msg Message, preference config.RuntimePreference) (bool, error) {
	answer := strings.TrimSpace(msg.Text)
	if s.Schedules == nil || (answer != "确认" && answer != "取消") {
		return false, nil
	}
	drafts := s.liveScheduleDrafts(msg, effectiveMessageTime(msg))
	if len(drafts) != 1 {
		return false, nil
	}
	accepted, err := s.Sessions.AcceptMessage(msg.ID, effectiveMessageTime(msg), s.dedupTTL(), s.dedupMaxEntries())
	if err != nil || !accepted {
		return true, err
	}
	if answer == "取消" {
		if err := s.Schedules.CancelDraft(drafts[0].ID, msg.Sender); err != nil {
			return true, err
		}
		s.Audit.Record(msg.Sender, "schedule_draft_cancelled", drafts[0].ID, "text confirmation")
		return true, s.renderTextWithMode("schedule-cancelled", msg.ID, card.SegmentText, "已取消，不会创建定时任务。", preference.ConversationMode)
	}
	task, err := s.confirmScheduleDraft(drafts[0].ID, msg.Sender, effectiveMessageTime(msg))
	if err != nil {
		return true, err
	}
	return true, s.renderTextWithMode("schedule-confirmed", msg.ID, card.SegmentText, confirmedScheduleText(task), preference.ConversationMode)
}

func (s *Service) liveScheduleDrafts(msg Message, now time.Time) []schedule.Draft {
	var out []schedule.Draft
	for _, draft := range s.Schedules.Drafts() {
		if draft.Creator != msg.Sender || draft.Target.ChatID != msg.ChatID || draft.Target.ThreadID != msg.ThreadID {
			continue
		}
		if !draft.ExpiresAt.IsZero() && !draft.ExpiresAt.After(now) {
			continue
		}
		out = append(out, draft)
	}
	return out
}

func (s *Service) handleScheduleCommand(ctx context.Context, msg Message, cmd Command, preference config.RuntimePreference) error {
	if s.Schedules == nil {
		return s.renderTextWithMode("schedule-unavailable", msg.ID, card.SegmentError, "定时任务服务尚未配置。", preference.ConversationMode)
	}
	kind := schedule.KindCron
	label := "cron"
	if cmd.Type == CommandTimer {
		kind = schedule.KindTimer
		label = "timer"
	}
	fields := strings.Fields(cmd.Text)
	sub := "list"
	var args []string
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
		args = fields[1:]
	}
	switch sub {
	case "list":
		if len(args) > 1 || (len(args) == 1 && !strings.EqualFold(args[0], "all")) {
			return s.renderTextWithMode(label+"-usage", msg.ID, card.SegmentError, "参数不正确。\n\n"+scheduleUsageText(kind), preference.ConversationMode)
		}
		all := len(args) == 1 && strings.EqualFold(args[0], "all")
		if all && !s.canRunAdminCommand(msg.Sender) {
			return s.renderTextWithMode("schedule-denied", msg.ID, card.SegmentError, "仅管理员可查看全部定时任务。", preference.ConversationMode)
		}
		return s.renderTextWithMode(label+"-list", msg.ID, card.SegmentText, s.scheduleListText(kind, msg, all), preference.ConversationMode)
	case "add":
		request := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cmd.Text), fields[0]))
		if request == "" {
			return s.renderTextWithMode(label+"-usage", msg.ID, card.SegmentError, "缺少自然语言任务描述。\n\n"+scheduleUsageText(kind), preference.ConversationMode)
		}
		run := Command{Type: CommandRun, Agent: cmd.Agent, Text: scheduleAddPrompt(kind, request), Raw: cmd.Raw, ScheduleKind: string(kind)}
		return s.runWithPreference(ctx, run, msg, "", preference)
	case "info", "del", "delete", "remove", "rm", "run", "enable", "disable":
		if len(args) != 1 {
			return s.renderTextWithMode(label+"-usage", msg.ID, card.SegmentError, "任务 ID 参数不正确。\n\n"+scheduleUsageText(kind), preference.ConversationMode)
		}
		task, ok := s.Schedules.Task(args[0])
		if !ok || task.Kind != kind {
			return s.renderTextWithMode(label+"-missing", msg.ID, card.SegmentError, "找不到该定时任务。", preference.ConversationMode)
		}
		if !s.canManageSchedule(msg, task) {
			return s.renderTextWithMode(label+"-denied", msg.ID, card.SegmentError, "无权管理该定时任务。", preference.ConversationMode)
		}
		switch sub {
		case "info":
			return s.renderTextWithMode(label+"-info", msg.ID, card.SegmentText, scheduleTaskText(task), preference.ConversationMode)
		case "del", "delete", "remove", "rm":
			var err error
			if s.Scheduler != nil {
				err = s.Scheduler.Remove(task.ID)
			} else {
				err = s.Schedules.DeleteTask(task.ID)
			}
			if err != nil {
				return err
			}
			s.Audit.Record(msg.Sender, "schedule_task_deleted", task.ID, string(task.Kind))
			return s.renderTextWithMode(label+"-deleted", msg.ID, card.SegmentText, fmt.Sprintf("已删除任务 `%s`。", task.ID), preference.ConversationMode)
		case "run":
			if kind != schedule.KindCron {
				return s.renderTextWithMode(label+"-usage", msg.ID, card.SegmentError, "一次性 timer 不支持手动重复执行。\n\n"+scheduleUsageText(kind), preference.ConversationMode)
			}
			if s.Scheduler == nil {
				return errors.New("schedule engine is not configured")
			}
			if err := s.Scheduler.RunNow(ctx, task.ID); err != nil {
				return err
			}
			return s.renderTextWithMode(label+"-run", msg.ID, card.SegmentText, fmt.Sprintf("已触发任务 `%s`。", task.ID), preference.ConversationMode)
		case "enable", "disable":
			if kind != schedule.KindCron {
				return s.renderTextWithMode(label+"-usage", msg.ID, card.SegmentError, "一次性 timer 不支持启用或停用。\n\n"+scheduleUsageText(kind), preference.ConversationMode)
			}
			enabled := sub == "enable"
			var updateErr error
			if s.Scheduler != nil {
				_, updateErr = s.Scheduler.SetEnabled(task.ID, enabled)
			} else {
				_, updateErr = s.Schedules.SetTaskEnabled(task.ID, enabled)
			}
			if updateErr != nil {
				return updateErr
			}
			state := "停用"
			if enabled {
				state = "启用"
			}
			return s.renderTextWithMode(label+"-state", msg.ID, card.SegmentText, fmt.Sprintf("已%s任务 `%s`。", state, task.ID), preference.ConversationMode)
		}
	}
	return s.renderTextWithMode(label+"-usage", msg.ID, card.SegmentError, "未知子命令。\n\n"+scheduleUsageText(kind), preference.ConversationMode)
}

func scheduleHelpLines() []string {
	return []string{
		"**`/cron`** `[list [all]]` 查看周期任务",
		"**`/cron`** `add <自然语言任务>` 创建 · **`info|run|enable|disable|del <id>`** 管理",
		"**`/timer`** `[list [all]]` 查看一次性任务",
		"**`/timer`** `add <自然语言任务>` 创建 · **`info|del <id>`** 管理",
	}
}

func scheduleUsageText(kind schedule.Kind) string {
	if kind == schedule.KindTimer {
		return strings.Join([]string{
			"**`/timer` / `/timer list`** 查看当前会话的一次性任务",
			"**`/timer list all`** 查看全部一次性任务（仅管理员）",
			"**`/timer add <自然语言任务>`** 创建任务，确认后生效",
			"例如：`/timer add 30分钟后提醒我提交周报`",
			"**`/timer info <id>`** 查看详情",
			"**`/timer del <id>`** 删除任务",
			"timer 到期只执行一次，不支持 `run`、`enable` 或 `disable`。",
		}, "\n")
	}
	return strings.Join([]string{
		"**`/cron` / `/cron list`** 查看当前会话的周期任务",
		"**`/cron list all`** 查看全部周期任务（仅管理员）",
		"**`/cron add <自然语言任务>`** 创建任务，确认后生效",
		"例如：`/cron add 每个工作日上午9点总结项目进展`",
		"**`/cron info <id>`** 查看详情",
		"**`/cron run <id>`** 立即执行一次",
		"**`/cron enable|disable <id>`** 启用或停用",
		"**`/cron del <id>`** 删除任务",
	}, "\n")
}

func (s *Service) confirmScheduleDraft(id, actor string, now time.Time) (schedule.Task, error) {
	task, err := s.Schedules.ConfirmDraft(id, actor, now)
	if err != nil {
		return schedule.Task{}, err
	}
	if s.Scheduler != nil {
		s.Scheduler.Add(task)
	}
	s.Audit.Record(actor, "schedule_task_confirmed", task.ID, string(task.Kind))
	return task, nil
}

func (s *Service) canManageSchedule(msg Message, task schedule.Task) bool {
	if s.canRunAdminCommand(msg.Sender) {
		return true
	}
	return task.Creator == msg.Sender && task.Target.ChatID == msg.ChatID && task.Target.ThreadID == msg.ThreadID
}

func (s *Service) scheduleListText(kind schedule.Kind, msg Message, all bool) string {
	var tasks []schedule.Task
	for _, task := range s.Schedules.Tasks() {
		if task.Kind != kind {
			continue
		}
		if !all && (task.Target.ChatID != msg.ChatID || task.Target.ThreadID != msg.ThreadID) {
			continue
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].NextRun.Before(tasks[j].NextRun) })
	if len(tasks) == 0 {
		return "当前范围没有定时任务。"
	}
	var lines []string
	for _, task := range tasks {
		state := "enabled"
		if !task.Enabled {
			state = "disabled"
		}
		lines = append(lines, fmt.Sprintf("- `%s` [%s] %s", task.ID, state, schedule.Describe(task, time.Now())))
	}
	return strings.Join(lines, "\n")
}

func scheduleTaskText(task schedule.Task) string {
	return fmt.Sprintf("任务 `%s`\n\n规则：%s\nPrompt：%s\nAgent：%s / %s\nWorkdir：%s", task.ID, schedule.Describe(task, time.Now()), task.Prompt, task.Execution.Agent, task.Execution.Model, task.Execution.WorkDir)
}

func confirmedScheduleText(task schedule.Task) string {
	command := "/cron"
	if task.Kind == schedule.KindTimer {
		command = "/timer"
	}
	return fmt.Sprintf("✅ 已创建任务 `%s`：%s\n\n使用 `%s info %s` 查看详情。", task.ID, schedule.Describe(task, time.Now()), command, task.ID)
}

func scheduleAddPrompt(kind schedule.Kind, request string) string {
	return fmt.Sprintf("[Bridge scheduling request: propose exactly one %s task using the schedule proposal tool; do not claim it is created before user confirmation.]\n%s", kind, request)
}

func scheduleConfirmationEvent(sessionID string, draft schedule.Draft) card.Event {
	next := make([]string, 0, len(draft.Next))
	for _, at := range draft.Next {
		next = append(next, at.Format("2006-01-02 15:04 MST"))
	}
	text := fmt.Sprintf("请确认定时任务：\n\n**规则**：%s（%s）\n\n**接下来执行**：%s\n\n**任务**：%s\n\n**执行环境**：%s / %s / `%s`\n\n服务离线不超过 5 分钟时补跑最近一次；同一任务不会重叠执行。", draft.Description, draft.Timezone, strings.Join(next, "、"), draft.Prompt, draft.Execution.Agent, draft.Execution.Model, draft.Execution.WorkDir)
	return card.Event{
		Type: "schedule_confirmation", SessionID: sessionID, HeaderTitle: "⏰ 确认定时任务", HeaderTemplate: "orange",
		Segments: []card.Segment{{Kind: card.SegmentText, Text: text}}, HideAgentPanels: true,
		Actions: []card.Action{{ID: "schedule.confirm", Label: "确认创建", Value: draft.ID}, {ID: "schedule.cancel", Label: "取消", Value: draft.ID}},
	}
}

func (s *Service) issueScheduleProposalContext(sess session.Session, batch session.Batch, originRunID string) (string, string) {
	if s.Schedules == nil || s.ScheduleContexts == nil || s.ScheduleSocket == "" || len(batch.Inputs) == 0 {
		return "", ""
	}
	anchor := batch.Inputs[len(batch.Inputs)-1]
	targetThreadID := sess.Key.Thread
	if anchor.ScheduleKind != "" {
		targetThreadID = anchor.ScheduleTargetThreadID
	}
	bound := schedule.ProposalContext{
		OriginRunID: originRunID,
		Creator:     anchor.Sender,
		Target: schedule.Target{
			ChatID: sess.Key.ChatID, ThreadID: targetThreadID, ReplyToMessageID: anchor.ReplyToMessageID, IsGroup: anchor.IsGroup,
		},
		Execution: schedule.FrozenExecution{
			Agent: string(sess.Key.Agent), Model: anchor.RequestedModel, Effort: anchor.RequestedEffort,
			AgentHome: anchor.AgentHome, AgentBin: anchor.AgentBin, WorkDir: sess.WorkDir,
			ReplyMode: string(anchor.EffectiveReplyMode()), ConversationMode: string(anchor.ConversationMode),
		},
		ForcedKind: schedule.Kind(anchor.ScheduleKind),
	}
	token, err := s.ScheduleContexts.Issue(bound, time.Now())
	if err != nil {
		s.Audit.Record("system", "schedule_context_issue_failed", originRunID, err.Error())
		return "", ""
	}
	return s.ScheduleSocket, token
}

func (s *Service) scheduleDraftForOrigin(originRunID string) (schedule.Draft, bool) {
	if s.Schedules == nil {
		return schedule.Draft{}, false
	}
	return s.Schedules.DraftByOrigin(originRunID)
}

func (s *Service) finishScheduleConfirmation(stream *agentCardStream, status string, meta card.Meta, result AgentRunResult, draft schedule.Draft, sessionID string) {
	_, err := stream.FinishTransformed(status, meta, result, func(event card.Event) card.Event {
		confirmation := scheduleConfirmationEvent(event.SessionID, draft)
		for i := range confirmation.Actions {
			grantID, grantErr := s.issueActionGrantWithAdmin(draft.Creator, draft.Target.ChatID, confirmation.SessionID, confirmation.Actions[i].ID, confirmation.Actions[i].Value, draft.ExpiresAt, false)
			if grantErr != nil {
				s.Audit.Record("system", "action_grant_issue_failed", confirmation.SessionID, "action="+confirmation.Actions[i].ID+" error="+grantErr.Error())
				confirmation.Actions = nil
				break
			}
			confirmation.Actions[i].GrantID = grantID
		}
		confirmation.ReplyToMessageID = event.ReplyToMessageID
		confirmation.ReplyInThread = event.ReplyInThread
		confirmation.Meta = event.Meta
		return confirmation
	})
	if err != nil {
		s.Audit.Record("system", "terminal_reply_render_failed", sessionID, fmt.Sprintf("status=schedule_confirmation error=%v", err))
	}
}

func scheduleRunIDFromBatch(batch session.Batch) string {
	if len(batch.Inputs) != 1 {
		return ""
	}
	return batch.Inputs[0].ScheduleRunID
}

func (s *Service) markScheduleBatchRunning(batch session.Batch, at time.Time) {
	runID := scheduleRunIDFromBatch(batch)
	if runID == "" || s.Scheduler == nil {
		return
	}
	if err := s.Scheduler.MarkRunning(runID, at); err != nil {
		s.Audit.Record("system", "schedule_run_state_failed", runID, "running: "+err.Error())
	}
}

func (s *Service) completeScheduleBatch(batch session.Batch, status session.InputState, runErr error, at time.Time) {
	runID := scheduleRunIDFromBatch(batch)
	if runID == "" || s.Scheduler == nil {
		return
	}
	if status != session.InputCompleted && runErr == nil {
		runErr = fmt.Errorf("scheduled run ended with status %s", status)
	}
	if err := s.Scheduler.Complete(runID, runErr, false, at); err != nil {
		s.Audit.Record("system", "schedule_run_state_failed", runID, "terminal: "+err.Error())
	}
}
