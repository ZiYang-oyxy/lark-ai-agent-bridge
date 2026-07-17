package session

import (
	"fmt"
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
	Sender string
	Text   string
	Time   time.Time
}

type State string

const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StateCrashed State = "crashed"
	StateStopped State = "stopped"
)

type Session struct {
	Key           Key
	ID            string
	WindowName    string
	WindowStarted bool
	WorkDir       string
	ApprovalMode  agent.ApprovalMode
	Model         string
	Tokens        int
	State         State
	History       []Prompt
	Queue         []Input
	CreatedAt     time.Time
	LastActive    time.Time
	IdleNotified  bool
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*Session{}}
}

func (m *Manager) GetOrCreate(key Key, workDir string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := key.ID()
	if s, ok := m.sessions[id]; ok {
		return cloneSession(s)
	}
	now := time.Now()
	s := &Session{
		Key:        key,
		ID:         id,
		WindowName: windowName(id),
		WorkDir:    workDir,
		State:      StateIdle,
		CreatedAt:  now,
		LastActive: now,
	}
	m.sessions[id] = s
	return cloneSession(s)
}

func (m *Manager) Enqueue(key Key, input Input, workDir string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	if shouldAdoptWorkDir(s, workDir) {
		s.WorkDir = workDir
	}
	if input.Time.IsZero() {
		input.Time = time.Now()
	}
	s.LastActive = input.Time
	s.IdleNotified = false
	s.History = append(s.History, Prompt{Time: input.Time, Sender: input.Sender, Text: input.Text})
	if s.State == StateRunning {
		s.Queue = append(s.Queue, input)
		return *cloneSession(s), true
	}
	s.State = StateRunning
	return *cloneSession(s), false
}

func shouldAdoptWorkDir(s *Session, workDir string) bool {
	if workDir == "" || workDir == s.WorkDir {
		return false
	}
	if s.State == StateRunning {
		return false
	}
	return !s.WindowStarted || s.State == StateCrashed || s.State == StateStopped
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
		s.IdleNotified = false
		return *cloneSession(s), nil
	}
	next := s.Queue[0]
	s.Queue = s.Queue[1:]
	s.State = StateRunning
	if !next.Time.IsZero() {
		s.LastActive = next.Time
	}
	s.IdleNotified = false
	return *cloneSession(s), &next
}

func (m *Manager) MarkWindowStarted(key Key, workDir string, approvalMode agent.ApprovalMode) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	s.WindowStarted = true
	if approvalMode != "" {
		s.ApprovalMode = approvalMode
	}
	return *cloneSession(s)
}

func (m *Manager) MarkCrashed(key Key, workDir string) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	s.State = StateCrashed
	s.WindowStarted = false
	return *cloneSession(s)
}

func (m *Manager) MarkRestarted(key Key, workDir string, approvalMode agent.ApprovalMode) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	s.State = StateIdle
	s.WindowStarted = true
	if approvalMode != "" {
		s.ApprovalMode = approvalMode
	}
	s.LastActive = time.Now()
	return *cloneSession(s)
}

func (m *Manager) Stop(key Key, workDir string) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensureLocked(key, workDir)
	s.State = StateStopped
	s.WindowStarted = false
	s.Queue = nil
	return *cloneSession(s)
}

func (m *Manager) UpdateMeta(id, model string, tokens int) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}
	}
	if model != "" {
		s.Model = model
	}
	if tokens > 0 {
		s.Tokens = tokens
	}
	return *cloneSession(s)
}

func (m *Manager) Touch(id string, now time.Time) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.LastActive = now
	s.IdleNotified = false
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

func (m *Manager) IdleReminderDue(now time.Time, after time.Duration) []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []Session
	for _, s := range m.sessions {
		if s.IdleNotified || s.State != StateIdle {
			continue
		}
		if after > 0 && now.Sub(s.LastActive) >= after {
			s.IdleNotified = true
			due = append(due, *cloneSession(s))
		}
	}
	return due
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
		return s
	}
	now := time.Now()
	s := &Session{Key: key, ID: id, WindowName: windowName(id), WorkDir: workDir, State: StateIdle, CreatedAt: now, LastActive: now}
	m.sessions[id] = s
	return s
}

func cloneSession(s *Session) *Session {
	cp := *s
	cp.History = append([]Prompt(nil), s.History...)
	cp.Queue = append([]Input(nil), s.Queue...)
	return &cp
}

func windowName(id string) string {
	replacer := strings.NewReplacer(":", "-", "/", "-", " ", "-", ".", "-")
	name := replacer.Replace(id)
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		return "agent"
	}
	return fmt.Sprintf("agent-%s", name)
}
