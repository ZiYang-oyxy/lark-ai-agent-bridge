package persist

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// ConfigSchemaVersion 是配置磁盘文档当前版本。
const ConfigSchemaVersion = 1

// ConfigFileName 是配置文件在 engine 目录内的文件名。
const ConfigFileName = "config.json"

// Config 是 bridge 的强类型配置，子结构分组。
//
// 字段清单在 Task 3 按现状 internal/config 迁移完整；本骨架先给出代表性分组，
// 使分层合并（layers.go）与 env 注入（env.go）可通用实现。
//
// 分层合并逻辑（mergePatches/resolve）用反射遍历，扩字段时无需改动合并代码。
type Config struct {
	Lark     LarkConfig
	Agent    AgentConfig
	Behavior BehaviorConfig
}

// LarkConfig 飞书凭据（secret）+ bot 身份。
type LarkConfig struct {
	AppID     string `json:"-" secret:"true" env:"LAB_LARK_APP_ID"`
	AppSecret string `json:"-" secret:"true" env:"LAB_LARK_APP_SECRET"`
	BotOpenID string `json:"bot_open_id,omitempty"`
}

// AgentConfig agent / model / effort 等。
type AgentConfig struct {
	Model  string `json:"model,omitempty" env:"LAB_AGENT_MODEL"`
	Effort string `json:"effort,omitempty" env:"LAB_AGENT_EFFORT"`
}

// BehaviorConfig 各类行为开关与阈值。
type BehaviorConfig struct {
	ReplyEnabled bool `json:"reply_enabled"`
	MaxTurns     int  `json:"max_turns,omitempty"`
}

// ---- 可选镜像（全指针）：区分“未设(nil)”与“设成零值” ----

// configPatch 是 Config 的可选镜像。字段对应 Config，子结构用各自 patch 类型（指针），
// 叶子字段用指针。nil 表示“未设”，非 nil 表示“显式设置”（哪怕是零值）。
type configPatch struct {
	Lark     *larkPatch     `json:"lark,omitempty"`
	Agent    *agentPatch    `json:"agent,omitempty"`
	Behavior *behaviorPatch `json:"behavior,omitempty"`
}

type larkPatch struct {
	// AppID/AppSecret 为 secret：不序列化、不从文件加载，只经 env 注入（Task 4）。
	AppID     *string `json:"-" secret:"true" env:"LAB_LARK_APP_ID"`
	AppSecret *string `json:"-" secret:"true" env:"LAB_LARK_APP_SECRET"`
	BotOpenID *string `json:"bot_open_id,omitempty"`
}

type agentPatch struct {
	Model  *string `json:"model,omitempty" env:"LAB_AGENT_MODEL"`
	Effort *string `json:"effort,omitempty" env:"LAB_AGENT_EFFORT"`
}

type behaviorPatch struct {
	ReplyEnabled *bool `json:"reply_enabled,omitempty" env:"LAB_BEHAVIOR_REPLY_ENABLED"`
	MaxTurns     *int  `json:"max_turns,omitempty" env:"LAB_BEHAVIOR_MAX_TURNS"`
}

// Defaults 是唯一一处内置默认。Task 3 按现状补全全部字段默认值。
func Defaults() Config {
	return Config{
		Agent: AgentConfig{
			Model:  "claude-sonnet-5",
			Effort: "medium",
		},
		Behavior: BehaviorConfig{
			ReplyEnabled: true,
			MaxTurns:     20,
		},
	}
}

// validEfforts 是 agent effort 的合法枚举（对齐 bridge 现状）。
var validEfforts = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// Validate 是唯一一处校验：枚举/正整数/必填等。返回字段级错误。
func (c Config) Validate() error {
	if strings.TrimSpace(c.Agent.Model) == "" {
		return fmt.Errorf("persist: agent.model must not be empty")
	}
	if !validEfforts[c.Agent.Effort] {
		return fmt.Errorf("persist: agent.effort %q invalid", c.Agent.Effort)
	}
	if c.Behavior.MaxTurns < 1 {
		return fmt.Errorf("persist: behavior.max_turns must be >= 1, got %d", c.Behavior.MaxTurns)
	}
	return nil
}

// configDoc 是配置的磁盘分区段文档：存的是各层原始 patch，不是合并后的最终值。
//
// secret 字段（larkPatch 里 json:"-"）在任何区段都不会序列化、不会从文件加载，
// 只能由 env.go 注入。
type configDoc struct {
	SchemaVersion   int                     `json:"schema_version"`
	Base            *configPatch            `json:"base,omitempty"`
	RuntimeOverride *configPatch            `json:"runtime_override,omitempty"`
	ChatOverrides   map[string]*configPatch `json:"chat_overrides,omitempty"`
}

// readConfigDoc 读配置文档；文件不存在返回空文档（当前版本、无区段）。
func readConfigDoc(e *Engine) (configDoc, error) {
	f := e.open(ConfigFileName)
	raw, exists, err := f.readRaw()
	if err != nil {
		return configDoc{}, err
	}
	if !exists {
		return configDoc{SchemaVersion: ConfigSchemaVersion}, nil
	}
	var doc configDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return configDoc{}, fmt.Errorf("persist: decode %s: %w", ConfigFileName, err)
	}
	if doc.SchemaVersion > ConfigSchemaVersion {
		return configDoc{}, fmt.Errorf("persist: %s schema %d newer than supported %d", ConfigFileName, doc.SchemaVersion, ConfigSchemaVersion)
	}
	return doc, nil
}

// writeConfigDoc 同步 durable 写配置文档，权限 0600（配置含 secret 相邻数据）。
func writeConfigDoc(e *Engine, doc configDoc) error {
	f := e.open(ConfigFileName)
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("persist: encode %s: %w", ConfigFileName, err)
	}
	data = append(data, '\n')
	return f.writeAtomic(data, 0o600)
}

// Manager 持有配置文件句柄 + 当前生效值的原子快照。
//
// - Current() / ForChat() 走 atomic.Pointer[Config]，运行中读取无锁、看到一致快照。
// - SetRuntimeOverride / SetChatOverride 在 writeMu 内做“读文档 → 改 override 区段 →
//   校验 → 写回 → 发布快照”，保证磁盘与内存一致，且校验失败不影响旧快照。
type Manager struct {
	engine  *Engine
	lookup  func(string) (string, bool) // env 查询（默认 os.LookupEnv，可注入以便测试）
	writeMu sync.Mutex                  // 串行化写入侧
	current atomic.Pointer[Config]      // 全局生效值快照
	chats   atomic.Pointer[map[string]Config]
}

// LoadConfig 读 configDoc → envPatch → resolve(Defaults, merge(base,runtime,env)) →
// Validate → 发布快照。失败（校验不过 / 文件损坏）返回 error，调用方 fail-fast。
func LoadConfig(e *Engine) (*Manager, error) {
	return loadConfigWith(e, os.LookupEnv)
}

// loadConfigWith 是 LoadConfig 的可注入 env 版本，供测试用。
func loadConfigWith(e *Engine, lookup func(string) (string, bool)) (*Manager, error) {
	if e == nil {
		return nil, errors.New("persist: nil engine")
	}
	m := &Manager{engine: e, lookup: lookup}
	doc, err := readConfigDoc(e)
	if err != nil {
		return nil, err
	}
	if err := m.republish(doc); err != nil {
		return nil, err
	}
	return m, nil
}

// republish 由当前 doc 与 env 计算新快照并发布。校验失败返回 error，不改动已有快照。
// 调用方必须保证 doc 语义合法（磁盘落盘的 override 区段是已 Validate 过的）。
func (m *Manager) republish(doc configDoc) error {
	env := envPatch(m.lookup)
	merged := mergePatches(doc.Base, doc.RuntimeOverride, env)
	cfg := resolve(Defaults(), merged)
	if err := cfg.Validate(); err != nil {
		return err
	}
	// chat overrides：每个 chat 单独合并（base + runtime + chat + env）。
	chatSnap := make(map[string]Config, len(doc.ChatOverrides))
	for id, cp := range doc.ChatOverrides {
		cm := mergePatches(doc.Base, doc.RuntimeOverride, cp, env)
		ccfg := resolve(Defaults(), cm)
		if err := ccfg.Validate(); err != nil {
			return fmt.Errorf("persist: chat_override[%s] invalid: %w", id, err)
		}
		chatSnap[id] = ccfg
	}
	m.current.Store(&cfg)
	m.chats.Store(&chatSnap)
	return nil
}

// Current 返回全局生效配置的原子快照（无锁读）。
func (m *Manager) Current() Config {
	p := m.current.Load()
	if p == nil {
		return Defaults()
	}
	return *p
}

// ForChat 返回叠加了 chat_override 后的生效值；未配置 chat_override 时等价 Current()。
func (m *Manager) ForChat(chatID string) Config {
	cp := m.chats.Load()
	if cp != nil {
		if v, ok := (*cp)[chatID]; ok {
			return v
		}
	}
	return m.Current()
}

// SetRuntimeOverride 改全局 runtime_override 区段 → 校验 → 写回（只碰该区段）→
// 重算 → 原子切换。校验失败：拒绝切换、保留旧值、返回 error。/config 卡片走这里。
func (m *Manager) SetRuntimeOverride(patch *configPatch) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	doc, err := readConfigDoc(m.engine)
	if err != nil {
		return err
	}
	prev := doc.RuntimeOverride
	doc.RuntimeOverride = patch
	if err := m.republish(doc); err != nil {
		// 回滚 doc 状态（虽然 doc 是本地值，republish 失败前也未落盘）。
		doc.RuntimeOverride = prev
		return err
	}
	// republish 通过 → 落盘（也走同步 durable 写）。
	if err := writeConfigDoc(m.engine, doc); err != nil {
		// 落盘失败：把内存快照也回滚到旧 doc。
		doc.RuntimeOverride = prev
		_ = m.republish(doc)
		return err
	}
	return nil
}

// SetChatOverride 改指定 chat 的 override 区段；空 patch 视为“清除该 chat 的 override”。
func (m *Manager) SetChatOverride(chatID string, patch *configPatch) error {
	if strings.TrimSpace(chatID) == "" {
		return errors.New("persist: empty chat id")
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	doc, err := readConfigDoc(m.engine)
	if err != nil {
		return err
	}
	prev := doc.ChatOverrides
	if doc.ChatOverrides == nil {
		doc.ChatOverrides = map[string]*configPatch{}
	} else {
		// 拷贝，避免直接改到已发布快照 map 关联。
		next := make(map[string]*configPatch, len(doc.ChatOverrides)+1)
		for k, v := range doc.ChatOverrides {
			next[k] = v
		}
		doc.ChatOverrides = next
	}
	if patch == nil {
		delete(doc.ChatOverrides, chatID)
	} else {
		doc.ChatOverrides[chatID] = patch
	}
	if err := m.republish(doc); err != nil {
		doc.ChatOverrides = prev
		return err
	}
	if err := writeConfigDoc(m.engine, doc); err != nil {
		doc.ChatOverrides = prev
		_ = m.republish(doc)
		return err
	}
	return nil
}
