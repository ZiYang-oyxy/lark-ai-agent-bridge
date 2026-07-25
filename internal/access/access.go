package access

import (
	"sync"
	"time"
)

type Reason string

const (
	ReasonOwner         Reason = "owner"
	ReasonAllowedUser   Reason = "allowed-user"
	ReasonAllowedAdmin  Reason = "allowed-admin"
	ReasonAllowedChat   Reason = "allowed-chat"
	ReasonAllowedMember Reason = "allowed-group-member"
	ReasonDeniedUser    Reason = "denied-user"
	ReasonDeniedChat    Reason = "denied-chat"
	ReasonDeniedMember  Reason = "denied-group-member"
	ReasonDeniedAdmin   Reason = "denied-admin"
)

type GroupMode string

const (
	GroupModeAllMembers      GroupMode = "all_members"
	GroupModeSelectedMembers GroupMode = "selected_members"
)

type GroupPolicy struct {
	Mode           GroupMode `json:"mode"`
	AllowedMembers []string  `json:"allowed_members,omitempty"`
}

type Decision struct {
	OK     bool
	Reason Reason
}

type Policy struct {
	AllowedUsers  []string               `json:"allowed_users"`
	AllowedChats  []string               `json:"allowed_chats"`
	Admins        []string               `json:"admins"`
	GroupPolicies map[string]GroupPolicy `json:"group_policies,omitempty"`
}

type OwnerRefreshState string

const (
	OwnerRefreshUnknown OwnerRefreshState = "unknown"
	OwnerRefreshOK      OwnerRefreshState = "ok"
	OwnerRefreshFailed  OwnerRefreshState = "failed"
)

type RuntimeSnapshot struct {
	BotOwnerID        string
	OwnerRefreshState OwnerRefreshState
	OwnerRefreshedAt  time.Time
	OwnerRefreshError string
}

type RuntimeControls struct {
	mu       sync.RWMutex
	snapshot RuntimeSnapshot
}

func NewRuntimeControls() *RuntimeControls {
	return &RuntimeControls{snapshot: RuntimeSnapshot{OwnerRefreshState: OwnerRefreshUnknown}}
}

func (c *RuntimeControls) Snapshot() RuntimeSnapshot {
	if c == nil {
		return RuntimeSnapshot{OwnerRefreshState: OwnerRefreshUnknown}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

func (c *RuntimeControls) OwnerRefreshSucceeded(ownerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.BotOwnerID = ownerID
	c.snapshot.OwnerRefreshState = OwnerRefreshOK
	c.snapshot.OwnerRefreshedAt = time.Now()
	c.snapshot.OwnerRefreshError = ""
}

func (c *RuntimeControls) OwnerRefreshFailed(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.OwnerRefreshState = OwnerRefreshFailed
	c.snapshot.OwnerRefreshedAt = time.Now()
	if err != nil {
		c.snapshot.OwnerRefreshError = err.Error()
	}
}

func (c *RuntimeControls) SetCachedOwnerForTest(ownerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.BotOwnerID = ownerID
}

func IsCreator(controls *RuntimeControls, senderID string) bool {
	s := controls.Snapshot()
	return s.OwnerRefreshState != OwnerRefreshUnknown && s.BotOwnerID != "" && s.BotOwnerID == senderID
}

func CanUseDM(policy Policy, controls *RuntimeControls, senderID string) Decision {
	if IsCreator(controls, senderID) {
		return allow(ReasonOwner)
	}
	if contains(policy.AllowedUsers, senderID) {
		return allow(ReasonAllowedUser)
	}
	if contains(policy.Admins, senderID) {
		return allow(ReasonAllowedAdmin)
	}
	return deny(ReasonDeniedUser)
}

func CanUseGroup(policy Policy, controls *RuntimeControls, chatID, senderID string) Decision {
	if IsCreator(controls, senderID) {
		return allow(ReasonOwner)
	}
	if contains(policy.Admins, senderID) {
		return allow(ReasonAllowedAdmin)
	}
	if contains(policy.AllowedChats, chatID) {
		group := GroupPolicyFor(policy, chatID)
		if group.Mode == GroupModeSelectedMembers {
			if contains(group.AllowedMembers, senderID) {
				return allow(ReasonAllowedMember)
			}
			return deny(ReasonDeniedMember)
		}
		return allow(ReasonAllowedChat)
	}
	return deny(ReasonDeniedChat)
}

func GroupPolicyFor(policy Policy, chatID string) GroupPolicy {
	group, ok := policy.GroupPolicies[chatID]
	if !ok || group.Mode == "" {
		return GroupPolicy{Mode: GroupModeAllMembers}
	}
	return GroupPolicy{Mode: group.Mode, AllowedMembers: append([]string(nil), group.AllowedMembers...)}
}

func CanRunAdminCommand(policy Policy, controls *RuntimeControls, senderID string) Decision {
	if IsCreator(controls, senderID) {
		return allow(ReasonOwner)
	}
	if contains(policy.Admins, senderID) {
		return allow(ReasonAllowedAdmin)
	}
	return deny(ReasonDeniedAdmin)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func allow(reason Reason) Decision { return Decision{OK: true, Reason: reason} }
func deny(reason Reason) Decision  { return Decision{Reason: reason} }
