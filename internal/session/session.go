package session

import (
	"strings"
	"sync"
	"time"

	"lark-agent-bridge/internal/agent"
)

type Key struct {
	Agent  agent.Kind
	ChatID string
	Thread string
}

func (k Key) ID() string {
	parts := []string{string(k.Agent), k.ChatID}
	if k.Thread != "" {
		parts = append(parts, "thread", k.Thread)
	}
	return strings.Join(parts, ":")
}

type Prompt struct {
	Time   time.Time
	Sender string
	Text   string
}

type Input struct {
	Sender           string
	Text             string
	ReplyToMessageID string
	CardSessionID    string
	WorkDir          string
	Time             time.Time
	Reset            bool
}

type State string

const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StateCrashed State = "crashed"
	StateStopped State = "stopped"
)

type Session struct {
	Key             Key
	ID              string
	WorkDir         string
	ClaudeSessionID string
	Model           string
	Tokens          int
	State           State
	History         []Prompt
	Queue           []Input
	CreatedAt       time.Time
	LastActive      time.Time
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*Session{}}
}

func (m *Manager) GetOrCreate(key Key, workDir string) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *cloneSession(m.ensureLocked(key, workDir))
}

func (m *Manager) Reset(key Key, workDir string) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	resetSessionLocked(s, workDir, time.Now())
	return *cloneSession(s)
}

func (m *Manager) Enqueue(key Key, input Input, workDir string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	if input.Time.IsZero() {
		input.Time = time.Now()
	}
	if input.WorkDir == "" {
		input.WorkDir = workDir
	}
	if s.State == StateRunning {
		s.Queue = append(s.Queue, input)
		s.LastActive = input.Time
		return *cloneSession(s), true
	}
	if input.Reset {
		resetSessionLocked(s, workDir, input.Time)
	} else if workDir != "" && s.WorkDir == "" {
		s.WorkDir = workDir
	}
	s.State = StateRunning
	s.LastActive = input.Time
	appendPromptLocked(s, input)
	return *cloneSession(s), false
}

func (m *Manager) Complete(key Key, workDir string) (Session, *Input) {
	return m.CompleteAt(key, workDir, time.Now())
}

func (m *Manager) CompleteAt(key Key, workDir string, now time.Time) (Session, *Input) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	if len(s.Queue) == 0 {
		s.State = StateIdle
		if !now.IsZero() {
			s.LastActive = now
		}
		return *cloneSession(s), nil
	}
	next := s.Queue[0]
	s.Queue = s.Queue[1:]
	nextWorkDir := next.WorkDir
	if nextWorkDir == "" {
		nextWorkDir = workDir
	}
	if next.Reset {
		resetSessionLocked(s, nextWorkDir, next.Time)
	} else if nextWorkDir != "" && s.WorkDir == "" {
		s.WorkDir = nextWorkDir
	}
	s.State = StateRunning
	if !next.Time.IsZero() {
		s.LastActive = next.Time
	}
	appendPromptLocked(s, next)
	return *cloneSession(s), &next
}

func (m *Manager) MarkCrashed(key Key, workDir string) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	s.State = StateCrashed
	return *cloneSession(s)
}

func (m *Manager) Stop(key Key, workDir string) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	s.State = StateStopped
	s.Queue = nil
	return *cloneSession(s)
}

func (m *Manager) UpdateRunResult(id, claudeSessionID, model string, tokens int) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}
	}
	if claudeSessionID != "" {
		s.ClaudeSessionID = claudeSessionID
	}
	if model != "" {
		s.Model = model
	}
	if tokens > 0 {
		s.Tokens += tokens
	}
	return *cloneSession(s)
}

func (m *Manager) List() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, *cloneSession(s))
	}
	return out
}

func (m *Manager) History(id string) []Prompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil
	}
	cp := make([]Prompt, len(s.History))
	copy(cp, s.History)
	return cp
}

func (m *Manager) ensureLocked(key Key, workDir string) *Session {
	id := key.ID()
	if s, ok := m.sessions[id]; ok {
		if s.WorkDir == "" && workDir != "" {
			s.WorkDir = workDir
		}
		return s
	}
	now := time.Now()
	s := &Session{Key: key, ID: id, WorkDir: workDir, State: StateIdle, CreatedAt: now, LastActive: now}
	m.sessions[id] = s
	return s
}

func resetSessionLocked(s *Session, workDir string, now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	if workDir != "" {
		s.WorkDir = workDir
	}
	s.ClaudeSessionID = ""
	s.Model = ""
	s.Tokens = 0
	s.History = nil
	s.Queue = nil
	s.State = StateIdle
	s.CreatedAt = now
	s.LastActive = now
}

func appendPromptLocked(s *Session, input Input) {
	if strings.TrimSpace(input.Text) == "" {
		return
	}
	s.History = append(s.History, Prompt{Time: input.Time, Sender: input.Sender, Text: input.Text})
}

func cloneSession(s *Session) *Session {
	cp := *s
	cp.History = append([]Prompt(nil), s.History...)
	cp.Queue = append([]Input(nil), s.Queue...)
	return &cp
}
