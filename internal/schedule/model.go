package schedule

import (
	"fmt"
	"time"
)

type Kind string

const (
	KindCron  Kind = "cron"
	KindTimer Kind = "timer"
)

type RunState string

const (
	RunPending         RunState = "pending"
	RunQueued          RunState = "queued"
	RunRunning         RunState = "running"
	RunDelivered       RunState = "delivered"
	RunFailed          RunState = "failed"
	RunMissed          RunState = "missed"
	RunSkippedOverlap  RunState = "skipped_overlap"
	RunDeliveryUnknown RunState = "delivery_unknown"
)

type Target struct {
	ChatID           string `json:"chat_id"`
	ThreadID         string `json:"thread_id,omitempty"`
	ReplyToMessageID string `json:"reply_to_message_id,omitempty"`
	IsGroup          bool   `json:"is_group,omitempty"`
}

type FrozenExecution struct {
	Agent            string `json:"agent"`
	Model            string `json:"model"`
	Effort           string `json:"effort"`
	AgentHome        string `json:"agent_home,omitempty"`
	AgentBin         string `json:"agent_bin,omitempty"`
	WorkDir          string `json:"work_dir"`
	ReplyMode        string `json:"reply_mode"`
	ConversationMode string `json:"conversation_mode"`
}

type Proposal struct {
	Kind        Kind      `json:"kind"`
	CronExpr    string    `json:"cron_expr,omitempty"`
	ScheduledAt time.Time `json:"scheduled_at,omitempty"`
	Timezone    string    `json:"timezone"`
	Description string    `json:"description,omitempty"`
	Prompt      string    `json:"prompt"`
}

type NormalizedRule struct {
	Proposal
	Description string
	Next        []time.Time
}

type Draft struct {
	ID          string          `json:"id"`
	OriginRunID string          `json:"origin_run_id,omitempty"`
	Kind        Kind            `json:"kind"`
	CronExpr    string          `json:"cron_expr,omitempty"`
	ScheduledAt time.Time       `json:"scheduled_at,omitempty"`
	Timezone    string          `json:"timezone"`
	Description string          `json:"description"`
	Prompt      string          `json:"prompt"`
	Creator     string          `json:"creator"`
	Target      Target          `json:"target"`
	Execution   FrozenExecution `json:"execution"`
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
	Next        []time.Time     `json:"next"`
}

type Task struct {
	ID          string          `json:"id"`
	Kind        Kind            `json:"kind"`
	CronExpr    string          `json:"cron_expr,omitempty"`
	ScheduledAt time.Time       `json:"scheduled_at,omitempty"`
	Timezone    string          `json:"timezone"`
	Description string          `json:"description"`
	Prompt      string          `json:"prompt"`
	Creator     string          `json:"creator"`
	Target      Target          `json:"target"`
	Execution   FrozenExecution `json:"execution"`
	Enabled     bool            `json:"enabled"`
	Archived    bool            `json:"archived,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	ConfirmedAt time.Time       `json:"confirmed_at"`
	NextRun     time.Time       `json:"next_run,omitempty"`
	LastRunID   string          `json:"last_run_id,omitempty"`
}

type Run struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	ScheduledAt time.Time `json:"scheduled_at"`
	ClaimedAt   time.Time `json:"claimed_at,omitempty"`
	QueuedAt    time.Time `json:"queued_at,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	State       RunState  `json:"state"`
	Manual      bool      `json:"manual,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

func RunID(kind Kind, taskID string, scheduledAt time.Time) string {
	return fmt.Sprintf("%s:%s:%s", kind, taskID, scheduledAt.UTC().Format(time.RFC3339))
}

func terminalRunState(state RunState) bool {
	switch state {
	case RunDelivered, RunFailed, RunMissed, RunSkippedOverlap, RunDeliveryUnknown:
		return true
	default:
		return false
	}
}
