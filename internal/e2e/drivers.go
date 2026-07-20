package e2e

import (
	"context"
	"encoding/json"
)

type Messenger interface {
	SendText(context.Context, string) (string, *Failure)
}

type Reply struct {
	MessageID string
	Raw       []byte
}

type Replies interface {
	WaitReply(context.Context, int64, string) (Reply, *Failure)
	ContainsVisibleText(Reply, string) (bool, string, *Failure)
}

type AuditMatch struct {
	Action string
	Source string
	Detail string
}

type AuditEvent struct {
	Action    string          `json:"action"`
	SessionID string          `json:"session_id,omitempty"`
	Detail    string          `json:"detail,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

type AuditObserver interface {
	Mark(context.Context) (int64, *Failure)
	Wait(context.Context, int64, AuditMatch) (AuditEvent, *Failure)
}

type StopFixture interface {
	Arm(context.Context, string) *Failure
	Disarm(context.Context) *Failure
}

type AgentFixture interface {
	ArmAgent(context.Context, AgentFixturePlan) *Failure
	WaitAgentInvocation(context.Context, string, string) (AgentInvocation, *Failure)
	HasAgentInvocation(context.Context, string) (bool, *Failure)
	DisarmAgent(context.Context) *Failure
}

type CandidateLifecycle interface {
	RestartCandidate(context.Context) (int, *Failure)
}

type Drivers struct {
	Messenger    Messenger
	Replies      Replies
	Audit        AuditObserver
	Fixture      StopFixture
	AgentFixture AgentFixture
	Lifecycle    CandidateLifecycle
}
