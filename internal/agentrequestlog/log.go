package agentrequestlog

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

const SchemaVersion = 1

// Entry captures the exact serializable request boundary immediately before
// bridge invokes the AI agent. Execution credentials and callbacks are
// intentionally excluded.
type Entry struct {
	SchemaVersion             int       `json:"schema_version"`
	Time                      time.Time `json:"time"`
	RunID                     string    `json:"run_id"`
	SessionID                 string    `json:"session_id"`
	BatchID                   string    `json:"batch_id"`
	Agent                     string    `json:"agent"`
	Bin                       string    `json:"bin"`
	Home                      string    `json:"home,omitempty"`
	WorkDir                   string    `json:"work_dir"`
	Prompt                    string    `json:"prompt"`
	Images                    []string  `json:"images,omitempty"`
	AgentSessionID            string    `json:"agent_session_id,omitempty"`
	ForkFromAgentSessionID    string    `json:"fork_from_agent_session_id,omitempty"`
	Model                     string    `json:"model,omitempty"`
	Effort                    string    `json:"effort,omitempty"`
	BridgeInstructionsVersion string    `json:"bridge_instructions_version"`
	ContextUsageDir           string    `json:"context_usage_dir,omitempty"`
	ScheduleProposalEnabled   bool      `json:"schedule_proposal_enabled"`
}

type Recorder struct {
	mu     sync.Mutex
	writer io.Writer
	now    func() time.Time
}

func NewRecorder(writer io.Writer) *Recorder {
	return &Recorder{writer: writer, now: time.Now}
}

func (r *Recorder) Record(entry Entry) error {
	if r == nil || r.writer == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.SchemaVersion = SchemaVersion
	if entry.Time.IsZero() {
		entry.Time = r.now()
	}
	return json.NewEncoder(r.writer).Encode(entry)
}
