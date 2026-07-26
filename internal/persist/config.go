package persist

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
