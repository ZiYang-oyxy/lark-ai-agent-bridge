package e2e

import "time"

type FailureClass string

const (
	FailureProduct     FailureClass = "product"
	FailureAssertion   FailureClass = "assertion"
	FailureHarness     FailureClass = "harness"
	FailurePlatform    FailureClass = "platform"
	FailureEnvironment FailureClass = "environment"
	FailureTimeout     FailureClass = "timeout"
	FailureCleanup     FailureClass = "cleanup"
)

type Deployment struct {
	SchemaVersion int    `json:"schema_version"`
	Transaction   string `json:"transaction"`
	CandidatePID  int    `json:"candidate_pid"`
	SourceCommit  string `json:"source_commit"`
	BinarySHA256  string `json:"binary_sha256"`
	Workspace     string `json:"workspace"`
	StateDir      string `json:"state_dir"`
	FixtureDir    string `json:"fixture_dir"`
}

type Config struct {
	SchemaVersion   int    `json:"schema_version"`
	Profile         string `json:"profile"`
	ExpectedBotName string `json:"expected_bot_name"`
	AppID           string `json:"app_id"`
	ChatID          string `json:"chat_id"`
	RemoteHost      string `json:"remote_host"`
	AuditPath       string `json:"audit_path"`
	PollIntervalMS  int    `json:"poll_interval_ms"`
	StepTimeoutMS   int    `json:"step_timeout_ms"`
}

type Failure struct {
	Class        FailureClass `json:"class"`
	Scenario     string       `json:"scenario"`
	Step         string       `json:"step"`
	Assertion    string       `json:"assertion,omitempty"`
	Expected     string       `json:"expected,omitempty"`
	Actual       string       `json:"actual,omitempty"`
	LastObserved string       `json:"last_observed,omitempty"`
	Message      string       `json:"message"`
}

type ActionRecord struct {
	Time         time.Time `json:"time"`
	Scenario     string    `json:"scenario"`
	Step         string    `json:"step"`
	State        string    `json:"state"`
	Detail       string    `json:"detail,omitempty"`
	LastObserved string    `json:"last_observed,omitempty"`
}

type Result struct {
	SchemaVersion   int       `json:"schema_version"`
	Scenario        string    `json:"scenario"`
	Status          string    `json:"status"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
	Failure         *Failure  `json:"failure,omitempty"`
	CleanupFailures []Failure `json:"cleanup_failures,omitempty"`
}

func (r Result) SummaryFailure() *Failure {
	if r.Failure != nil {
		return r.Failure
	}
	if len(r.CleanupFailures) > 0 {
		return &r.CleanupFailures[0]
	}
	return nil
}
