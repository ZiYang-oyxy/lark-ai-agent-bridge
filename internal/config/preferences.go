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
)

const PreferenceSchemaVersion = 1

type ReplyMode string

type ConversationMode string

type GroupMessageMode string

const (
	ReplyModeAppend          ReplyMode = "append"
	ReplyModeAppendCleanCard ReplyMode = "append-clean-card"
	ReplyModeLatestCard      ReplyMode = "latest-card"

	ConversationModeChat  ConversationMode = "chat"
	ConversationModeTopic ConversationMode = "topic"

	GroupMessageModeMentionOnly        GroupMessageMode = "mention_only"
	GroupMessageModeParticipatedTopics GroupMessageMode = "participated_topics"
	GroupMessageModeAll                GroupMessageMode = "all_group_messages"
)

var builtinModels = []string{"default", "sonnet", "opus", "haiku"}

var validEfforts = map[string]struct{}{
	"default": {},
	"low":     {},
	"medium":  {},
	"high":    {},
}

type RuntimePreference struct {
	Model            string           `json:"model"`
	Effort           string           `json:"effort"`
	ReplyMode        ReplyMode        `json:"reply_mode,omitempty"`
	ConversationMode ConversationMode `json:"conversation_mode,omitempty"`
	GroupMessageMode GroupMessageMode `json:"group_message_mode,omitempty"`
	RespondToBots    bool             `json:"respond_to_bots,omitempty"`
	// NotifyOnComplete, when true,补发一条 thread reply 文本消息 in the chat when a
	// run reaches the completed terminal state, so the 发起用户 gets a red-dot /
	// unread notification (the terminal card is an in-place CardKit update and
	// produces no new message). Defaults to false to avoid打扰.
	NotifyOnComplete bool `json:"notify_on_complete,omitempty"`
	// ShowMetaRows, when true, 在 AI 回复卡片底部渲染两行运行时元信息
	// (agent/会话/模型/tokens 与 user/ip/workdir)。默认 false 隐藏,避免把
	// 这些信息暴露到聊天;可在 /config 全局或 /local-config 按群开启。
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
	if snapshot.Override != nil {
		preference := normalizeRuntimePreference(*snapshot.Override)
		if err := validateRuntimePreferenceWith(preference, models, agents); err != nil {
			return nil, fmt.Errorf("validate stored runtime preference: %w", err)
		}
		store.override = &preference
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
			// preference against the current defaults/catalogue.
			if _, err := mergeChatOverride(store.effectiveGlobalLocked(), override, models, agents); err != nil {
				return nil, fmt.Errorf("validate stored chat override %q: %w", chatID, err)
			}
			loaded[chatID] = override
		}
		store.chatOverrides = loaded
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

func (s *PreferenceStore) Set(preference RuntimePreference) error {
	preference = normalizeRuntimePreference(preference)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateRuntimePreferenceWith(preference, s.allowedModels, s.agents); err != nil {
		return err
	}
	revision := s.revision + 1
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision, Override: &preference, ChatOverrides: s.chatOverrides}); err != nil {
		return err
	}
	s.override = &preference
	s.revision = revision
	return nil
}

func (s *PreferenceStore) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision := s.revision + 1
	// Reset only clears the global override; per-chat overrides are preserved.
	if err := savePreferenceSnapshot(s.path, preferenceSnapshot{SchemaVersion: PreferenceSchemaVersion, Revision: revision, ChatOverrides: s.chatOverrides}); err != nil {
		return err
	}
	s.override = nil
	s.revision = revision
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
	if _, ok := validEfforts[preference.Effort]; !ok {
		return fmt.Errorf("effort %q is not allowed", preference.Effort)
	}
	switch preference.ReplyMode {
	case ReplyModeAppend, ReplyModeAppendCleanCard, ReplyModeLatestCard:
	default:
		return fmt.Errorf("reply mode %q is not allowed", preference.ReplyMode)
	}
	switch preference.ConversationMode {
	case ConversationModeChat, ConversationModeTopic:
	default:
		return fmt.Errorf("conversation mode %q is not allowed", preference.ConversationMode)
	}
	switch preference.GroupMessageMode {
	case GroupMessageModeMentionOnly, GroupMessageModeParticipatedTopics, GroupMessageModeAll:
	default:
		return fmt.Errorf("group message mode %q is not allowed", preference.GroupMessageMode)
	}
	if err := validateAgentSelection(preference, agents); err != nil {
		return err
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
		preference.ReplyMode = ReplyModeAppend
	}
	preference.ConversationMode = ConversationMode(strings.ToLower(strings.TrimSpace(string(preference.ConversationMode))))
	if preference.ConversationMode == "" {
		preference.ConversationMode = ConversationModeChat
	}
	preference.GroupMessageMode = GroupMessageMode(strings.ToLower(strings.TrimSpace(string(preference.GroupMessageMode))))
	if preference.GroupMessageMode == "" {
		preference.GroupMessageMode = GroupMessageModeMentionOnly
	}
	preference.Agent = strings.ToLower(strings.TrimSpace(preference.Agent))
	if preference.Agent == "" {
		preference.Agent = DefaultAgentKind
	}
	preference.AgentHome = strings.TrimSpace(preference.AgentHome)
	preference.AgentBin = strings.TrimSpace(preference.AgentBin)
	for _, model := range builtinModels {
		if strings.EqualFold(preference.Model, model) {
			preference.Model = model
			break
		}
	}
	return preference
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
