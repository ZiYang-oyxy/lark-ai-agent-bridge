package session

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/media"
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

type InputState string

const (
	InputDebouncing  InputState = "debouncing"
	InputQueued      InputState = "queued"
	InputStarting    InputState = "starting"
	InputRunning     InputState = "running"
	InputCompleted   InputState = "completed"
	InputFailed      InputState = "failed"
	InputCancelled   InputState = "cancelled"
	InputInterrupted InputState = "interrupted"
)

type Input struct {
	ID     string
	Sender string
	Text   string
	// QuotedText carries the plain-text body of a message the user quoted
	// (replied to) when triggering this input. Feishu delivers only the
	// quoted message's parent_id in the inbound event, so bridge fetches its
	// body separately and stores it here to be rendered into the prompt.
	// Empty when the message quotes nothing or the fetch failed.
	QuotedText string `json:",omitempty"`
	// QuotedSender is the open_id of the quoted message's author, used only to
	// label the quoted block in the prompt. Empty when unknown.
	QuotedSender string `json:",omitempty"`
	// QuotedSenderType 是引用消息发送者的归一化主体类型，供 prompt 侧决定要不要给
	// 引用块加主体身份边框：`user` 普通用户；`app` 别的 bot（另一分身/别的 App）；
	// `self_bot` 引用的是本 bot 自己发过的历史；`anonymous`；空表示未知。
	QuotedSenderType string `json:",omitempty"`
	Attachments      []media.Attachment
	ReplyToMessageID string
	CardSessionID    string
	WorkDir          string
	RequestedModel   string
	RequestedEffort  string
	AgentBin         string
	AgentHome        string
	// ForkFromAgentSessionID marks this input as the seed run of a session
	// whose Claude/Codex history should be forked from another session's agent
	// session id. Only consulted at execute time when the enclosing session
	// has no AgentSessionID of its own yet — after the first successful run
	// the session owns a real (forked) AgentSessionID and this hint is
	// ignored. Empty means "no fork, start fresh".
	ForkFromAgentSessionID    string `json:",omitempty"`
	ReplyMode                 config.ReplyMode
	AppendOverflowMode        config.AppendOverflowMode `json:",omitempty"`
	ConversationMode          config.ConversationMode
	BridgeInstructionsVersion string `json:",omitempty"`
	ScheduleRunID             string `json:",omitempty"`
	ScheduleTaskID            string `json:",omitempty"`
	ScheduleKind              string `json:",omitempty"`
	ScheduleTargetThreadID    string `json:",omitempty"`
	IsGroup                   bool   `json:",omitempty"`
	NotifyOnComplete          bool   `json:",omitempty"`
	CompletionStatusText      string `json:",omitempty"`
	// Compact marks the native /compact control input. It stays in the durable
	// FIFO but must never be merged with a user prompt or another compact run.
	Compact        bool `json:",omitempty"`
	Time           time.Time
	DebounceUntil  time.Time
	DebounceWindow time.Duration `json:",omitempty"`
	State          InputState
	Reset          bool
}

// EffectiveReplyMode keeps durable inputs written before ReplyMode was added
// compatible with the original default behavior.
func (in Input) EffectiveReplyMode() config.ReplyMode {
	if in.ReplyMode == "" {
		return config.ReplyModeAppend
	}
	return in.ReplyMode
}

// EffectiveAppendOverflowMode keeps durable inputs written before the option
// was introduced on the single-card tail-window behavior.
func (in Input) EffectiveAppendOverflowMode() config.AppendOverflowMode {
	if in.AppendOverflowMode == "" {
		return config.AppendOverflowModeTruncate
	}
	return in.AppendOverflowMode
}

// EffectiveCompletionStatusText keeps durable inputs written before the
// configurable terminal label compatible with the original card title.
func (in Input) EffectiveCompletionStatusText() string {
	return config.EffectiveCompletionStatusText(in.CompletionStatusText)
}

type Batch struct {
	ID        string
	Inputs    []Input
	State     InputState
	CreatedAt time.Time
	StartedAt time.Time
	RenderRef *RenderRef
}

// BatchCompletion records the durable terminal result for one active batch.
type BatchCompletion struct {
	Status         InputState
	AgentSessionID string
	Model          string
	Tokens         int
	At             time.Time
}

type RenderRef struct {
	CardID          string
	ReplyMessageID  string
	Version         int
	CreatedAt       time.Time
	SequenceUnknown bool `json:"sequence_unknown,omitempty"`
	PendingSequence int  `json:"pending_sequence,omitempty"`
}

type RenderRefSequenceResolver interface {
	RenderRefSequenceUnknown(RenderRef) bool
}

type BatchLimits struct {
	MaxInputs          int
	MaxTextRunes       int
	MaxAttachments     int
	MaxAttachmentBytes int64
	MaxPending         int
}

type EnqueueResult struct {
	Position int
	Queued   bool
}

type State string

const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StateCrashed State = "crashed"
	StateStopped State = "stopped"
)

type Session struct {
	Key                       Key
	ID                        string
	WorkDir                   string
	AgentSessionID            string
	Model                     string
	Tokens                    int
	State                     State
	History                   []Prompt
	Queue                     []Input
	ActiveBatch               *Batch
	BridgeInstructionsVersion string `json:",omitempty"`
	CreatedAt                 time.Time
	LastActive                time.Time
}

type Manager struct {
	mu                               sync.Mutex
	sessions                         map[string]*Session
	storePath                        string
	revision                         uint64
	batchSeq                         uint64
	lastPersistErr                   error
	receipts                         []Receipt
	currentBridgeInstructionsVersion string
	catalog                          *Catalog
}

var (
	ErrSessionBusy        = errors.New("session: scope has active or queued work")
	ErrSessionNotFound    = errors.New("session: resumable session not found")
	ErrCatalogUnavailable = errors.New("session: catalog unavailable")
)

func NewManager() *Manager {
	return NewManagerWithStoreVersion("", "")
}

// NewManagerWithStore creates a manager whose durable queue and batch
// transitions are atomically persisted to path before becoming observable.
func NewManagerWithStore(path string) *Manager {
	return NewManagerWithStoreVersion(path, "")
}

// NewManagerWithStoreVersion creates a durable manager that uses
// currentVersion only to backfill inputs and sessions that predate versioning.
func NewManagerWithStoreVersion(path, currentVersion string) *Manager {
	return &Manager{
		sessions:                         map[string]*Session{},
		storePath:                        path,
		currentBridgeInstructionsVersion: strings.TrimSpace(currentVersion),
	}
}

func (m *Manager) AttachCatalog(catalog *Catalog) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.catalog = catalog
}

func (m *Manager) RecentSessions(identity CatalogIdentity, limit int) ([]CatalogEntry, error) {
	m.mu.Lock()
	catalog := m.catalog
	m.mu.Unlock()
	if catalog == nil {
		return nil, ErrCatalogUnavailable
	}
	return catalog.Recent(identity, limit), nil
}

func (m *Manager) RecordSession(entry CatalogEntry) error {
	m.mu.Lock()
	catalog := m.catalog
	m.mu.Unlock()
	if catalog == nil {
		return ErrCatalogUnavailable
	}
	return catalog.Upsert(entry)
}

// Resume switches an idle conversation scope to an existing catalog entry.
// Catalog ordering is touched before the authoritative session snapshot; a
// later snapshot failure may advance list ordering but never publishes a new
// in-memory binding or loses queued work.
func (m *Manager) Resume(key Key, identity CatalogIdentity, sessionID string, now time.Time) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.currentSessionLocked(key)
	if m.catalog == nil {
		return current, ErrCatalogUnavailable
	}
	if key.Agent != identity.Agent {
		return current, ErrSessionNotFound
	}
	if existing, ok := m.sessions[key.ID()]; ok && (existing.State == StateRunning || existing.ActiveBatch != nil || len(existing.Queue) != 0) {
		return current, ErrSessionBusy
	}
	entry, ok := m.catalog.Resolve(identity, strings.TrimSpace(sessionID))
	if !ok {
		return current, ErrSessionNotFound
	}
	if now.IsZero() {
		now = time.Now()
	}
	if _, err := m.catalog.Touch(identity, entry.SessionID, now); err != nil {
		return current, fmt.Errorf("session: touch catalog: %w", err)
	}

	sessions := cloneSessions(m.sessions)
	s := ensureSessionIn(sessions, key, identity.WorkDir)
	s.AgentSessionID = entry.SessionID
	s.WorkDir = entry.WorkDir
	s.BridgeInstructionsVersion = entry.BridgeInstructionsVersion
	s.Model = ""
	s.Tokens = 0
	s.History = nil
	s.Queue = nil
	s.ActiveBatch = nil
	s.State = StateIdle
	s.LastActive = now
	if m.storePath != "" {
		if err := m.persistCandidateLocked(sessions, m.receipts, m.revision); err != nil {
			return m.currentSessionLocked(key), err
		}
	} else {
		m.sessions = sessions
	}
	return *cloneSession(s), nil
}

// RecoveryNotice is the terminal handoff for a pending input that was cleared
// during restart recovery. Queue inputs do not have a render reference; active
// batch inputs receive their own copy of the batch render reference.
type RecoveryNotice struct {
	SessionID        string
	ReplyToMessageID string
	CardSessionID    string
	Status           InputState
	RenderRef        *RenderRef
}

// Restore loads the last snapshot, preserves session context, and terminates
// every runnable pending input without replaying it. The cleaned snapshot is
// saved before it replaces any in-memory manager state.
func (m *Manager) Restore() ([]RecoveryNotice, error) {
	if m.storePath == "" {
		return nil, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot, err := LoadSnapshot(m.storePath)
	if err != nil {
		m.lastPersistErr = err
		return nil, err
	}

	candidate, notices := restoredSessions(snapshot.Sessions)
	receipts := cloneReceipts(snapshot.Receipts)

	if err := m.persistCandidateLocked(candidate, receipts, snapshot.Revision); err != nil {
		return nil, err
	}
	return notices, nil
}

func (m *Manager) GetOrCreate(key Key, workDir string) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *cloneSession(m.ensureLocked(key, workDir))
}

func (m *Manager) Get(key Key) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key.ID()]
	if !ok {
		return Session{}, false
	}
	return *cloneSession(s), true
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

// EnqueueDurable appends an input to the durable-model queue without starting
// an agent or changing session history. A store-aware manager first persists a
// fully cloned candidate, so a persistence error leaves the live queue intact.
func (m *Manager) EnqueueDurable(key Key, input Input, workDir string, limits BatchLimits) (Session, EnqueueResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessions := m.sessions
	if m.storePath != "" {
		sessions = cloneSessions(m.sessions)
	}
	s := ensureSessionIn(sessions, key, workDir)
	pending := len(s.Queue)
	if s.ActiveBatch != nil {
		pending += len(s.ActiveBatch.Inputs)
	}
	if limits.MaxPending > 0 && pending >= limits.MaxPending {
		return *cloneSession(s), EnqueueResult{}, fmt.Errorf("session: pending input limit %d reached", limits.MaxPending)
	}
	if input.Time.IsZero() {
		input.Time = time.Now()
	}
	if input.WorkDir == "" {
		input.WorkDir = workDir
	}
	if input.State == "" {
		input.State = InputQueued
	}
	s.Queue = append(s.Queue, input)
	if m.storePath != "" {
		if err := m.persistCandidateLocked(sessions, m.receipts, m.revision); err != nil {
			return m.currentSessionLocked(key), EnqueueResult{}, err
		}
	}
	return *cloneSession(s), EnqueueResult{Position: len(s.Queue), Queued: true}, nil
}

// FreezeReadyBatch turns ready inputs at a queue head into an immutable batch.
// A scope with an active batch remains serial: no later input is inspected or
// promoted until that batch has been released by the execution lifecycle.
func (m *Manager) FreezeReadyBatch(key Key, now time.Time, limits BatchLimits) (Session, *Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessions := m.sessions
	if m.storePath != "" {
		sessions = cloneSessions(m.sessions)
	}
	s, ok := sessions[key.ID()]
	if !ok {
		return Session{}, nil, nil
	}
	if s.ActiveBatch != nil {
		return *cloneSession(s), nil, nil
	}
	changed := false
	for i := range s.Queue {
		if s.Queue[i].State == InputDebouncing && !s.Queue[i].DebounceUntil.After(now) {
			s.Queue[i].State = InputQueued
			changed = true
		}
	}
	if len(s.Queue) == 0 || s.Queue[0].State != InputQueued {
		if changed && m.storePath != "" {
			if err := m.persistCandidateLocked(sessions, m.receipts, m.revision); err != nil {
				return m.currentSessionLocked(key), nil, err
			}
		}
		return *cloneSession(s), nil, nil
	}

	first := s.Queue[0]
	count := 0
	textRunes := 0
	attachmentCount := 0
	attachmentBytes := int64(0)
	for _, input := range s.Queue {
		if input.State != InputQueued || !compatibleBatchInput(first, input) {
			break
		}
		if count > 0 && (first.Reset || input.Reset) {
			break
		}
		if limits.MaxInputs > 0 && count >= limits.MaxInputs {
			break
		}
		inputRunes := utf8.RuneCountInString(input.Text)
		if limits.MaxTextRunes > 0 && textRunes+inputRunes > limits.MaxTextRunes {
			if count == 0 {
				count++
			}
			break
		}
		inputAttachments, inputAttachmentBytes := attachmentUsage(input.Attachments)
		if count > 0 && ((limits.MaxAttachments > 0 && attachmentCount+inputAttachments > limits.MaxAttachments) ||
			(limits.MaxAttachmentBytes > 0 && attachmentBytes+inputAttachmentBytes > limits.MaxAttachmentBytes)) {
			break
		}
		count++
		textRunes += inputRunes
		attachmentCount += inputAttachments
		attachmentBytes += inputAttachmentBytes
		if first.Reset {
			break
		}
	}
	if count == 0 {
		return *cloneSession(s), nil, nil
	}

	inputs := cloneInputs(s.Queue[:count])
	for i := range inputs {
		inputs[i].State = InputStarting
	}
	s.Queue = append([]Input(nil), s.Queue[count:]...)
	m.batchSeq++
	s.ActiveBatch = &Batch{
		ID:        fmt.Sprintf("%s:%d:%d", s.ID, now.UnixNano(), m.batchSeq),
		Inputs:    cloneInputs(inputs),
		State:     InputStarting,
		CreatedAt: now,
	}
	if m.storePath != "" {
		if err := m.persistCandidateLocked(sessions, m.receipts, m.revision); err != nil {
			return m.currentSessionLocked(key), nil, err
		}
	}
	snapshot := cloneSession(s)
	return *snapshot, cloneBatch(s.ActiveBatch), nil
}

func attachmentUsage(attachments []media.Attachment) (int, int64) {
	var bytes int64
	for _, attachment := range attachments {
		bytes += attachment.Size
	}
	return len(attachments), bytes
}

// ReadyKeys returns scopes whose FIFO queue head can be frozen now. It is a
// read-only view: promotion from debouncing to queued belongs to
// FreezeReadyBatch so discovering ready work never changes durable state.
func (m *Manager) ReadyKeys(now time.Time) []Key {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys := make([]Key, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.ActiveBatch != nil || len(s.Queue) == 0 {
			continue
		}
		head := s.Queue[0]
		if head.State == InputQueued || (head.State == InputDebouncing && !head.DebounceUntil.After(now)) {
			keys = append(keys, s.Key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ID() < keys[j].ID() })
	return keys
}

// MarkBatchRunning publishes a starting batch as running after its candidate
// state is durable. A stale or non-starting batch is rejected so an old
// goroutine cannot claim a newer active batch.
func (m *Manager) MarkBatchRunning(key Key, batchID string, renderRef *RenderRef, startedAt time.Time) (Session, *Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessions := m.sessions
	if m.storePath != "" {
		sessions = cloneSessions(m.sessions)
	}
	s, ok := sessions[key.ID()]
	if !ok {
		return Session{}, nil, fmt.Errorf("session: active batch %q not found for %s", batchID, key.ID())
	}
	if s.ActiveBatch == nil {
		return *cloneSession(s), nil, fmt.Errorf("session: no active batch for %s", key.ID())
	}
	if s.ActiveBatch.ID != batchID {
		return *cloneSession(s), nil, fmt.Errorf("session: active batch mismatch for %s: got %q, want %q", key.ID(), batchID, s.ActiveBatch.ID)
	}
	if s.ActiveBatch.State != InputStarting {
		return *cloneSession(s), nil, fmt.Errorf("session: active batch %q is %q, want %q", batchID, s.ActiveBatch.State, InputStarting)
	}

	batch := s.ActiveBatch
	batch.State = InputRunning
	batch.StartedAt = startedAt
	batch.RenderRef = cloneRenderRef(renderRef)
	for i := range batch.Inputs {
		batch.Inputs[i].State = InputRunning
	}
	if len(batch.Inputs) > 0 {
		first := batch.Inputs[0]
		version := strings.TrimSpace(first.BridgeInstructionsVersion)
		if version == "" {
			version = m.currentBridgeInstructionsVersion
		}
		if first.Reset || s.BridgeInstructionsVersion == "" {
			s.BridgeInstructionsVersion = version
		}
		for i := range batch.Inputs {
			if strings.TrimSpace(batch.Inputs[i].BridgeInstructionsVersion) == "" {
				batch.Inputs[i].BridgeInstructionsVersion = version
			}
		}
		if first.Reset {
			s.AgentSessionID = ""
			s.Model = ""
			s.Tokens = 0
			s.History = nil
		}
		if first.WorkDir != "" {
			s.WorkDir = first.WorkDir
		}
	}
	for _, input := range batch.Inputs {
		appendPromptLocked(s, input)
	}
	s.State = StateRunning

	if m.storePath != "" {
		if err := m.persistCandidateLocked(sessions, m.receipts, m.revision); err != nil {
			return m.currentSessionLocked(key), nil, err
		}
	}
	return *cloneSession(s), cloneBatch(batch), nil
}

// ReplaceActiveBatchRenderRef durably replaces the complete render reference
// for one active batch before publishing the new value in memory.
func (m *Manager) ReplaceActiveBatchRenderRef(sessionID, batchID string, ref RenderRef) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(batchID) == "" || strings.TrimSpace(ref.CardID) == "" || ref.PendingSequence < 0 {
		return fmt.Errorf("session: invalid active batch render ref replacement")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	sessions := m.sessions
	if m.storePath != "" {
		sessions = cloneSessions(m.sessions)
	}
	s, ok := sessions[sessionID]
	if !ok || s.ActiveBatch == nil {
		return fmt.Errorf("session: active batch %q not found for %s", batchID, sessionID)
	}
	if s.ActiveBatch.ID != batchID {
		return fmt.Errorf("session: active batch mismatch for %s: got %q, want %q", sessionID, batchID, s.ActiveBatch.ID)
	}
	s.ActiveBatch.RenderRef = cloneRenderRef(&ref)
	if m.storePath != "" {
		return m.persistCandidateLocked(sessions, m.receipts, m.revision)
	}
	return nil
}

// FinishBatch records a terminal active-batch outcome and releases the scope
// for later queued work. It rejects stale batch IDs before changing anything.
func (m *Manager) FinishBatch(key Key, batchID string, completion BatchCompletion) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !terminalBatchStatus(completion.Status) {
		return m.currentSessionLocked(key), fmt.Errorf("session: invalid batch completion status %q", completion.Status)
	}
	sessions := m.sessions
	if m.storePath != "" {
		sessions = cloneSessions(m.sessions)
	}
	s, ok := sessions[key.ID()]
	if !ok {
		return Session{}, fmt.Errorf("session: active batch %q not found for %s", batchID, key.ID())
	}
	if s.ActiveBatch == nil {
		return *cloneSession(s), fmt.Errorf("session: no active batch for %s", key.ID())
	}
	if s.ActiveBatch.ID != batchID {
		return *cloneSession(s), fmt.Errorf("session: active batch mismatch for %s: got %q, want %q", key.ID(), batchID, s.ActiveBatch.ID)
	}

	for i := range s.ActiveBatch.Inputs {
		s.ActiveBatch.Inputs[i].State = completion.Status
	}
	s.ActiveBatch.State = completion.Status
	if completion.AgentSessionID != "" {
		s.AgentSessionID = completion.AgentSessionID
	}
	if completion.Model != "" {
		s.Model = completion.Model
	}
	if completion.Tokens > 0 {
		s.Tokens += completion.Tokens
	}
	s.ActiveBatch = nil
	s.State = StateIdle
	if !completion.At.IsZero() {
		s.LastActive = completion.At
		rearmCompatibleDebounceHead(s.Queue, completion.At)
	}

	if m.storePath != "" {
		if err := m.persistCandidateLocked(sessions, m.receipts, m.revision); err != nil {
			return m.currentSessionLocked(key), err
		}
	}
	return *cloneSession(s), nil
}

func rearmCompatibleDebounceHead(queue []Input, completedAt time.Time) {
	if len(queue) == 0 || completedAt.IsZero() {
		return
	}
	first := queue[0]
	count := 0
	window := time.Duration(0)
	for i := range queue {
		input := &queue[i]
		if input.State != InputDebouncing || input.Reset || !compatibleBatchInput(first, *input) {
			break
		}
		count++
		if input.DebounceWindow > window {
			window = input.DebounceWindow
		}
	}
	if count == 0 || window <= 0 {
		return
	}
	deadline := completedAt.Add(window)
	for i := 0; i < count; i++ {
		queue[i].DebounceUntil = deadline
	}
}

// CancelQueuedInputByMessageID removes exactly one queued or debouncing input
// matched by its own or reply message ID. Persistence failures report the
// match but leave the live queue unchanged so callers can retry safely.
func (m *Manager) CancelQueuedInputByMessageID(messageID string) (Session, Input, bool, error) {
	if messageID == "" {
		return Session{}, Input{}, false, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	sessions := m.sessions
	if m.storePath != "" {
		sessions = cloneSessions(m.sessions)
	}
	for _, s := range sessions {
		for i, input := range s.Queue {
			if (input.State != InputQueued && input.State != InputDebouncing) || (input.ID != messageID && input.ReplyToMessageID != messageID) {
				continue
			}
			input.State = InputCancelled
			s.Queue = append(s.Queue[:i:i], s.Queue[i+1:]...)
			if m.storePath != "" {
				if err := m.persistCandidateLocked(sessions, m.receipts, m.revision); err != nil {
					return m.currentSessionLocked(s.Key), input, true, err
				}
			}
			return *cloneSession(s), input, true, nil
		}
	}
	return Session{}, Input{}, false, nil
}

func terminalBatchStatus(status InputState) bool {
	switch status {
	case InputCompleted, InputFailed, InputCancelled, InputInterrupted:
		return true
	default:
		return false
	}
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
	s.Queue = nil
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

func (m *Manager) RemoveQueuedInputByMessageID(messageID string) (Session, Input, bool) {
	if messageID == "" {
		return Session{}, Input{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		for i, input := range s.Queue {
			if input.ReplyToMessageID != messageID {
				continue
			}
			s.Queue = append(s.Queue[:i], s.Queue[i+1:]...)
			return *cloneSession(s), input, true
		}
	}
	return Session{}, Input{}, false
}

func (m *Manager) UpdateRunResult(id, claudeSessionID, model string, tokens int) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}
	}
	if claudeSessionID != "" {
		s.AgentSessionID = claudeSessionID
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

func (m *Manager) HasWork() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		if session.State == StateRunning || session.ActiveBatch != nil || len(session.Queue) != 0 {
			return true
		}
	}
	return false
}

// LiveAttachmentPaths returns a detached set of every attachment still needed
// by runnable durable work. The manager lock protects only the in-memory copy;
// callers must perform any filesystem work after this method returns.
func (m *Manager) LiveAttachmentPaths() map[string]struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	paths := make(map[string]struct{})
	for _, s := range m.sessions {
		for _, input := range s.Queue {
			addLiveAttachmentPaths(paths, input)
		}
		if s.ActiveBatch == nil {
			continue
		}
		for _, input := range s.ActiveBatch.Inputs {
			addLiveAttachmentPaths(paths, input)
		}
	}
	return paths
}

func addLiveAttachmentPaths(paths map[string]struct{}, input Input) {
	switch input.State {
	case InputDebouncing, InputQueued, InputStarting, InputRunning:
		for _, attachment := range input.Attachments {
			if attachment.Path != "" {
				paths[attachment.Path] = struct{}{}
			}
		}
	}
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
	return ensureSessionIn(m.sessions, key, workDir)
}

func resetSessionLocked(s *Session, workDir string, now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	if workDir != "" {
		s.WorkDir = workDir
	}
	s.AgentSessionID = ""
	s.Model = ""
	s.Tokens = 0
	s.History = nil
	s.Queue = nil
	s.ActiveBatch = nil
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
	cp.Queue = cloneInputs(s.Queue)
	cp.ActiveBatch = cloneBatch(s.ActiveBatch)
	return &cp
}

func ensureSessionIn(sessions map[string]*Session, key Key, workDir string) *Session {
	id := key.ID()
	if s, ok := sessions[id]; ok {
		if s.WorkDir == "" && workDir != "" {
			s.WorkDir = workDir
		}
		return s
	}
	now := time.Now()
	s := &Session{Key: key, ID: id, WorkDir: workDir, State: StateIdle, CreatedAt: now, LastActive: now}
	sessions[id] = s
	return s
}

func (m *Manager) currentSessionLocked(key Key) Session {
	s, ok := m.sessions[key.ID()]
	if !ok {
		return Session{}
	}
	return *cloneSession(s)
}

func (m *Manager) persistCandidateLocked(sessions map[string]*Session, receipts []Receipt, revision uint64) error {
	nextRevision := revision + 1
	snapshot := Snapshot{
		Revision: revision + 1,
		SavedAt:  time.Now().UTC(),
		Sessions: snapshotSessions(sessions),
		Receipts: snapshotReceipts(receipts),
	}
	if err := SaveSnapshot(m.storePath, snapshot); err != nil {
		m.lastPersistErr = err
		return err
	}
	m.sessions = sessions
	m.receipts = cloneReceipts(receipts)
	m.revision = nextRevision
	m.lastPersistErr = nil
	return nil
}

func restoredSessions(sessions []Session) (map[string]*Session, []RecoveryNotice) {
	candidate := make(map[string]*Session, len(sessions))
	var notices []RecoveryNotice
	for i := range sessions {
		s := cloneSession(&sessions[i])
		for _, input := range s.Queue {
			if status, ok := recoveryStatus(input.State, ""); ok {
				notices = append(notices, RecoveryNotice{
					SessionID:        s.ID,
					ReplyToMessageID: input.ReplyToMessageID,
					CardSessionID:    input.CardSessionID,
					Status:           status,
				})
			}
		}
		if s.ActiveBatch != nil {
			for _, input := range s.ActiveBatch.Inputs {
				if status, ok := recoveryStatus(input.State, s.ActiveBatch.State); ok {
					notices = append(notices, RecoveryNotice{
						SessionID:        s.ID,
						ReplyToMessageID: input.ReplyToMessageID,
						CardSessionID:    input.CardSessionID,
						Status:           status,
						RenderRef:        cloneRenderRef(s.ActiveBatch.RenderRef),
					})
				}
			}
		}
		s.Queue = nil
		s.ActiveBatch = nil
		s.State = StateIdle
		id := s.ID
		if id == "" {
			id = s.Key.ID()
			s.ID = id
		}
		candidate[id] = s
	}
	return candidate, notices
}

func recoveryStatus(state, fallback InputState) (InputState, bool) {
	if state == "" {
		state = fallback
	}
	switch state {
	case InputDebouncing, InputQueued, InputStarting:
		return InputCancelled, true
	case InputRunning:
		return InputInterrupted, true
	default:
		return "", false
	}
}

func cloneSessions(sessions map[string]*Session) map[string]*Session {
	cp := make(map[string]*Session, len(sessions))
	for id, s := range sessions {
		cp[id] = cloneSession(s)
	}
	return cp
}

func snapshotSessions(sessions map[string]*Session) []Session {
	list := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		list = append(list, *cloneSession(s))
	}
	sort.Slice(list, func(i, j int) bool {
		left, right := list[i].Key.ID(), list[j].Key.ID()
		if left == right {
			return list[i].ID < list[j].ID
		}
		return left < right
	})
	return list
}

func cloneReceipts(receipts []Receipt) []Receipt {
	return append([]Receipt(nil), receipts...)
}

func snapshotReceipts(receipts []Receipt) []Receipt {
	list := cloneReceipts(receipts)
	sort.Slice(list, func(i, j int) bool {
		if list[i].MessageID == list[j].MessageID {
			return list[i].ExpiresAt.Before(list[j].ExpiresAt)
		}
		return list[i].MessageID < list[j].MessageID
	})
	return list
}

func compatibleBatchInput(first, next Input) bool {
	if first.Compact || next.Compact {
		return first.Compact && next.Compact && first.ID == next.ID
	}
	if first.ScheduleRunID != "" || next.ScheduleRunID != "" {
		return first.ScheduleRunID != "" && first.ScheduleRunID == next.ScheduleRunID
	}
	return first.WorkDir == next.WorkDir &&
		first.RequestedModel == next.RequestedModel &&
		first.RequestedEffort == next.RequestedEffort &&
		first.AgentBin == next.AgentBin &&
		first.AgentHome == next.AgentHome &&
		first.BridgeInstructionsVersion == next.BridgeInstructionsVersion &&
		first.EffectiveReplyMode() == next.EffectiveReplyMode() &&
		first.EffectiveAppendOverflowMode() == next.EffectiveAppendOverflowMode() &&
		first.ConversationMode == next.ConversationMode
}

func cloneInputs(inputs []Input) []Input {
	cloned := append([]Input(nil), inputs...)
	for i := range cloned {
		cloned[i].Attachments = append([]media.Attachment(nil), inputs[i].Attachments...)
	}
	return cloned
}

func cloneBatch(batch *Batch) *Batch {
	if batch == nil {
		return nil
	}
	cp := *batch
	cp.Inputs = cloneInputs(batch.Inputs)
	cp.RenderRef = cloneRenderRef(batch.RenderRef)
	return &cp
}

func cloneRenderRef(ref *RenderRef) *RenderRef {
	if ref == nil {
		return nil
	}
	cp := *ref
	return &cp
}
