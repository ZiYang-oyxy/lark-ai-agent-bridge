package testfw

import "encoding/json"

// TestCase 代表一个完整的测试用例
type TestCase struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags"`
	Steps       []Step   `yaml:"steps"`
}

// Step 代表测试用例中的一个步骤。
//
// 两种互斥的触发方式:
//   - input: 发送一条消息文本(走 simulate),模拟用户发消息。
//   - action: 触发一个卡片动作 id(走 simulate-action),等价于点击卡片按钮;
//     value 为动作参数,primeText 可选,用于在触发前先发一条消息建立会话。
//
// 二者只应填其一;同时填时以 action 优先。
type Step struct {
	Input     string   `yaml:"input,omitempty"`
	Action    string   `yaml:"action,omitempty"`     // 卡片动作 id,如 stop / update.details
	Value     string   `yaml:"value,omitempty"`      // 动作参数(action 模式)
	PrimeText string   `yaml:"prime_text,omitempty"` // action 模式:触发前先发的建会话消息
	Group     bool     `yaml:"group,omitempty"`      // 是否模拟群聊场景(仅 input 模式)
	Asserts   []Assert `yaml:"asserts"`
}

// Assert 代表一个断言
type Assert struct {
	Type     string `yaml:"type"`
	Expected string `yaml:"expected,omitempty"`
	Text     string `yaml:"text,omitempty"`
	Button   string `yaml:"button,omitempty"`
}

// 以下结构镜像 bridge 真实输出。card.Event / audit.Event 均无 json tag,
// 序列化 key 直接等于 Go 字段名(首字母大写),故这里的 json tag 全部大写对齐。
// 只声明断言实际会读到的字段,其余字段忽略即可(json.Unmarshal 容忍多余字段)。

// SimulateOutput 是 simulate / simulate-action 的顶层输出,
// 顶层结构带小写 json tag(events/audit),见 cmd/lark-agent-bridge/main.go。
type SimulateOutput struct {
	Events []Event `json:"events"`
	Audit  []Audit `json:"audit"`
}

// Event 镜像 card.Event(internal/card/card.go)。
type Event struct {
	Type                string               `json:"Type"`
	HeaderTitle         string               `json:"HeaderTitle"`
	Segments            []Segment            `json:"Segments"`
	Actions             []Action             `json:"Actions"`
	ReplyToMessageID    string               `json:"ReplyToMessageID"`
	StopButton          StopButton           `json:"StopButton"`
	Message             string               `json:"Message"`
	Markdown            string               `json:"Markdown"`
	HelpCard            *HelpCard            `json:"HelpCard"`
	StatusCard          *StatusCard          `json:"StatusCard"`
	ConfigForm          *ConfigForm          `json:"ConfigForm"`
	LocalConfigOverview *LocalConfigOverview `json:"LocalConfigOverview"`
	AgentModeForm       *AgentModeForm       `json:"AgentModeForm"`
}

// Segment 镜像 card.Segment。Kind 取值:text / thought / tool / error。
type Segment struct {
	Kind string `json:"Kind"`
	Text string `json:"Text"`
}

// Action 镜像 card.Action。
type Action struct {
	ID       string `json:"ID"`
	Label    string `json:"Label"`
	Value    string `json:"Value"`
	URL      string `json:"URL"`
	Disabled bool   `json:"Disabled"`
}

// StopButton 镜像 card.StopButton。
type StopButton struct {
	Visible  bool `json:"Visible"`
	Disabled bool `json:"Disabled"`
}

// HelpCard 镜像 card.HelpCard。
type HelpCard struct {
	Groups []HelpGroup `json:"Groups"`
	Footer string      `json:"Footer"`
}

// HelpGroup 镜像 card.HelpGroup。
type HelpGroup struct {
	Title string   `json:"Title"`
	Lines []string `json:"Lines"`
}

// StatusCard 镜像 card.StatusCard。
type StatusCard struct {
	Sections   []StatusSection `json:"Sections"`
	NotStarted bool            `json:"NotStarted"`
}

// StatusSection 镜像 card.StatusSection。
type StatusSection struct {
	Title  string        `json:"Title"`
	Fields []StatusField `json:"Fields"`
}

// StatusField 镜像 card.StatusField。
type StatusField struct {
	Label string `json:"Label"`
	Value string `json:"Value"`
	Code  bool   `json:"Code"`
}

// SelectOption 镜像 card.SelectOption。
type SelectOption struct {
	Value string `json:"Value"`
	Label string `json:"Label"`
}

// ConfigForm 镜像 card.ConfigForm。只声明断言会用到的展示字段;
// 真实结构含 30+ 字段,此处按需取用,其余忽略。
type ConfigForm struct {
	Agent     string         `json:"Agent"`
	Model     string         `json:"Model"`
	Effort    string         `json:"Effort"`
	ReplyMode string         `json:"ReplyMode"`
	Agents    []SelectOption `json:"Agents"`
}

// AgentModeForm 镜像 card.AgentModeForm。
type AgentModeForm struct {
	Agent  string         `json:"Agent"`
	Agents []SelectOption `json:"Agents"`
}

// LocalConfigOverview 镜像 card.LocalConfigOverview。
type LocalConfigOverview struct {
	ChatID        string            `json:"ChatID"`
	Items         []LocalConfigItem `json:"Items"`
	OverrideCount int               `json:"OverrideCount"`
}

// LocalConfigItem 镜像 card.LocalConfigItem。
type LocalConfigItem struct {
	Label      string `json:"Label"`
	Value      string `json:"Value"`
	Overridden bool   `json:"Overridden"`
}

// Audit 镜像 audit.Event(internal/audit/audit.go)。
// 注意:真实结构没有 Level 字段;错误通过 Action 名的后缀约定表达。
// 具体哪些后缀算错误见 assert.go 的 errorActionSuffixes(刻意只含
// _failed / _denied / _rejected,排除 _ignored / _unavailable 等正常语义)。
type Audit struct {
	Time      string `json:"Time"`
	Actor     string `json:"Actor"`
	Action    string `json:"Action"`
	SessionID string `json:"SessionID"`
	Detail    string `json:"Detail"`
}

// TestResult 测试结果
type TestResult struct {
	Case    TestCase
	Passed  bool
	Failed  []FailedAssert
	Error   error
	Outputs []SimulateOutput
}

// FailedAssert 失败的断言
type FailedAssert struct {
	Step      int
	StepInput string
	Assert    Assert
	Message   string
}

func ParseSimulateOutput(data []byte) (*SimulateOutput, error) {
	var out SimulateOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
