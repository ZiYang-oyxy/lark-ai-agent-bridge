package config

import (
	"fmt"
	"strings"
)

// ChatOverride is a per-chat, per-field override of the global runtime
// preference. Every field is a pointer: a nil field means "inherit the global
// value for this field". Only execution-oriented preferences are overridable;
// access control (allowed users/chats/admins) is intentionally never per-chat.
type ChatOverride struct {
	Model              *string             `json:"model,omitempty"`
	Effort             *string             `json:"effort,omitempty"`
	ReplyMode          *ReplyMode          `json:"reply_mode,omitempty"`
	ConversationMode   *ConversationMode   `json:"conversation_mode,omitempty"`
	TopicSeedMode      *TopicSeedMode      `json:"topic_seed_mode,omitempty"`
	GroupMessageMode   *GroupMessageMode   `json:"group_message_mode,omitempty"`
	AppendOverflowMode *AppendOverflowMode `json:"append_overflow_mode,omitempty"`
	RespondToBots      *bool               `json:"respond_to_bots,omitempty"`
	// ShowMetaRowAgent / ShowMetaRowRuntime / ShowMetaRowDeveloper 是新的三个独立
	// 元信息行开关的按群覆盖。ShowMetaRows 是旧字段,只保留读侧迁移(见 mergeChatOverride)。
	ShowMetaRowAgent     *bool   `json:"show_meta_row_agent,omitempty"`
	ShowMetaRowRuntime   *bool   `json:"show_meta_row_runtime,omitempty"`
	ShowMetaRowDeveloper *bool   `json:"show_meta_row_developer,omitempty"`
	ShowMetaRows         *bool   `json:"show_meta_rows,omitempty"`
	Agent                *string `json:"agent,omitempty"`
	AgentHome            *string `json:"agent_home,omitempty"`
	AgentBin             *string `json:"agent_bin,omitempty"`
}

// IsEmpty reports whether the override sets no field at all.
func (o ChatOverride) IsEmpty() bool {
	return o.Model == nil && o.Effort == nil && o.ReplyMode == nil &&
		o.ConversationMode == nil && o.TopicSeedMode == nil && o.GroupMessageMode == nil &&
		o.AppendOverflowMode == nil &&
		o.RespondToBots == nil && o.ShowMetaRowAgent == nil &&
		o.ShowMetaRowRuntime == nil && o.ShowMetaRowDeveloper == nil &&
		o.ShowMetaRows == nil && o.Agent == nil &&
		o.AgentHome == nil && o.AgentBin == nil
}

// normalizeChatOverride trims/lowercases set string fields to match the
// normalization applied to a full RuntimePreference. nil fields stay nil.
func normalizeChatOverride(o ChatOverride) ChatOverride {
	if o.Model != nil {
		v := strings.TrimSpace(*o.Model)
		for _, model := range builtinModels {
			if strings.EqualFold(v, model) {
				v = model
				break
			}
		}
		o.Model = &v
	}
	if o.Effort != nil {
		v := strings.ToLower(strings.TrimSpace(*o.Effort))
		o.Effort = &v
	}
	if o.ReplyMode != nil {
		v := ReplyMode(strings.ToLower(strings.TrimSpace(string(*o.ReplyMode))))
		o.ReplyMode = &v
	}
	if o.ConversationMode != nil {
		v := ConversationMode(strings.ToLower(strings.TrimSpace(string(*o.ConversationMode))))
		o.ConversationMode = &v
	}
	if o.TopicSeedMode != nil {
		v := TopicSeedMode(strings.ToLower(strings.TrimSpace(string(*o.TopicSeedMode))))
		o.TopicSeedMode = &v
	}
	if o.GroupMessageMode != nil {
		v := GroupMessageMode(strings.ToLower(strings.TrimSpace(string(*o.GroupMessageMode))))
		o.GroupMessageMode = &v
	}
	if o.AppendOverflowMode != nil {
		v := AppendOverflowMode(strings.ToLower(strings.TrimSpace(string(*o.AppendOverflowMode))))
		o.AppendOverflowMode = &v
	}
	if o.Agent != nil {
		v := strings.ToLower(strings.TrimSpace(*o.Agent))
		o.Agent = &v
	}
	if o.AgentHome != nil {
		v := strings.TrimSpace(*o.AgentHome)
		o.AgentHome = &v
	}
	if o.AgentBin != nil {
		v := strings.TrimSpace(*o.AgentBin)
		o.AgentBin = &v
	}
	return o
}

// ChatOverrideAdjustmentAction describes how a per-chat override was repaired
// when it stopped merging into a legal preference.
type ChatOverrideAdjustmentAction string

const (
	// ChatOverridePinnedAgent means the agent kind the chat had been inheriting
	// was written into the override so its home/bin selection stays legal.
	ChatOverridePinnedAgent ChatOverrideAdjustmentAction = "pinned_agent"
	// ChatOverrideClearedAgent means the agent/home/bin fields were dropped so
	// the chat follows the global agent again; other fields are kept.
	ChatOverrideClearedAgent ChatOverrideAdjustmentAction = "cleared_agent"
	// ChatOverrideDropped means the whole override was removed.
	ChatOverrideDropped ChatOverrideAdjustmentAction = "dropped"
)

// ChatOverrideAdjustment records one per-chat override that had to be repaired
// or removed because it no longer merges into a legal preference.
type ChatOverrideAdjustment struct {
	ChatID string
	Action ChatOverrideAdjustmentAction
	Reason string
}

// pinAgentForOverride fixes the agent kind into an override that selects an
// agent-specific home or bin. Home/bin labels only exist under one agent kind,
// so leaving Agent nil makes the override illegal the moment the global agent
// changes — the exact shape that used to abort startup. global must already be
// the normalized effective global preference.
func pinAgentForOverride(o ChatOverride, global RuntimePreference) ChatOverride {
	if o.Agent != nil || (o.AgentHome == nil && o.AgentBin == nil) {
		return o
	}
	agent := strings.ToLower(strings.TrimSpace(global.Agent))
	if agent == "" {
		agent = DefaultAgentKind
	}
	o.Agent = &agent
	return o
}

// reconcileChatOverridesLocked re-validates every per-chat override against
// nextGlobal and repairs the ones that no longer merge legally, returning the
// map to persist plus a description of what changed. A global write (above all
// switching agent) can invalidate overrides that were legal when written; left
// alone they sit illegal on disk and only surface at the next startup.
//
// Repairs escalate: pin the agent the chat was inheriting (keeps its home/bin
// choice), else drop just the agent dimension (chat follows the global agent),
// else drop the override. previousGlobal supplies the agent being inherited
// before this write. Callers must hold s.mu.
func (s *PreferenceStore) reconcileChatOverridesLocked(nextGlobal, previousGlobal RuntimePreference) (map[string]ChatOverride, []ChatOverrideAdjustment) {
	if len(s.chatOverrides) == 0 {
		return s.chatOverrides, nil
	}
	var adjustments []ChatOverrideAdjustment
	next := cloneChatOverrides(s.chatOverrides)
	for chatID, override := range s.chatOverrides {
		_, err := mergeChatOverride(nextGlobal, override, s.allowedModels, s.agents)
		if err == nil {
			continue
		}
		reason := err.Error()
		if pinned := pinAgentForOverride(override, previousGlobal); override.Agent == nil && pinned.Agent != nil {
			if _, err := mergeChatOverride(nextGlobal, pinned, s.allowedModels, s.agents); err == nil {
				next[chatID] = pinned
				adjustments = append(adjustments, ChatOverrideAdjustment{ChatID: chatID, Action: ChatOverridePinnedAgent, Reason: reason})
				continue
			}
		}
		cleared := override
		cleared.Agent, cleared.AgentHome, cleared.AgentBin = nil, nil, nil
		if !cleared.IsEmpty() {
			if _, err := mergeChatOverride(nextGlobal, cleared, s.allowedModels, s.agents); err == nil {
				next[chatID] = cleared
				adjustments = append(adjustments, ChatOverrideAdjustment{ChatID: chatID, Action: ChatOverrideClearedAgent, Reason: reason})
				continue
			}
		}
		delete(next, chatID)
		adjustments = append(adjustments, ChatOverrideAdjustment{ChatID: chatID, Action: ChatOverrideDropped, Reason: reason})
	}
	if len(adjustments) == 0 {
		return s.chatOverrides, nil
	}
	if len(next) == 0 {
		next = nil
	}
	return next, adjustments
}

// mergeChatOverride layers a chat override on top of a base preference,
// normalizes and validates the result. The base is expected to already be the
// effective global preference.
func mergeChatOverride(base RuntimePreference, o ChatOverride, allowedModels []string, agents []AgentDef) (RuntimePreference, error) {
	merged := base
	if o.Model != nil {
		merged.Model = *o.Model
	}
	if o.Effort != nil {
		merged.Effort = *o.Effort
	}
	if o.ReplyMode != nil {
		merged.ReplyMode = *o.ReplyMode
	}
	if o.ConversationMode != nil {
		merged.ConversationMode = *o.ConversationMode
	}
	if o.TopicSeedMode != nil {
		merged.TopicSeedMode = *o.TopicSeedMode
	}
	if o.GroupMessageMode != nil {
		merged.GroupMessageMode = *o.GroupMessageMode
	}
	if o.AppendOverflowMode != nil {
		merged.AppendOverflowMode = *o.AppendOverflowMode
	}
	if o.RespondToBots != nil {
		merged.RespondToBots = *o.RespondToBots
	}
	if o.ShowMetaRowAgent != nil {
		merged.ShowMetaRowAgent = *o.ShowMetaRowAgent
	}
	if o.ShowMetaRowRuntime != nil {
		merged.ShowMetaRowRuntime = *o.ShowMetaRowRuntime
	}
	if o.ShowMetaRowDeveloper != nil {
		merged.ShowMetaRowDeveloper = *o.ShowMetaRowDeveloper
	}
	// 旧字段 ShowMetaRows override 的迁移:仅当新三项均未 override 且旧字段被显式
	// 设为 true 时,把 agent+runtime 两行都打开(与旧版语义一致,不涉及新增的
	// developer 行)。写路径不再产生 ShowMetaRows override。
	if o.ShowMetaRows != nil && o.ShowMetaRowAgent == nil && o.ShowMetaRowRuntime == nil && o.ShowMetaRowDeveloper == nil {
		if *o.ShowMetaRows {
			merged.ShowMetaRowAgent = true
			merged.ShowMetaRowRuntime = true
		} else {
			merged.ShowMetaRowAgent = false
			merged.ShowMetaRowRuntime = false
		}
	}
	if o.Agent != nil {
		merged.Agent = *o.Agent
	}
	if o.AgentHome != nil {
		merged.AgentHome = *o.AgentHome
	}
	if o.AgentBin != nil {
		merged.AgentBin = *o.AgentBin
	}
	merged = normalizeRuntimePreference(merged)
	if err := validateRuntimePreferenceWith(merged, allowedModels, agents); err != nil {
		return RuntimePreference{}, err
	}
	return merged, nil
}

// effectiveGlobalLocked returns the current global effective preference
// (override if set, otherwise defaults). Callers must hold s.mu.
func (s *PreferenceStore) effectiveGlobalLocked() RuntimePreference {
	if s.override != nil {
		return *s.override
	}
	return s.defaults
}

// GetForChat returns the effective preference for a chat: the global effective
// preference with the chat's non-nil override fields layered on top. Chats
// without an override return exactly the global effective preference.
func (s *PreferenceStore) GetForChat(chatID string) RuntimePreference {
	s.mu.RLock()
	defer s.mu.RUnlock()
	base := s.effectiveGlobalLocked()
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return base
	}
	override, ok := s.chatOverrides[chatID]
	if !ok || override.IsEmpty() {
		return base
	}
	// Stored overrides were validated on write; a merge failure here would be
	// a stored-data bug, so fall back to the global preference rather than
	// returning a partial/invalid one.
	merged, err := mergeChatOverride(base, override, s.allowedModels, s.agents)
	if err != nil {
		return base
	}
	return merged
}

// SnapshotForChat returns the global and effective chat preferences together
// with one shared store revision.
func (s *PreferenceStore) SnapshotForChat(chatID string) (RuntimePreference, RuntimePreference, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	global := s.effectiveGlobalLocked()
	chatID = strings.TrimSpace(chatID)
	override, ok := s.chatOverrides[chatID]
	if !ok || override.IsEmpty() {
		return global, global, s.revision
	}
	effective, err := mergeChatOverride(global, override, s.allowedModels, s.agents)
	if err != nil {
		return global, global, s.revision
	}
	return global, effective, s.revision
}

// ChatOverride returns the stored override for a chat and whether one exists.
func (s *PreferenceStore) ChatOverride(chatID string) (ChatOverride, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	override, ok := s.chatOverrides[strings.TrimSpace(chatID)]
	return override, ok
}

// SetChat stores a per-chat override. The override is validated by merging it
// onto the current global effective preference; an illegal merged result is
// rejected and nothing is persisted. An empty override is treated as ResetChat.
func (s *PreferenceStore) SetChat(chatID string, override ChatOverride) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return fmt.Errorf("config: empty chat id")
	}
	override = normalizeChatOverride(override)
	s.mu.Lock()
	defer s.mu.Unlock()
	if override.IsEmpty() {
		return s.resetChatLocked(chatID)
	}
	global := s.effectiveGlobalLocked()
	override = pinAgentForOverride(override, global)
	if _, err := mergeChatOverride(global, override, s.allowedModels, s.agents); err != nil {
		return err
	}
	next := cloneChatOverrides(s.chatOverrides)
	next[chatID] = override
	revision := s.revision + 1
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision, Override: s.override, ChatOverrides: next}); err != nil {
		return err
	}
	s.chatOverrides = next
	s.revision = revision
	return nil
}

// UpdateChat atomically applies a partial per-chat override change.
func (s *PreferenceStore) UpdateChat(chatID string, update func(ChatOverride) (ChatOverride, error)) (RuntimePreference, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return RuntimePreference{}, fmt.Errorf("config: empty chat id")
	}
	if update == nil {
		return RuntimePreference{}, fmt.Errorf("config: nil chat preference update")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, effective, err := s.updateChatLocked(chatID, update)
	return effective, err
}

// UpdateChatAtRevision rejects a stale full-form save. It returns the effective
// preference before and after the successful update for confirmation copy.
func (s *PreferenceStore) UpdateChatAtRevision(chatID string, expected uint64, update func(RuntimePreference, ChatOverride) (ChatOverride, error)) (RuntimePreference, RuntimePreference, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return RuntimePreference{}, RuntimePreference{}, fmt.Errorf("config: empty chat id")
	}
	if update == nil {
		return RuntimePreference{}, RuntimePreference{}, fmt.Errorf("config: nil chat preference update")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision != expected {
		return RuntimePreference{}, RuntimePreference{}, ErrPreferenceConflict
	}
	global := s.effectiveGlobalLocked()
	return s.updateChatLocked(chatID, func(current ChatOverride) (ChatOverride, error) {
		return update(global, current)
	})
}

func (s *PreferenceStore) updateChatLocked(chatID string, update func(ChatOverride) (ChatOverride, error)) (RuntimePreference, RuntimePreference, error) {
	global := s.effectiveGlobalLocked()
	previous := global
	if current, ok := s.chatOverrides[chatID]; ok && !current.IsEmpty() {
		if merged, err := mergeChatOverride(global, current, s.allowedModels, s.agents); err == nil {
			previous = merged
		}
	}
	override, err := update(s.chatOverrides[chatID])
	if err != nil {
		return RuntimePreference{}, RuntimePreference{}, err
	}
	override = normalizeChatOverride(override)
	if override.IsEmpty() {
		if err := s.resetChatLocked(chatID); err != nil {
			return RuntimePreference{}, RuntimePreference{}, err
		}
		return previous, global, nil
	}
	// 选定 home/bin 就隐含选定了 agent:一并固化,否则全局切 agent 后这条覆盖
	// 会变成非法组合(旧数据正是这样让启动崩掉的)。
	override = pinAgentForOverride(override, global)
	effective, err := mergeChatOverride(global, override, s.allowedModels, s.agents)
	if err != nil {
		return RuntimePreference{}, RuntimePreference{}, err
	}
	next := cloneChatOverrides(s.chatOverrides)
	next[chatID] = override
	revision := s.revision + 1
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision, Override: s.override, ChatOverrides: next}); err != nil {
		return RuntimePreference{}, RuntimePreference{}, err
	}
	s.chatOverrides = next
	s.revision = revision
	return previous, effective, nil
}

// ResetChat removes any override for a chat. Resetting an unset chat is a no-op
// that still bumps the revision for auditability.
func (s *PreferenceStore) ResetChat(chatID string) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return fmt.Errorf("config: empty chat id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resetChatLocked(chatID)
}

func (s *PreferenceStore) resetChatLocked(chatID string) error {
	if _, ok := s.chatOverrides[chatID]; !ok {
		return nil
	}
	next := cloneChatOverrides(s.chatOverrides)
	delete(next, chatID)
	if len(next) == 0 {
		next = nil
	}
	revision := s.revision + 1
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision, Override: s.override, ChatOverrides: next}); err != nil {
		return err
	}
	s.chatOverrides = next
	s.revision = revision
	return nil
}

func cloneChatOverrides(in map[string]ChatOverride) map[string]ChatOverride {
	out := make(map[string]ChatOverride, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
