package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

const PreferenceSchemaVersion = 1

// DefaultCompletionStatusText is the default terminal-card status label.
// The elapsed-time suffix remains owned by the card stream.
const DefaultCompletionStatusText = "✅ 已完成"

const maxCompletionStatusTextRunes = 32

var ErrPreferenceConflict = errors.New("config: preference revision conflict")

type ReplyMode string

type ConversationMode string

type GroupMessageMode string

// AppendOverflowMode determines how append-mode replies behave after a card
// reaches its display limit.
type AppendOverflowMode string

// TopicSeedMode 决定 topic 模式下"新话题的 session 起点"如何构造:
//   - Quote(默认):新话题从零起 session,把用户 @bot 的正文 + 引用消息(若有)
//     作为唯一 seed prompt。上下文短、不会继承主会话历史。
//   - Fork:走 Claude 的 --fork-session <root>,新话题从群主会话 fork 出一份,
//     继承主会话完整历史。上下文消耗大,但语义连续。
//
// 该字段仅在 ConversationMode == topic 时生效,chat 模式忽略。
type TopicSeedMode string

const (
	// ReplyModeCoder keeps the full ordered reply timeline. The stored value is
	// kept for backward compatibility with existing preferences, schedules, and env.
	ReplyModeCoder ReplyMode = "append"
	// ReplyModeWorker separates thought and tool timelines from the final answer.
	ReplyModeWorker ReplyMode = "append-clean-card"
	// ReplyModeSingleton keeps updating one card in the current scope.
	ReplyModeSingleton ReplyMode = "latest-card"

	// Deprecated storage-oriented aliases. Use Worker/Coder/Singleton in new
	// product code; these names remain so external integrations keep compiling.
	ReplyModeAppend          = ReplyModeCoder
	ReplyModeAppendCleanCard = ReplyModeWorker
	ReplyModeLatestCard      = ReplyModeSingleton

	ConversationModeChat  ConversationMode = "chat"
	ConversationModeTopic ConversationMode = "topic"

	GroupMessageModeMentionOnly        GroupMessageMode = "mention_only"
	GroupMessageModeParticipatedTopics GroupMessageMode = "participated_topics"
	GroupMessageModeAll                GroupMessageMode = "all_group_messages"

	AppendOverflowModeTruncate     AppendOverflowMode = "truncate"
	AppendOverflowModeContinueCard AppendOverflowMode = "continue-card"

	TopicSeedModeQuote TopicSeedMode = "quote"
	TopicSeedModeFork  TopicSeedMode = "fork"
)

// DisplayName is the product-facing reply mode name. Never expose the
// storage key as user-facing copy unless documenting a compatibility boundary.
func (m ReplyMode) DisplayName() string {
	switch m {
	case ReplyModeCoder:
		return "Coder"
	case ReplyModeWorker:
		return "Worker"
	case ReplyModeSingleton:
		return "Singleton"
	default:
		return string(m)
	}
}

func (m ReplyMode) Description() string {
	switch m {
	case ReplyModeCoder:
		return "按顺序展示回复与工具进展"
	case ReplyModeWorker:
		return "思考、正文、工具分区展示"
	case ReplyModeSingleton:
		return "维持单个卡片更新，搭配 pin 使用"
	default:
		return ""
	}
}

func (m ReplyMode) Label() string {
	if description := m.Description(); description != "" {
		return m.DisplayName() + "（" + description + "）"
	}
	return m.DisplayName()
}

var builtinModels = []string{"default", "sonnet", "opus", "haiku"}

var runtimeEfforts = []string{"default", "low", "medium", "high", "xhigh", "max"}

var validEfforts = func() map[string]struct{} {
	valid := make(map[string]struct{}, len(runtimeEfforts))
	for _, effort := range runtimeEfforts {
		valid[effort] = struct{}{}
	}
	return valid
}()

// RuntimeEfforts returns the ordered effort options accepted by runtime
// preferences. Callers receive a copy so the shared validation catalogue
// cannot be mutated by a form renderer.
func RuntimeEfforts() []string {
	return append([]string(nil), runtimeEfforts...)
}

// IsRuntimeEffort reports whether effort is accepted after the same trim and
// case normalization applied to persisted runtime preferences.
func IsRuntimeEffort(effort string) bool {
	_, ok := validEfforts[strings.ToLower(strings.TrimSpace(effort))]
	return ok
}

type RuntimePreference struct {
	Model              string             `json:"model"`
	Effort             string             `json:"effort"`
	ReplyMode          ReplyMode          `json:"reply_mode,omitempty"`
	ConversationMode   ConversationMode   `json:"conversation_mode,omitempty"`
	TopicSeedMode      TopicSeedMode      `json:"topic_seed_mode,omitempty"`
	GroupMessageMode   GroupMessageMode   `json:"group_message_mode,omitempty"`
	AppendOverflowMode AppendOverflowMode `json:"append_overflow_mode,omitempty"`
	RespondToBots      bool               `json:"respond_to_bots,omitempty"`
	// NotifyOnComplete, when true,补发一条 thread reply 文本消息 in the chat when a
	// run reaches the completed terminal state, so the 发起用户 gets a red-dot /
	// unread notification (the terminal card is an in-place CardKit update and
	// produces no new message). Defaults to false to avoid打扰.
	NotifyOnComplete bool `json:"notify_on_complete,omitempty"`
	// CompletionStatusText replaces the successful terminal-card label. Empty
	// means use DefaultCompletionStatusText, which keeps existing preferences
	// backward compatible.
	CompletionStatusText string `json:"completion_status_text,omitempty"`
	// ShowMetaRowAgent / ShowMetaRowRuntime / ShowMetaRowDeveloper 分别控制 AI 回复
	// 卡片底部三行运行时元信息的独立开关:
	//   agent 行:agent/会话/模型/tokens
	//   runtime 行:user/ip/workdir
	//   developer 行:当前版本 · 最新版本(若有)· 开发者模式 emoji
	// 三个都默认 false 隐藏,避免把运行时信息暴露到聊天;可在 /config 全局或
	// /local-config 按群独立开关。
	ShowMetaRowAgent     bool `json:"show_meta_row_agent,omitempty"`
	ShowMetaRowRuntime   bool `json:"show_meta_row_runtime,omitempty"`
	ShowMetaRowDeveloper bool `json:"show_meta_row_developer,omitempty"`
	// ShowMetaRows 是旧的"元信息行总开关",拆分为三个独立开关后仅用于**读侧迁移**:
	// 反序列化到 true 且三个新字段全 false 时,normalizeRuntimePreference 会把三个
	// 新字段(agent/runtime,不含 developer——旧版没有 developer 行)映射为 true 并
	// 清零 ShowMetaRows。写路径不再置 true,这样一次读+写后旧配置自然升级。
	ShowMetaRows bool `json:"show_meta_rows,omitempty"`
	// Agent is the selected agent kind (empty = "claude").
	Agent string `json:"agent,omitempty"`
	// AgentHome / AgentBin store the selected preset labels (not paths); they
	// are resolved to real paths against the agents catalogue at run time. An
	// empty label means "use the default", i.e. no config-dir env / default
	// executable name.
	AgentHome string `json:"agent_home,omitempty"`
	AgentBin  string `json:"agent_bin,omitempty"`
}

// DefaultAgentKind is the agent used when a preference leaves Agent empty.
const DefaultAgentKind = "claude"

type preferenceSnapshot struct {
	SchemaVersion int                     `json:"schema_version"`
	Revision      uint64                  `json:"revision"`
	Override      *RuntimePreference      `json:"override,omitempty"`
	ChatOverrides map[string]ChatOverride `json:"chat_overrides,omitempty"`
}

type PreferenceStore struct {
	mu            sync.RWMutex
	path          string
	defaults      RuntimePreference
	allowedModels []string
	agents        []AgentDef
	revision      uint64
	override      *RuntimePreference
	chatOverrides map[string]ChatOverride
	// recovery holds what had to be degraded at load time; adjustments holds the
	// repairs made by later global writes, until a caller takes them.
	recovery    PreferenceRecovery
	adjustments []ChatOverrideAdjustment
}

// PreferenceRecovery reports the degradations applied while loading the store.
// A stored preference that no longer validates — typically because agents.json
// changed under it — must not keep the bridge from starting, so the offending
// part is dropped in memory and reported here for the caller to log loudly.
//
// Nothing is rewritten to disk at load time on purpose: a transient agents.json
// read failure falls back to a minimal catalogue, and persisting the resulting
// drops would silently destroy the operator's per-chat configuration.
type PreferenceRecovery struct {
	// GlobalOverrideDropped means the stored global override was ignored and the
	// process-level defaults are in effect instead.
	GlobalOverrideDropped bool
	GlobalOverrideReason  string
	// ChatOverrides lists the per-chat overrides that were not loaded.
	ChatOverrides []ChatOverrideAdjustment
}

// Empty reports whether the store loaded without any degradation.
func (r PreferenceRecovery) Empty() bool {
	return !r.GlobalOverrideDropped && len(r.ChatOverrides) == 0
}

// Recovery returns the degradations applied when the store was loaded.
func (s *PreferenceStore) Recovery() PreferenceRecovery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.recovery
	out.ChatOverrides = append([]ChatOverrideAdjustment(nil), s.recovery.ChatOverrides...)
	return out
}

// TakeChatOverrideAdjustments returns and clears the per-chat repairs made by
// global preference writes since the last call, so a command handler can tell
// the operator that switching agent rewrote some per-chat overrides.
func (s *PreferenceStore) TakeChatOverrideAdjustments() []ChatOverrideAdjustment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.adjustments) == 0 {
		return nil
	}
	out := s.adjustments
	s.adjustments = nil
	return out
}

func (s *PreferenceStore) recordAdjustmentsLocked(adjustments []ChatOverrideAdjustment) {
	if len(adjustments) == 0 {
		return
	}
	s.adjustments = append(s.adjustments, adjustments...)
}

// OpenPreferenceStore opens (or lazily creates on first Set) the preference
// store. The optional agents argument supplies the agent catalogue used to
// validate the Agent/AgentHome/AgentBin fields; when omitted, agent-dimension
// validation is skipped so existing callers keep working unchanged.
func OpenPreferenceStore(path string, defaults RuntimePreference, allowedModels []string, agents ...AgentDef) (*PreferenceStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config: empty preference store path")
	}
	defaults = normalizeRuntimePreference(defaults)
	models, err := modelCatalog(allowedModels)
	if err != nil {
		return nil, err
	}
	if err := validateRuntimePreferenceWith(defaults, models, agents); err != nil {
		return nil, fmt.Errorf("validate runtime preference defaults: %w", err)
	}
	store := &PreferenceStore{path: path, defaults: defaults, allowedModels: models, agents: agents}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read preference store: %w", err)
	}
	var snapshot preferenceSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode preference store: %w", err)
	}
	if snapshot.SchemaVersion != PreferenceSchemaVersion {
		return nil, fmt.Errorf("unsupported preference schema %d", snapshot.SchemaVersion)
	}
	// A stored preference that no longer validates is degraded, never fatal:
	// refusing to start leaves the operator with a bot that is simply gone, and
	// the offending value can only be fixed through that same bot.
	if snapshot.Override != nil {
		preference := normalizeRuntimePreference(*snapshot.Override)
		if err := validateRuntimePreferenceWith(preference, models, agents); err != nil {
			store.recovery.GlobalOverrideDropped = true
			store.recovery.GlobalOverrideReason = err.Error()
		} else {
			store.override = &preference
		}
	}
	if len(snapshot.ChatOverrides) > 0 {
		loaded := make(map[string]ChatOverride, len(snapshot.ChatOverrides))
		for chatID, override := range snapshot.ChatOverrides {
			chatID = strings.TrimSpace(chatID)
			if chatID == "" {
				continue
			}
			override = normalizeChatOverride(override)
			// Validate that the stored override still merges into a legal
			// preference against the current defaults/catalogue. One bad chat
			// override only costs that chat its override, not the whole process.
			if _, err := mergeChatOverride(store.effectiveGlobalLocked(), override, models, agents); err != nil {
				store.recovery.ChatOverrides = append(store.recovery.ChatOverrides, ChatOverrideAdjustment{
					ChatID: chatID,
					Action: ChatOverrideDropped,
					Reason: err.Error(),
				})
				continue
			}
			loaded[chatID] = override
		}
		if len(loaded) > 0 {
			store.chatOverrides = loaded
		}
	}
	store.revision = snapshot.Revision
	return store, nil
}

func (s *PreferenceStore) Get() RuntimePreference {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.override != nil {
		return *s.override
	}
	return s.defaults
}

// Snapshot returns the effective global preference and the revision observed
// with it. The revision can later be used to reject stale full-form saves.
func (s *PreferenceStore) Snapshot() (RuntimePreference, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectiveGlobalLocked(), s.revision
}

func (s *PreferenceStore) Set(preference RuntimePreference) error {
	preference = normalizeRuntimePreference(preference)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateRuntimePreferenceWith(preference, s.allowedModels, s.agents); err != nil {
		return err
	}
	chatOverrides, adjustments := s.reconcileChatOverridesLocked(preference, s.effectiveGlobalLocked())
	revision := s.revision + 1
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision, Override: &preference, ChatOverrides: chatOverrides}); err != nil {
		return err
	}
	s.override = &preference
	s.chatOverrides = chatOverrides
	s.revision = revision
	s.recordAdjustmentsLocked(adjustments)
	return nil
}

// Update atomically applies a partial preference change. The callback runs
// while the store lock is held, so concurrent command handlers cannot overwrite
// fields written by another handler between a Get and Set pair.
func (s *PreferenceStore) Update(update func(RuntimePreference) (RuntimePreference, error)) (RuntimePreference, error) {
	if update == nil {
		return RuntimePreference{}, errors.New("config: nil preference update")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(update)
}

// UpdateAtRevision applies a full-form update only if no preference write has
// occurred since the form snapshot was rendered.
func (s *PreferenceStore) UpdateAtRevision(expected uint64, update func(RuntimePreference) (RuntimePreference, error)) (RuntimePreference, error) {
	if update == nil {
		return RuntimePreference{}, errors.New("config: nil preference update")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision != expected {
		return RuntimePreference{}, ErrPreferenceConflict
	}
	return s.updateLocked(update)
}

func (s *PreferenceStore) updateLocked(update func(RuntimePreference) (RuntimePreference, error)) (RuntimePreference, error) {
	preference, err := update(s.effectiveGlobalLocked())
	if err != nil {
		return RuntimePreference{}, err
	}
	preference = normalizeRuntimePreference(preference)
	if err := validateRuntimePreferenceWith(preference, s.allowedModels, s.agents); err != nil {
		return RuntimePreference{}, err
	}
	// 全局变更(尤其是切 agent)会让某些按群覆盖变成非法组合。写盘前一起收敛,
	// 否则它们以非法形态留在盘上,直到下次重启才暴露。
	chatOverrides, adjustments := s.reconcileChatOverridesLocked(preference, s.effectiveGlobalLocked())
	revision := s.revision + 1
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision, Override: &preference, ChatOverrides: chatOverrides}); err != nil {
		return RuntimePreference{}, err
	}
	s.override = &preference
	s.chatOverrides = chatOverrides
	s.revision = revision
	s.recordAdjustmentsLocked(adjustments)
	return preference, nil
}

func (s *PreferenceStore) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Reset only clears the global override; per-chat overrides are preserved,
	// but falling back to the defaults can invalidate them just like any other
	// global write, so they are reconciled too.
	chatOverrides, adjustments := s.reconcileChatOverridesLocked(s.defaults, s.effectiveGlobalLocked())
	revision := s.revision + 1
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision, ChatOverrides: chatOverrides}); err != nil {
		return err
	}
	s.override = nil
	s.chatOverrides = chatOverrides
	s.revision = revision
	s.recordAdjustmentsLocked(adjustments)
	return nil
}

func ValidateRuntimePreference(preference RuntimePreference, allowedModels ...string) error {
	models, err := modelCatalog(allowedModels)
	if err != nil {
		return err
	}
	return validateRuntimePreference(normalizeRuntimePreference(preference), models)
}

func validateRuntimePreference(preference RuntimePreference, allowedModels []string) error {
	return validateRuntimePreferenceWith(preference, allowedModels, nil)
}

func validateRuntimePreferenceWith(preference RuntimePreference, allowedModels []string, agents []AgentDef) error {
	allowed := false
	for _, model := range allowedModels {
		if preference.Model == model {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("model %q is not allowed", preference.Model)
	}
	if !IsRuntimeEffort(preference.Effort) {
		return fmt.Errorf("effort %q is not allowed", preference.Effort)
	}
	switch preference.ReplyMode {
	case ReplyModeWorker, ReplyModeCoder, ReplyModeSingleton:
	default:
		return fmt.Errorf("reply mode %q is not allowed", preference.ReplyMode)
	}
	switch preference.ConversationMode {
	case ConversationModeChat, ConversationModeTopic:
	default:
		return fmt.Errorf("conversation mode %q is not allowed", preference.ConversationMode)
	}
	switch preference.TopicSeedMode {
	case TopicSeedModeQuote, TopicSeedModeFork:
	default:
		return fmt.Errorf("topic seed mode %q is not allowed", preference.TopicSeedMode)
	}
	switch preference.GroupMessageMode {
	case GroupMessageModeMentionOnly, GroupMessageModeParticipatedTopics, GroupMessageModeAll:
	default:
		return fmt.Errorf("group message mode %q is not allowed", preference.GroupMessageMode)
	}
	switch preference.AppendOverflowMode {
	case AppendOverflowModeTruncate, AppendOverflowModeContinueCard:
	default:
		return fmt.Errorf("append overflow mode %q is not allowed", preference.AppendOverflowMode)
	}
	if err := validateAgentSelection(preference, agents); err != nil {
		return err
	}
	if text := strings.TrimSpace(preference.CompletionStatusText); text != "" {
		if strings.ContainsAny(text, "\r\n") {
			return errors.New("completion status text must be one line")
		}
		if utf8.RuneCountInString(text) > maxCompletionStatusTextRunes {
			return fmt.Errorf("completion status text exceeds %d characters", maxCompletionStatusTextRunes)
		}
	}
	return nil
}

// validateAgentSelection checks the Agent/AgentHome/AgentBin fields against the
// supplied catalogue. When agents is empty the agent dimension is not enforced
// (agent must still be the default kind so an out-of-band value cannot slip in).
func validateAgentSelection(preference RuntimePreference, agents []AgentDef) error {
	if len(agents) == 0 {
		if preference.Agent != DefaultAgentKind {
			return fmt.Errorf("agent %q is not allowed", preference.Agent)
		}
		return nil
	}
	catalogue := AgentsConfig{SchemaVersion: AgentsSchemaVersion, Agents: agents}
	if _, ok := catalogue.Find(preference.Agent); !ok {
		return fmt.Errorf("agent %q is not allowed", preference.Agent)
	}
	if _, ok := catalogue.HomePath(preference.Agent, preference.AgentHome); !ok {
		return fmt.Errorf("agent home %q is not allowed", preference.AgentHome)
	}
	if _, ok := catalogue.BinPath(preference.Agent, preference.AgentBin); !ok {
		return fmt.Errorf("agent bin %q is not allowed", preference.AgentBin)
	}
	return nil
}

func normalizeRuntimePreference(preference RuntimePreference) RuntimePreference {
	preference.Model = strings.TrimSpace(preference.Model)
	preference.Effort = strings.ToLower(strings.TrimSpace(preference.Effort))
	preference.ReplyMode = ReplyMode(strings.ToLower(strings.TrimSpace(string(preference.ReplyMode))))
	if preference.ReplyMode == "" {
		preference.ReplyMode = ReplyModeCoder
	}
	preference.ConversationMode = ConversationMode(strings.ToLower(strings.TrimSpace(string(preference.ConversationMode))))
	if preference.ConversationMode == "" {
		preference.ConversationMode = ConversationModeChat
	}
	preference.TopicSeedMode = TopicSeedMode(strings.ToLower(strings.TrimSpace(string(preference.TopicSeedMode))))
	if preference.TopicSeedMode == "" {
		preference.TopicSeedMode = TopicSeedModeQuote
	}
	preference.GroupMessageMode = GroupMessageMode(strings.ToLower(strings.TrimSpace(string(preference.GroupMessageMode))))
	if preference.GroupMessageMode == "" {
		preference.GroupMessageMode = GroupMessageModeMentionOnly
	}
	preference.AppendOverflowMode = AppendOverflowMode(strings.ToLower(strings.TrimSpace(string(preference.AppendOverflowMode))))
	if preference.AppendOverflowMode == "" {
		preference.AppendOverflowMode = AppendOverflowModeTruncate
	}
	preference.Agent = strings.ToLower(strings.TrimSpace(preference.Agent))
	if preference.Agent == "" {
		preference.Agent = DefaultAgentKind
	}
	preference.AgentHome = strings.TrimSpace(preference.AgentHome)
	preference.AgentBin = strings.TrimSpace(preference.AgentBin)
	preference.CompletionStatusText = strings.TrimSpace(preference.CompletionStatusText)
	for _, model := range builtinModels {
		if strings.EqualFold(preference.Model, model) {
			preference.Model = model
			break
		}
	}
	// 旧字段 ShowMetaRows 迁移为新的独立开关:旧值 true 且三个新开关全 false 时,
	// 说明这是从旧版本读上来的偏好——把 agent/runtime 行都打开(与旧版行为等价,
	// 旧版本没有 developer 行,保持不打开),然后清零旧字段。这样后续 Set() 落盘
	// 时新配置形态干净,旧字段自然消失。
	if preference.ShowMetaRows && !preference.ShowMetaRowAgent && !preference.ShowMetaRowRuntime && !preference.ShowMetaRowDeveloper {
		preference.ShowMetaRowAgent = true
		preference.ShowMetaRowRuntime = true
	}
	preference.ShowMetaRows = false
	return preference
}

// EffectiveCompletionStatusText returns the configured terminal-card label or
// the compatible default when a preference snapshot predates this field.
func EffectiveCompletionStatusText(text string) string {
	if text = strings.TrimSpace(text); text != "" {
		return text
	}
	return DefaultCompletionStatusText
}

func (p RuntimePreference) EffectiveCompletionStatusText() string {
	return EffectiveCompletionStatusText(p.CompletionStatusText)
}

func modelCatalog(additions []string) ([]string, error) {
	models := append([]string(nil), builtinModels...)
	seen := make(map[string]struct{}, len(models)+len(additions))
	for _, model := range models {
		seen[model] = struct{}{}
	}
	for _, addition := range additions {
		addition = strings.TrimSpace(addition)
		if !validModelName(addition) {
			return nil, fmt.Errorf("allowed model %q is invalid", addition)
		}
		if _, ok := seen[addition]; ok {
			continue
		}
		seen[addition] = struct{}{}
		models = append(models, addition)
	}
	return models, nil
}

func validModelName(model string) bool {
	if model == "" || len(model) > 128 {
		return false
	}
	for _, r := range model {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '-', '_', '.', ':', '/':
			continue
		default:
			return false
		}
	}
	return true
}

func savePreferenceSnapshot(path string, snapshot preferenceSnapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create preference directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode preference store: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".preferences-*.tmp")
	if err != nil {
		return fmt.Errorf("create preference temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set preference temp permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write preference store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync preference store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close preference store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace preference store: %w", err)
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}
