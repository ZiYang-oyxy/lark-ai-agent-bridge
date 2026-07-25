package actiongrant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const SchemaVersion = 1

type Reason string

const (
	ReasonAllowed       Reason = "allowed"
	ReasonMissing       Reason = "missing"
	ReasonActorMismatch Reason = "actor-mismatch"
	ReasonScopeMismatch Reason = "scope-mismatch"
	ReasonValueMismatch Reason = "value-mismatch"
	ReasonExpired       Reason = "expired"
	ReasonReplayed      Reason = "replayed"
	ReasonPolicyChanged Reason = "policy-changed"
)

type Grant struct {
	ID             string     `json:"id"`
	Actor          string     `json:"actor"`
	ChatID         string     `json:"chat_id,omitempty"`
	SessionID      string     `json:"session_id"`
	ActionID       string     `json:"action_id"`
	ValueDigest    string     `json:"value_digest"`
	PolicyRevision uint64     `json:"policy_revision"`
	AllowAdmin     bool       `json:"allow_admin"`
	ExpiresAt      time.Time  `json:"expires_at"`
	ConsumedAt     *time.Time `json:"consumed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type Spec struct {
	Actor          string
	ChatID         string
	SessionID      string
	ActionID       string
	Value          string
	PolicyRevision uint64
	AllowAdmin     bool
	ExpiresAt      time.Time
}

type Request struct {
	GrantID        string
	Actor          string
	ChatID         string
	SessionID      string
	ActionID       string
	Value          string
	PolicyRevision uint64
	ActorIsAdmin   bool
	Now            time.Time
}

type Decision struct {
	OK     bool
	Reason Reason
	Grant  Grant
}

type snapshot struct {
	SchemaVersion int              `json:"schema_version"`
	Grants        map[string]Grant `json:"grants"`
}

type Store struct {
	mu     sync.Mutex
	path   string
	grants map[string]Grant
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("actiongrant: empty store path")
	}
	store := &Store{path: path, grants: map[string]Grant{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read action grant store: %w", err)
	}
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("decode action grant store: %w", err)
	}
	if saved.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported action grant schema %d", saved.SchemaVersion)
	}
	if saved.Grants != nil {
		store.grants = saved.Grants
	}
	return store, nil
}

func (s *Store) Issue(spec Spec, now time.Time) (Grant, error) {
	if s == nil {
		return Grant{}, errors.New("actiongrant: store unavailable")
	}
	if strings.TrimSpace(spec.Actor) == "" || strings.TrimSpace(spec.ChatID) == "" || strings.TrimSpace(spec.SessionID) == "" || strings.TrimSpace(spec.ActionID) == "" {
		return Grant{}, errors.New("actiongrant: actor, chat, session, and action are required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if !spec.ExpiresAt.After(now) {
		return Grant{}, errors.New("actiongrant: expiry must be in the future")
	}
	id, err := randomID()
	if err != nil {
		return Grant{}, err
	}
	grant := Grant{
		ID: id, Actor: spec.Actor, ChatID: spec.ChatID, SessionID: spec.SessionID,
		ActionID: spec.ActionID, ValueDigest: digest(spec.Value), PolicyRevision: spec.PolicyRevision,
		AllowAdmin: spec.AllowAdmin, ExpiresAt: spec.ExpiresAt.UTC(), CreatedAt: now.UTC(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneGrants(s.grants)
	prune(next, now.Add(-24*time.Hour))
	next[id] = grant
	if err := save(s.path, next); err != nil {
		return Grant{}, err
	}
	s.grants = next
	return grant, nil
}

func (s *Store) Consume(req Request) (Decision, error) {
	if s == nil {
		return Decision{Reason: ReasonMissing}, errors.New("actiongrant: store unavailable")
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.grants[req.GrantID]
	if !ok {
		return Decision{Reason: ReasonMissing}, nil
	}
	decision := Decision{Grant: grant}
	switch {
	case grant.ConsumedAt != nil:
		decision.Reason = ReasonReplayed
	case !grant.ExpiresAt.After(req.Now):
		decision.Reason = ReasonExpired
	case grant.PolicyRevision != req.PolicyRevision:
		decision.Reason = ReasonPolicyChanged
	case grant.ChatID != req.ChatID || grant.SessionID != req.SessionID || grant.ActionID != req.ActionID:
		decision.Reason = ReasonScopeMismatch
	case grant.ValueDigest != digest(req.Value):
		decision.Reason = ReasonValueMismatch
	case grant.Actor != req.Actor && !(grant.AllowAdmin && req.ActorIsAdmin):
		decision.Reason = ReasonActorMismatch
	default:
		consumedAt := req.Now.UTC()
		grant.ConsumedAt = &consumedAt
		next := cloneGrants(s.grants)
		next[grant.ID] = grant
		prune(next, req.Now.Add(-24*time.Hour))
		if err := save(s.path, next); err != nil {
			return Decision{}, err
		}
		s.grants = next
		return Decision{OK: true, Reason: ReasonAllowed, Grant: grant}, nil
	}
	return decision, nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomID() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate action grant id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func cloneGrants(in map[string]Grant) map[string]Grant {
	out := make(map[string]Grant, len(in))
	for id, grant := range in {
		out[id] = grant
	}
	return out
}

func prune(grants map[string]Grant, before time.Time) {
	for id, grant := range grants {
		if grant.ExpiresAt.Before(before) {
			delete(grants, id)
		}
	}
}

func save(path string, grants map[string]Grant) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create action grant directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot{SchemaVersion: SchemaVersion, Grants: grants}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode action grant store: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".action-grants-*.tmp")
	if err != nil {
		return fmt.Errorf("create action grant temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace action grant store: %w", err)
	}
	return nil
}
