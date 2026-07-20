package schedule

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrContextUnknown = errors.New("schedule proposal context is unknown")
	ErrContextUsed    = errors.New("schedule proposal context was already used")
	ErrContextExpired = errors.New("schedule proposal context expired")
)

type ProposalContext struct {
	OriginRunID string
	Creator     string
	Target      Target
	Execution   FrozenExecution
	ForcedKind  Kind
	ExpiresAt   time.Time
}

type contextRecord struct {
	context ProposalContext
	used    bool
}

type ContextRegistry struct {
	mu      sync.Mutex
	ttl     time.Duration
	records map[string]contextRecord
}

func NewContextRegistry(ttl time.Duration) *ContextRegistry {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &ContextRegistry{ttl: ttl, records: map[string]contextRecord{}}
}

func (r *ContextRegistry) Issue(value ProposalContext, now time.Time) (string, error) {
	if r == nil {
		return "", errors.New("schedule context registry is nil")
	}
	if value.Creator == "" || value.Target.ChatID == "" || value.OriginRunID == "" {
		return "", errors.New("schedule proposal context is incomplete")
	}
	token, err := randomHex(32)
	if err != nil {
		return "", err
	}
	value.ExpiresAt = now.Add(r.ttl)
	r.mu.Lock()
	r.records[token] = contextRecord{context: value}
	r.pruneLocked(now)
	r.mu.Unlock()
	return token, nil
}

func (r *ContextRegistry) Consume(token string, now time.Time) (ProposalContext, error) {
	if r == nil || token == "" {
		return ProposalContext{}, ErrContextUnknown
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[token]
	if !ok {
		return ProposalContext{}, ErrContextUnknown
	}
	if record.used {
		return ProposalContext{}, ErrContextUsed
	}
	if !record.context.ExpiresAt.After(now) {
		delete(r.records, token)
		return ProposalContext{}, ErrContextExpired
	}
	record.used = true
	r.records[token] = record
	return record.context, nil
}

func (r *ContextRegistry) Revoke(token string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.records, token)
	r.mu.Unlock()
}

func (r *ContextRegistry) pruneLocked(now time.Time) {
	for token, record := range r.records {
		if !record.context.ExpiresAt.After(now) {
			delete(r.records, token)
		}
	}
}

type ProposeRequest struct {
	Token    string   `json:"token"`
	Proposal Proposal `json:"proposal"`
}

type ProposeResponse struct {
	DraftID     string      `json:"draft_id,omitempty"`
	Description string      `json:"description,omitempty"`
	Next        []time.Time `json:"next,omitempty"`
	Error       string      `json:"error,omitempty"`
}

type ControlConfig struct {
	Now      func() time.Time
	DraftTTL time.Duration
}

type ControlServer struct {
	path     string
	store    *Store
	registry *ContextRegistry
	config   ControlConfig

	mu       sync.Mutex
	listener net.Listener
	closed   bool
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func NewControlServer(path string, store *Store, registry *ContextRegistry, cfg ControlConfig) *ControlServer {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.DraftTTL <= 0 {
		cfg.DraftTTL = 10 * time.Minute
	}
	return &ControlServer{path: path, store: store, registry: registry, config: cfg}
}

func (s *ControlServer) Start(ctx context.Context) error {
	if s == nil || s.store == nil || s.registry == nil {
		return errors.New("schedule control server is not configured")
	}
	if s.path == "" {
		return errors.New("schedule control socket path is empty")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create schedule control directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("protect schedule control directory: %w", err)
	}
	if info, err := os.Lstat(s.path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket schedule control path %q", s.path)
		}
		if err := os.Remove(s.path); err != nil {
			return fmt.Errorf("remove stale schedule control socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect schedule control socket: %w", err)
	}
	listener, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("listen on schedule control socket: %w", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(s.path)
		return fmt.Errorf("protect schedule control socket: %w", err)
	}
	s.mu.Lock()
	s.listener = listener
	s.closed = false
	stopCh := make(chan struct{})
	s.stopCh = stopCh
	s.mu.Unlock()
	s.wg.Add(2)
	go s.watchContext(ctx, listener, stopCh)
	go s.acceptLoop(ctx, listener)
	return nil
}

func (s *ControlServer) watchContext(ctx context.Context, listener net.Listener, stopCh <-chan struct{}) {
	defer s.wg.Done()
	select {
	case <-ctx.Done():
		_ = listener.Close()
	case <-stopCh:
	}
}

func (s *ControlServer) acceptLoop(ctx context.Context, listener net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || s.isClosed() || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *ControlServer) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	decoder := json.NewDecoder(io.LimitReader(bufio.NewReader(conn), 64<<10))
	decoder.DisallowUnknownFields()
	var request ProposeRequest
	if err := decoder.Decode(&request); err != nil {
		s.writeResponse(conn, ProposeResponse{Error: "invalid proposal request: " + err.Error()})
		return
	}
	response, err := s.propose(request)
	if err != nil {
		response = ProposeResponse{Error: err.Error()}
	}
	s.writeResponse(conn, response)
}

func (s *ControlServer) propose(request ProposeRequest) (ProposeResponse, error) {
	now := s.config.Now()
	bound, err := s.registry.Consume(request.Token, now)
	if err != nil {
		return ProposeResponse{}, err
	}
	proposal := request.Proposal
	if bound.ForcedKind != "" {
		if proposal.Kind != "" && proposal.Kind != bound.ForcedKind {
			return ProposeResponse{}, fmt.Errorf("proposal kind %q does not match required kind %q", proposal.Kind, bound.ForcedKind)
		}
		proposal.Kind = bound.ForcedKind
	}
	rule, err := NormalizeProposal(proposal, now)
	if err != nil {
		return ProposeResponse{}, err
	}
	draftID, err := s.unusedDraftID()
	if err != nil {
		return ProposeResponse{}, err
	}
	draft := Draft{
		ID: draftID, OriginRunID: bound.OriginRunID, Kind: rule.Kind, CronExpr: rule.CronExpr,
		ScheduledAt: rule.ScheduledAt, Timezone: rule.Timezone, Description: rule.Description,
		Prompt: rule.Prompt, Creator: bound.Creator, Target: bound.Target, Execution: bound.Execution,
		CreatedAt: now, ExpiresAt: now.Add(s.config.DraftTTL), Next: append([]time.Time(nil), rule.Next...),
	}
	if err := s.store.CreateDraft(draft); err != nil {
		return ProposeResponse{}, err
	}
	return ProposeResponse{DraftID: draft.ID, Description: rule.Description, Next: append([]time.Time(nil), rule.Next...)}, nil
}

func (s *ControlServer) unusedDraftID() (string, error) {
	for range 16 {
		id, err := randomHex(4)
		if err != nil {
			return "", err
		}
		if _, ok := s.store.Draft(id); ok {
			continue
		}
		if _, ok := s.store.Task(id); ok {
			continue
		}
		return id, nil
	}
	return "", errors.New("could not allocate unique schedule draft id")
}

func (s *ControlServer) writeResponse(conn net.Conn, response ProposeResponse) {
	_ = json.NewEncoder(conn).Encode(response)
}

func (s *ControlServer) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *ControlServer) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	listener := s.listener
	stopCh := s.stopCh
	if stopCh != nil {
		close(stopCh)
		s.stopCh = nil
	}
	s.mu.Unlock()
	var err error
	if listener != nil {
		err = listener.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
	}
	s.wg.Wait()
	if removeErr := os.Remove(s.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
		err = removeErr
	}
	return err
}

type ProposeClient struct {
	SocketPath string
	Timeout    time.Duration
}

func (c ProposeClient) Propose(ctx context.Context, request ProposeRequest) (ProposeResponse, error) {
	if c.SocketPath == "" {
		return ProposeResponse{}, errors.New("schedule control socket path is empty")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return ProposeResponse{}, fmt.Errorf("connect to schedule control socket: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return ProposeResponse{}, fmt.Errorf("send schedule proposal: %w", err)
	}
	var response ProposeResponse
	if err := json.NewDecoder(io.LimitReader(conn, 64<<10)).Decode(&response); err != nil {
		return ProposeResponse{}, fmt.Errorf("read schedule proposal response: %w", err)
	}
	if response.Error != "" {
		return ProposeResponse{}, errors.New(response.Error)
	}
	return response, nil
}

func randomHex(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random schedule token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
