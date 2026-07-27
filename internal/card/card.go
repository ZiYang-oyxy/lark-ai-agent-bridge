package card

import (
	"strings"
	"sync"

	"lark-agent-bridge/internal/security"
)

type SegmentKind string

const (
	SegmentText    SegmentKind = "text"
	SegmentThought SegmentKind = "thought"
	SegmentTool    SegmentKind = "tool"
	SegmentError   SegmentKind = "error"
)

type Segment struct {
	Kind SegmentKind
	Text string
	Tool *ToolMeta
}

type ToolMeta struct {
	ID      string
	Name    string
	Summary string
	Phase   string
	IsError bool
}

type ModelInfo struct {
	Requested string
	Actual    string
	Effort    string
}

type Meta struct {
	Agent       string
	SessionID   string
	Model       string
	Tokens      int
	RunTokens   int
	TotalTokens int
	CtxOK       bool
	CtxApprox   bool
	// CtxPending distinguishes a new run that has not exported its own context
	// usage yet from a run where context telemetry is unavailable.
	CtxPending     bool
	CtxUsedPercent int
	CtxTokens      int
	CtxWindow      int
	User           string
	IP             string
	WorkDir        string
	Status         string
	ModelInfo      ModelInfo
	// ModelPending means the running card has not observed this run's actual
	// model yet. It must not render a model inherited from a prior run.
	ModelPending bool
	// 三行元信息独立开关。任一为 true 才渲染对应行;全 false 时整段 meta 段
	// (含分隔线)不渲染,与旧的"总开关关闭"视觉一致。
	ShowMetaRowAgent     bool
	ShowMetaRowRuntime   bool
	ShowMetaRowDeveloper bool
	// Developer 行的三段数据。Version 为当前 bridge 版本(通常带 v 前缀,如
	// v0.1.8-rc.3),LatestVersion 只在有可用且不同于 current 的最新版本时非空
	// (读 update client 缓存,未命中不发请求),DeveloperMode 决定 emoji ✅/❌。
	Version       string
	LatestVersion string
	DeveloperMode bool
}

type StopButton struct {
	Visible         bool
	Disabled        bool
	GrantID         string
	ActionSessionID string // 停止按钮操作路由到的原始sessionID，分页场景下和e.SessionID（分页ID）不同
}

type Action struct {
	ID       string
	Label    string
	Value    string
	URL      string
	Disabled bool
	Confirm  *ActionConfirm
	GrantID  string
}

type ActionConfirm struct {
	Title string
	Text  string
}

// SelectOption is a value/display pair for a card dropdown. Value is what gets
// submitted/stored; Label is the richer text shown to the user (e.g. with a
// model description appended).
type SelectOption struct {
	Value string
	Label string
}

type ConfigForm struct {
	PreferenceRevision uint64
	Agent              string
	AgentHome          string
	AgentBin           string
	Model              string
	Effort             string
	ReplyMode          string
	AppendOverflowMode string
	ConversationMode   string
	TopicSeedMode      string
	GroupMessageMode   string
	RespondToBots      string
	NotifyOnComplete   string
	// 三个元信息行独立开关的表单值,均为字符串序列化的 "true"/"false"。
	ShowMetaRowAgent     string
	ShowMetaRowRuntime   string
	ShowMetaRowDeveloper string
	Agents               []SelectOption
	AgentHomes           []SelectOption
	AgentBins            []SelectOption
	Models               []string
	Efforts              []string
	ReplyModes           []string
	AppendOverflowModes  []SelectOption
	ConversationModes    []string
	TopicSeedModes       []string
	AllowedUsers         []string
	AllowedChats         []AccessChat
	Admins               []string
	OwnerState           string
	// ChatID, when non-empty, marks this form as a per-chat override editor
	// (the /local-config surface). It is carried back to the save callback as
	// the action value so the store knows which group to write. Empty means
	// the global /config editor.
	ChatID string
	// CurrentChatID is set only on the GLOBAL /config form when it was opened
	// inside a group chat. It is NOT the local-override marker (that is ChatID);
	// it only lets the global form render a "jump to this group's /local-config"
	// button. Empty in DMs or when unknown, so the button is simply omitted.
	CurrentChatID string
}

type AgentModeForm struct {
	Agent  string
	Agents []SelectOption
}

// LocalConfigItem is one row of the read-only /local-config overview: a labelled
// effective value plus whether this group overrides it (true) or inherits the
// global default (false).
type LocalConfigItem struct {
	Label      string
	Value      string
	Overridden bool // true=本群覆盖, false=继承全局
}

// LocalConfigOverview is the read-only /local-config summary card model: the
// group's effective per-field values with per-item override/inherit badges and
// a count of how many fields this group overrides.
type LocalConfigOverview struct {
	ChatID        string
	Items         []LocalConfigItem
	OverrideCount int
}

// HelpCard is the sectioned /help card model: a set of titled command groups
// plus a small footer note rendered under a divider. Buttons are attached by
// the renderer.
type HelpCard struct {
	Groups        []HelpGroup
	Footer        string
	ChatID        string
	VersionStatus *HelpVersionStatus
}

// HelpVersionStatus is the structured version/update state shown at the top
// of /help. Available updates carry their own details action so the renderer
// can keep the CTA next to the version comparison instead of at card bottom.
type HelpVersionStatus struct {
	CurrentVersion  string
	Status          string
	LatestVersion   string
	UpdateAvailable bool
	DetailsAction   Action
}

// StatusField is one labelled row in a /status section: a Chinese Label and its
// effective Value. Code marks whether the value should render as inline code
// (paths, ids, mode keys) rather than plain text.
type StatusField struct {
	Label string
	Value string
	Code  bool
}

// StatusSection is a titled group of status fields, rendered as one bordered
// section by the /status card renderer.
type StatusSection struct {
	Title  string
	Fields []StatusField
}

// StatusCard is the sectioned /status card model: an intro note plus a set of
// titled sections (会话概览 / 运行偏好 / 运行时). NotStarted flags the "no active
// session yet" state so the renderer can show a friendly hint instead of an
// empty 运行时 section. Buttons (refresh / open config) are attached by the
// renderer.
type StatusCard struct {
	Sections   []StatusSection
	NotStarted bool
}

// ResumeItem is one row of the /resume list: an ordered index, the agent
// session id used both as the display value and the resume callback value, a
// localised timestamp, an optional summary, and whether it is the currently
// active session in this chat/topic.
type ResumeItem struct {
	Index     int
	SessionID string
	UpdatedAt string
	Summary   string
	Current   bool
	GrantID   string
}

// ResumeCard is the /resume list card model: the catalog identity (agent +
// workdir, shown in the intro) and up to ten recent sessions, each rendered as
// a bordered row with a one-click 恢复 button. Empty Items renders a friendly
// "no resumable sessions" note.
type ResumeCard struct {
	Agent   string
	WorkDir string
	Items   []ResumeItem
}

// HelpGroup is one titled section of the /help card; each Line is a single
// markdown row inside the section body.
type HelpGroup struct {
	Title string
	Lines []string
}

type AccessChat struct {
	ID      string
	Name    string
	Mode    string
	Members []string
}

type Event struct {
	Type             string
	SessionID        string
	ReplyToMessageID string
	ReplyInThread    bool
	Segments         []Segment
	Meta             Meta
	StopButton       StopButton
	Actions          []Action
	Message          string
	HeaderTitle      string
	HeaderTemplate   string
	Streaming        bool
	// ForceFullUpdate is an internal transport hint for running-card heartbeats:
	// elapsed/header changes must reach CardKit even though native answer streaming
	// deliberately normalizes those fields in its static fingerprint.
	ForceFullUpdate bool
	Activity        string
	ThoughtExpanded bool
	ToolsExpanded   bool
	ProcessExpanded bool
	ToolCallCount   int
	// ThoughtRoundCount / ToolRoundCount 是本轮 run 累计的思考轮次 / 工具调用次数,
	// 用于三段布局(ThreeSectionLayout)下折叠区标题的累计计数。OmittedCount 把正文滚动
	// 窗口之外的数量显式传给渲染层，避免 Card 层复制窗口大小业务规则。
	ThoughtRoundCount   int
	ToolRoundCount      int
	ThoughtOmittedCount int
	ToolOmittedCount    int
	// ThreeSectionLayout 打开时(append-clean-card 运行/终态),普通分支改用三段结构:
	// 思考推理折叠区 → 正文流式 → 工具调用折叠区。
	ThreeSectionLayout   bool
	HideAgentPanels      bool
	OrderedLayout        bool
	InlineTimelineLayout bool
	MarkdownLayout       bool
	Markdown             string
	ConfigForm           *ConfigForm
	AgentModeForm        *AgentModeForm
	HelpCard             *HelpCard
	LocalConfigOverview  *LocalConfigOverview
	StatusCard           *StatusCard
	ResumeCard           *ResumeCard
	// VersionStatus 让 /config 与 /status 卡片顶部也能展示与 /help 一致的更新提示。
	// /help 走的是 HelpCard.VersionStatus（结构一致、渲染共用 buildHelpVersionElements）；
	// 顶层字段仅供其他卡片模型用，不与 HelpCard.VersionStatus 同时生效。
	VersionStatus *HelpVersionStatus
}

func WorkDirCreateActions(path string) []Action {
	return WorkDirActions(path, false)
}

func WorkDirActions(path string, disabled bool) []Action {
	return []Action{
		{ID: "create_workdir", Label: "创建目录", Value: path, Disabled: disabled},
		{ID: "cancel_workdir", Label: "取消", Value: path, Disabled: disabled},
	}
}

type Renderer interface {
	Render(Event) error
}

type LimitRenderer struct {
	Next     Renderer
	MaxChars int
}

func NewLimitRenderer(next Renderer, maxChars int) Renderer {
	if next == nil || maxChars <= 0 {
		return next
	}
	return &LimitRenderer{Next: next, MaxChars: maxChars}
}

func (r *LimitRenderer) Render(e Event) error {
	if r == nil || r.Next == nil {
		return nil
	}
	return r.Next.Render(LimitEvent(e, r.MaxChars))
}

type FakeRenderer struct {
	mu     sync.Mutex
	events []Event
}

func NewFakeRenderer() *FakeRenderer {
	return &FakeRenderer{}
}

func (r *FakeRenderer) Render(e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *FakeRenderer) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]Event, len(r.events))
	copy(cp, r.events)
	return cp
}

func SplitLongText(text string, maxChars int) []string {
	if maxChars <= 0 || len([]rune(text)) <= maxChars {
		return []string{text}
	}
	var pages []string
	for len([]rune(text)) > maxChars {
		cut := maxChars
		prefix := firstRunes(text, maxChars)
		if i := strings.LastIndex(prefix, "\n"); i > len(prefix)/2 {
			cut = len([]rune(prefix[:i+1]))
		}
		page, rest := splitRunes(text, cut)
		pages = append(pages, page)
		text = rest
	}
	if text != "" {
		pages = append(pages, text)
	}
	return pages
}

func SplitSegments(segments []Segment, maxChars int) [][]Segment {
	if maxChars <= 0 {
		return [][]Segment{segments}
	}
	var pages [][]Segment
	var current []Segment
	currentLen := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		pages = append(pages, current)
		current = nil
		currentLen = 0
	}
	for _, segment := range segments {
		if segment.Text == "" {
			continue
		}
		for _, part := range SplitLongText(segment.Text, maxChars) {
			partLen := len([]rune(part))
			if currentLen > 0 && currentLen+partLen > maxChars {
				flush()
			}
			current = append(current, Segment{Kind: segment.Kind, Text: part})
			currentLen += partLen
		}
	}
	flush()
	if len(pages) == 0 {
		return [][]Segment{segments}
	}
	return pages
}

func LimitEvent(e Event, maxChars int) Event {
	for i := range e.Segments {
		e.Segments[i].Text = limitText(e.Segments[i].Text, maxChars)
	}
	e.Message = limitText(e.Message, maxChars)
	return e
}

func limitText(text string, maxChars int) string {
	text = security.Redact(text)
	if maxChars <= 0 || len([]rune(text)) <= maxChars {
		return text
	}
	const suffix = "\n\n[truncated]"
	runes := []rune(text)
	suffixRunes := []rune(suffix)
	if maxChars <= len(suffixRunes) {
		return string(runes[:maxChars])
	}
	return string(runes[:maxChars-len(suffixRunes)]) + suffix
}

func firstRunes(text string, n int) string {
	page, _ := splitRunes(text, n)
	return page
}

func splitRunes(text string, n int) (string, string) {
	runes := []rune(text)
	if n >= len(runes) {
		return text, ""
	}
	return string(runes[:n]), string(runes[n:])
}
