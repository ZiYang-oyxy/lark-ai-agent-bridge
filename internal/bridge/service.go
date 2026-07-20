package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/bridgeinstructions"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/media"
	"lark-agent-bridge/internal/reply"
	"lark-agent-bridge/internal/session"
)

const recoveryNoticesTimeout = 5 * time.Second

type Service struct {
	Config           config.Config
	Sessions         *session.Manager
	Cards            card.Renderer
	Runner           AgentRunner
	Audit            *audit.Recorder
	MediaCache       mediaResolver
	MediaDownloader  media.Downloader
	MediaGC          mediaSweeper
	Preferences      *config.PreferenceStore
	Agents           config.AgentsConfig
	Replies          *reply.Store
	CardTarget       reply.CardTarget
	Reactions        feishu.ReactionSink
	OutputImages     feishu.ImageSender
	SequenceResolver session.RenderRefSequenceResolver
	RestoreNotices   []session.RecoveryNotice
	Access           *access.Store
	AccessControls   *access.RuntimeControls
	AccessInfo       interface {
		GetOwner(context.Context, string) (string, error)
		ListChats(context.Context) ([]feishu.KnownChat, error)
	}
	AccessAppID        string
	BotOpenID          string
	TopicParticipation TopicParticipation
	ScopeInspector     ScopeInspector
	ScopeGrants        feishu.ScopeGrantProvider
	accessMu           sync.RWMutex
	knownChats         []feishu.KnownChat

	mu                 sync.Mutex
	pendingRuns        map[string]pendingRun
	activeRuns         map[string]activeRun
	pendingCompletions map[string]pendingCompletion
	waitingReactions   map[string]*reactionLifecycle
	reactionDelay      time.Duration
	startedAt          time.Time
	accepting          bool
	loopsCancel        context.CancelFunc
	loopsStarted       bool
	dispatchWG         sync.WaitGroup
	runWG              sync.WaitGroup
	scopeMu            sync.Mutex
	scopeContext       context.Context
	scopeCancel        context.CancelFunc
	activeScopeCancel  context.CancelFunc
	scopeWG            sync.WaitGroup

	// Test seam: called after an active starting batch is registered and before
	// the first cancellation check/card render.
	afterStoreActiveRunHook          func()
	beforePendingCompletionRetryHook func()
}

// mediaResolver keeps attachment resolution testable without coupling the
// service to a concrete cache implementation.
type mediaResolver interface {
	Resolve(context.Context, media.Downloader, []media.Ref) media.Resolution
}

type TopicParticipation interface {
	Has(string, string) bool
	Mark(string, string, time.Time) error
	Touch(string, string, time.Time) error
}

type mediaSweeper interface {
	Sweep(map[string]struct{}) (media.SweepResult, error)
	SweepStartup(map[string]struct{}) (media.SweepResult, error)
}

type AgentRunner interface {
	Run(context.Context, AgentRunRequest) (AgentRunResult, error)
}

type AgentRunRequest struct {
	Kind agent.Kind
	Bin  string
	// ClaudeBin is retained for source compatibility with existing runners and
	// tests. New callers populate Bin; CLIExecRunner falls back to ClaudeBin.
	ClaudeBin                 string
	Prompt                    string
	WorkDir                   string
	AgentSessionID            string
	Model                     string
	Effort                    string
	Images                    []string
	BridgeInstructionsVersion string
	// Home is the resolved agent home / config directory. Empty means default.
	Home    string
	OnEvent func(AgentStreamUpdate)
}

type AgentRunResult struct {
	Segments        []card.Segment
	OrderedSegments []card.Segment
	Model           string
	Tokens          int
	AgentSessionID  string
	// AnswerSegments 保存本次 run 内每个 assistant text message 的正文,按出现顺序排列。
	// 供终态"只保留最后一段回复"裁剪使用;Segments 仍是聚合结果,兼容其他消费方。
	AnswerSegments []string
	// ToolCallCount 是本次 run 内唯一 tool_use.id 的数量,用于卡片过程区标题的稳定计数。
	ToolCallCount int
	// ProtocolUnknown/ProtocolAnomalies expose Codex JSONL drift for audit.
	ProtocolUnknown   int
	ProtocolAnomalies int
}

type AgentStreamUpdate struct {
	Segments       []card.Segment
	Model          string
	Tokens         int
	AgentSessionID string
	Activity       string
	// Incremental marks content_block_delta payloads whose text must be
	// concatenated byte-for-byte with the preceding delta.
	Incremental bool
	// AnswerSnapshot marks a complete assistant message. It replaces any
	// partial answer accumulated for that message instead of duplicating it.
	AnswerSnapshot bool
	// AssistantSnapshot marks the boundary of any complete assistant message,
	// including tool-only messages that do not replace the answer builder.
	AssistantSnapshot bool
	// PartialMessage marks content_block/stream_event updates emitted before
	// the corresponding complete assistant message snapshot.
	PartialMessage bool
}

type pendingRun struct {
	SessionID        string
	RunCardSessionID string
	Command          Command
	Message          Message
	WorkDir          string
	Preference       config.RuntimePreference
	ExpiresAt        time.Time
}

type activeRun struct {
	BaseSessionID    string
	BatchID          string
	SourceMessageIDs []string
	Key              session.Key
	WorkDir          string
	Cancel           context.CancelFunc
	Stream           *agentCardStream
	Typing           *reactionLifecycle
}

type pendingCompletion struct {
	Key         session.Key
	BatchID     string
	Completion  session.BatchCompletion
	Attempts    int
	NextRetryAt time.Time
	Retrying    bool
}

type ActionRequest struct {
	SessionID  string
	ActionID   string
	Value      string
	Actor      string
	FormValues map[string]string
}

type ActionResult struct {
	Event *card.Event
}

func (r ActionResult) PrepareCard(maxChars int) (card.PreparedLarkCard, error) {
	if r.Event == nil {
		return card.PreparedLarkCard{}, nil
	}
	event := *r.Event
	if maxChars > 0 {
		event = card.LimitEvent(event, maxChars)
	}
	return card.PrepareLarkCard(event)
}

func NewService(cfg config.Config, renderer card.Renderer, runner AgentRunner, recorder *audit.Recorder) *Service {
	return NewServiceWithSessions(cfg, renderer, runner, recorder, session.NewManager(), nil)
}

// NewServiceWithSessions attaches the durable session manager that was restored
// by the caller. It records recovery notices but never renders recovery cards.
func NewServiceWithSessions(cfg config.Config, renderer card.Renderer, runner AgentRunner, recorder *audit.Recorder, sessions *session.Manager, notices []session.RecoveryNotice) *Service {
	if renderer == nil {
		renderer = card.NewFakeRenderer()
	}
	if runner == nil {
		runner = CLIExecRunner{}
	}
	if recorder == nil {
		recorder = audit.NewRecorder()
	}
	if sessions == nil {
		sessions = session.NewManager()
	}
	restoreNotices := cloneRecoveryNotices(notices)
	for _, notice := range restoreNotices {
		recorder.Record("system", "session_recovery_"+string(notice.Status), notice.SessionID, "reply="+notice.ReplyToMessageID+" card_session="+notice.CardSessionID)
	}
	renderer = card.NewLimitRenderer(renderer, cfg.CardMaxChars)
	scopeContext, scopeCancel := context.WithCancel(context.Background())
	return &Service{
		Config:             cfg,
		Sessions:           sessions,
		Cards:              renderer,
		Runner:             runner,
		Audit:              recorder,
		Agents:             config.DefaultAgentsConfig(),
		pendingRuns:        map[string]pendingRun{},
		activeRuns:         map[string]activeRun{},
		pendingCompletions: map[string]pendingCompletion{},
		waitingReactions:   map[string]*reactionLifecycle{},
		reactionDelay:      defaultWaitingReactionDelay,
		startedAt:          time.Now(),
		accepting:          true,
		RestoreNotices:     restoreNotices,
		scopeContext:       scopeContext,
		scopeCancel:        scopeCancel,
	}
}

func cloneRecoveryNotices(in []session.RecoveryNotice) []session.RecoveryNotice {
	out := make([]session.RecoveryNotice, len(in))
	copy(out, in)
	for i := range out {
		if in[i].RenderRef != nil {
			ref := *in[i].RenderRef
			out[i].RenderRef = &ref
		}
	}
	return out
}

// ProcessRecoveryNotices best-effort terminalizes cards that belonged to runs
// interrupted by a restart. Notices are consumed once and stale cards are not
// replaced, because restart recovery must never create a new reply or rerun an
// old input.
func (s *Service) ProcessRecoveryNotices(ctx context.Context) {
	s.mu.Lock()
	notices := s.RestoreNotices
	s.RestoreNotices = nil
	s.mu.Unlock()
	recoveryCtx, cancel := context.WithTimeout(ctx, recoveryNoticesTimeout)
	defer cancel()

	seenCards := make(map[string]struct{}, len(notices))
	for _, notice := range notices {
		if err := recoveryCtx.Err(); err != nil {
			s.Audit.Record("system", "recovery_card_update_failed", notice.SessionID, err.Error())
			break
		}
		if notice.RenderRef == nil || notice.RenderRef.CardID == "" {
			continue
		}
		if _, seen := seenCards[notice.RenderRef.CardID]; seen {
			continue
		}
		seenCards[notice.RenderRef.CardID] = struct{}{}
		if notice.RenderRef.SequenceUnknown || (s.SequenceResolver != nil && s.SequenceResolver.RenderRefSequenceUnknown(*notice.RenderRef)) {
			s.Audit.Record(
				"system",
				"recovery_card_update_skipped_sequence_unknown",
				notice.SessionID,
				fmt.Sprintf("pending_sequence=%d", notice.RenderRef.PendingSequence),
			)
			continue
		}
		if s.CardTarget == nil {
			s.Audit.Record("system", "recovery_card_update_failed", notice.SessionID, "reply card target is unavailable")
			continue
		}
		cardSessionID := notice.CardSessionID
		if cardSessionID == "" {
			cardSessionID = runID(notice.SessionID, notice.ReplyToMessageID)
		}
		renderer := s.CardTarget.Rehydrate(cardSessionID, *notice.RenderRef)
		if renderer == nil {
			s.Audit.Record("system", "recovery_card_update_failed", notice.SessionID, "reply card renderer is unavailable")
			continue
		}
		event := card.Event{
			Type:             "interrupted",
			SessionID:        cardSessionID,
			ReplyToMessageID: notice.ReplyToMessageID,
			Segments:         []card.Segment{{Kind: card.SegmentText, Text: "服务重启，已中断，请重新发送"}},
			Meta:             card.Meta{Status: string(session.InputInterrupted)},
			StopButton:       card.StopButton{Visible: true, Disabled: true},
			Actions:          nil,
			Streaming:        false,
			HideAgentPanels:  true,
		}
		var renderErr error
		if contextRenderer, ok := renderer.(feishu.ContextRenderer); ok {
			renderErr = contextRenderer.RenderContext(recoveryCtx, event)
		} else {
			renderErr = renderer.Render(event)
		}
		if renderErr != nil {
			s.Audit.Record("system", "recovery_card_update_failed", notice.SessionID, renderErr.Error())
			continue
		}
		if s.Replies != nil {
			latest := s.Replies.GetLatest(notice.SessionID)
			if latest != nil && latest.CardID == notice.RenderRef.CardID {
				advanced := renderer.RenderRef()
				if err := s.Replies.SetLatest(notice.SessionID, &advanced); err != nil {
					s.Audit.Record("system", "recovery_reply_ref_persist_failed", notice.SessionID, err.Error())
				}
			}
		}
	}
}

func (s *Service) HandleMessage(ctx context.Context, msg Message) error {
	if !msg.Time.IsZero() && msg.Time.Before(s.startedAt.Add(-2*time.Second)) {
		s.Audit.Record(msg.Sender, "old_message_skipped", "", msg.ID)
		return nil
	}
	preference := s.runtimePreference()
	if reason := senderRejectionReason(msg, preference.RespondToBots, s.BotOpenID); reason != "" {
		action := "group_message_skipped"
		if reason == IntakeReasonBotDisabled {
			action = "bot_message_skipped"
		}
		s.Audit.Record(msg.Sender, action, msg.ChatID, reason+" message="+msg.ID)
		return nil
	}
	if decision, enforced := s.messageAccessDecision(msg); enforced && !decision.OK {
		s.Audit.Record(msg.Sender, "access_denied", msg.ChatID, string(decision.Reason))
		if msg.IsGroup && msg.Mentioned && decision.Reason == access.ReasonDeniedChat {
			return s.renderTextWithMode("access-denied", msg.ID, card.SegmentError, "当前群尚未加入响应列表，所以 bot 不会处理消息。\nBot owner/管理员可在本群发 /invite group 加入白名单。", preference.ConversationMode)
		}
		return nil
	}
	participated := false
	if s.TopicParticipation != nil && msg.IsGroup && msg.ThreadID != "" {
		participated = s.TopicParticipation.Has(msg.ChatID, msg.ThreadID)
	}
	intake := DecideIntake(msg, preference, s.BotOpenID, participated)
	if !intake.Accept {
		s.Audit.Record(msg.Sender, "group_message_skipped", msg.ChatID, intake.Reason+" message="+msg.ID+" thread="+msg.ThreadID)
		return nil
	}
	if intake.Mark && s.TopicParticipation != nil {
		if err := s.TopicParticipation.Mark(msg.ChatID, msg.ThreadID, effectiveMessageTime(msg)); err != nil {
			s.Audit.Record(msg.Sender, "topic_participation_save_failed", msg.ChatID, "message="+msg.ID+" thread="+msg.ThreadID+" error="+err.Error())
			if renderErr := s.renderTextWithMode("topic-participation", msg.ID, card.SegmentError, "话题参与状态持久化失败；本条消息仍会处理，但后续非 @ 消息将继续忽略。", preference.ConversationMode); renderErr != nil {
				s.Audit.Record(msg.Sender, "topic_participation_error_render_failed", msg.ChatID, renderErr.Error())
			}
		} else {
			s.Audit.Record(msg.Sender, "topic_participation_saved", msg.ChatID, "message="+msg.ID+" thread="+msg.ThreadID)
		}
	}
	if intake.Touch && s.TopicParticipation != nil {
		if err := s.TopicParticipation.Touch(msg.ChatID, msg.ThreadID, effectiveMessageTime(msg)); err != nil {
			s.Audit.Record(msg.Sender, "topic_participation_save_failed", msg.ChatID, "touch message="+msg.ID+" thread="+msg.ThreadID+" error="+err.Error())
		}
	}
	defaultKind, ok := agent.ParseKind(s.Config.DefaultAgent)
	if !ok {
		defaultKind = agent.Claude
	}
	cmd := ParseCommand(msg, defaultKind)
	if cmd.Type == CommandIgnored {
		return nil
	}
	if selected, ok := agent.ParseKind(preference.Agent); ok && (cmd.Type == CommandRun || cmd.Type == CommandStatus) {
		cmd.Agent = selected
	}
	if s.adminCommand(cmd.Type) && !s.canRunAdminCommand(msg.Sender) {
		s.Audit.Record(msg.Sender, "admin_denied", msg.ChatID, string(cmd.Type))
		return s.renderTextWithMode("admin-denied", msg.ID, card.SegmentError, "❌ 此命令仅管理员可用。", preference.ConversationMode)
	}
	if cmd.Type != CommandRun {
		accepted, err := s.Sessions.AcceptMessage(msg.ID, effectiveMessageTime(msg), s.dedupTTL(), s.dedupMaxEntries())
		if err != nil {
			s.Audit.Record(msg.Sender, "command_persist_failed", "", err.Error())
			return s.renderTextWithMode("storage", msg.ID, card.SegmentError, "持久化失败，未执行。", preference.ConversationMode)
		}
		if !accepted {
			return nil
		}
	}
	switch cmd.Type {
	case CommandHelp:
		return s.renderTextWithMode("help", msg.ID, card.SegmentText, HelpText(), preference.ConversationMode)
	case CommandUnknown:
		return s.renderTextWithMode("command", msg.ID, card.SegmentError, cmd.Text, preference.ConversationMode)
	case CommandStatus:
		return s.renderTextWithMode("status", msg.ID, card.SegmentText, s.statusTextWithPreference(cmd.Agent, msg, preference), preference.ConversationMode)
	case CommandConfig:
		return s.handleConfigCommand(ctx, msg, cmd, preference)
	case CommandAgentMode:
		return s.handleAgentModeCommand(ctx, msg, cmd, preference)
	case CommandInvite:
		return s.handleInviteCommand(ctx, msg, cmd, preference)
	case CommandRemove:
		return s.handleRemoveCommand(msg, cmd, preference)
	case CommandRun:
		return s.runWithPreference(ctx, cmd, msg, "", preference)
	default:
		return s.renderTextWithMode("command", msg.ID, card.SegmentError, "unsupported command", preference.ConversationMode)
	}
}

func (s *Service) handleConfigCommand(ctx context.Context, msg Message, cmd Command, preference config.RuntimePreference) error {
	switch strings.ToLower(strings.TrimSpace(cmd.Text)) {
	case "":
		if s.AccessInfo != nil {
			if s.AccessAppID != "" {
				_ = access.RefreshOwner(ctx, s.AccessControls, s.AccessInfo, s.AccessAppID)
			}
			_ = s.refreshKnownChats(ctx)
		}
		return s.Cards.Render(card.Event{
			Type:             "config",
			SessionID:        runID("config", msg.ID),
			ReplyToMessageID: msg.ID,
			ReplyInThread:    preference.ConversationMode == config.ConversationModeTopic,
			ConfigForm:       s.configForm(preference),
		})
	case "reset":
		if s.Preferences == nil {
			return s.renderTextWithMode("config-reset", msg.ID, card.SegmentError, "偏好存储尚未配置。", preference.ConversationMode)
		}
		if err := s.Preferences.Reset(); err != nil {
			s.Audit.Record(msg.Sender, "config_reset_failed", "", err.Error())
			return s.renderTextWithMode("config-reset", msg.ID, card.SegmentError, "偏好重置失败，请检查存储状态。", preference.ConversationMode)
		}
		s.Audit.Record(msg.Sender, "config_reset", "", "runtime preferences reset")
		return s.renderTextWithMode("config-reset", msg.ID, card.SegmentText, "已恢复环境默认的 agent / home / bin / model / effort / reply mode / conversation mode；下一条新消息开始生效。", preference.ConversationMode)
	default:
		return s.renderTextWithMode("config", msg.ID, card.SegmentError, "用法：/config 或 /config reset", preference.ConversationMode)
	}
}

func (s *Service) handleAgentModeCommand(_ context.Context, msg Message, cmd Command, preference config.RuntimePreference) error {
	mode := strings.ToLower(strings.TrimSpace(cmd.Text))
	if mode == "" {
		return s.Cards.Render(card.Event{
			Type:             "agent_mode",
			SessionID:        runID("agent-mode", msg.ID),
			ReplyToMessageID: msg.ID,
			ReplyInThread:    preference.ConversationMode == config.ConversationModeTopic,
			AgentModeForm:    &card.AgentModeForm{Agent: orDefault(preference.Agent, config.DefaultAgentKind), Agents: toCardOptions(s.Agents.AgentOptions())},
		})
	}
	if _, ok := agent.ParseKind(mode); !ok {
		return s.renderTextWithMode("agent-mode", msg.ID, card.SegmentError, "用法：/agent-mode 或 /agent-mode claude|codex", preference.ConversationMode)
	}
	if s.Preferences == nil {
		return s.renderTextWithMode("agent-mode", msg.ID, card.SegmentError, "偏好存储尚未配置。", preference.ConversationMode)
	}
	preference.Agent = mode
	preference.AgentHome = ""
	preference.AgentBin = ""
	if err := s.Preferences.Set(preference); err != nil {
		return s.renderTextWithMode("agent-mode", msg.ID, card.SegmentError, "Agent mode 保存失败，请检查配置。", preference.ConversationMode)
	}
	return s.renderTextWithMode("agent-mode", msg.ID, card.SegmentText, fmt.Sprintf("已切换到 `%s`。`/config` 现在只显示该 Agent 可用的 home/bin。", mode), preference.ConversationMode)
}

func (s *Service) runtimePreference() config.RuntimePreference {
	if s.Preferences != nil {
		return s.Preferences.Get()
	}
	model := strings.TrimSpace(s.Config.Model)
	if model == "" {
		model = "default"
	}
	effort := strings.ToLower(strings.TrimSpace(s.Config.Effort))
	if effort == "" {
		effort = "low"
	}
	mode := s.Config.ReplyMode
	if mode == "" {
		mode = config.ReplyModeAppend
	}
	conversationMode := s.Config.ConversationMode
	if conversationMode == "" {
		conversationMode = config.ConversationModeChat
	}
	groupMessageMode := s.Config.GroupMessageMode
	if groupMessageMode == "" {
		groupMessageMode = config.GroupMessageModeMentionOnly
	}
	return config.RuntimePreference{Model: model, Effort: effort, ReplyMode: mode, ConversationMode: conversationMode, GroupMessageMode: groupMessageMode, RespondToBots: s.Config.RespondToBots, Agent: config.DefaultAgentKind}
}

// resolveAgentBinHome resolves the executable path and home/config directory
// for a run from the current preference against the agents catalogue.
//
//   - bin: the preset's path if set; otherwise falls back to Config.ClaudeBin
//     (which itself defaults to "claude"), preserving existing behaviour.
//   - home: the preset's path, or "" for the agent's default home.
//
// Unknown/empty labels resolve to the defaults so a stale selection can never
// wedge execution.
func (s *Service) resolveAgentBinHome(kind agent.Kind, preference config.RuntimePreference) (bin, home string) {
	agentKind := strings.TrimSpace(preference.Agent)
	if agentKind == "" {
		agentKind = string(kind)
	}
	if agentKind == "" {
		agentKind = config.DefaultAgentKind
	}
	if path, ok := s.Agents.BinPath(agentKind, preference.AgentBin); ok && strings.TrimSpace(path) != "" {
		bin = path
	} else if kind == agent.Codex {
		bin = "codex"
	} else {
		bin = s.Config.ClaudeBin
	}
	if path, ok := s.Agents.HomePath(agentKind, preference.AgentHome); ok {
		home = path
	}
	return bin, home
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func toCardOptions(options []config.SelectOption) []card.SelectOption {
	out := make([]card.SelectOption, 0, len(options))
	for _, opt := range options {
		out = append(out, card.SelectOption{Value: opt.Value, Label: opt.Label})
	}
	return out
}

func (s *Service) configModelOptions() []string {
	if len(s.Config.AllowedModels) > 0 {
		return append([]string(nil), s.Config.AllowedModels...)
	}
	return []string{"default", "sonnet", "opus", "haiku"}
}

func (s *Service) HandleMessageRecalled(_ context.Context, recall MessageRecall) error {
	if recall.MessageID == "" {
		return nil
	}
	if pending, ok := s.popPendingRunByMessageID(recall.MessageID); ok {
		s.Audit.Record("system", "message_recalled_pending_cancelled", pending.SessionID, recall.MessageID)
		_, err := s.renderActionEvent(workDirActionEvent("workdir_cancelled", pending.SessionID, pending.WorkDir))
		return err
	}
	if run, ok := s.cancelActiveRunByMessageID(recall.MessageID); ok {
		s.Audit.Record("system", "message_recalled_active_cancelled", run.BaseSessionID, recall.MessageID)
		return nil
	}
	if sess, input, ok, err := s.Sessions.CancelQueuedInputByMessageID(recall.MessageID); err != nil {
		s.Audit.Record("system", "message_recalled_queued_persist_failed", sess.ID, err.Error())
		return s.renderText("storage", recall.MessageID, card.SegmentError, "持久化失败，未执行。")
	} else if ok {
		s.Audit.Record("system", "message_recalled_queued_cancelled", sess.ID, input.ID)
		s.closeWaitingReaction(input.ID)
		return nil
	}
	s.Audit.Record("system", "message_recalled_ignored", recall.ChatID, recall.MessageID)
	return nil
}

func (s *Service) runWithPreference(ctx context.Context, cmd Command, msg Message, cardSessionID string, preference config.RuntimePreference) error {
	if !s.isAccepting() {
		return s.renderTextWithMode("service-stopping", msg.ID, card.SegmentError, "服务正在停止，暂不接受新的执行请求。", preference.ConversationMode)
	}
	if cmd.Agent == "" {
		cmd.Agent, _ = agent.ParseKind(preference.Agent)
		if cmd.Agent == "" {
			cmd.Agent = agent.Claude
		}
	}
	key := sessionKeyForMode(cmd.Agent, msg, preference.ConversationMode)
	workDir := s.effectiveWorkDir(key, cmd)
	pendingID := runID(key.ID(), msg.ID)
	asked, err := s.ensureWorkDirOrAsk(workDir, pendingID, msg.ID, preference.ConversationMode)
	if err != nil {
		return err
	}
	if asked {
		runCardSessionID := cardSessionID
		if runCardSessionID == "" {
			runCardSessionID = runID(key.ID(), msg.ID+":run")
		}
		s.storePendingRun(pendingID, pendingRun{RunCardSessionID: runCardSessionID, Command: cmd, Message: msg, WorkDir: workDir, Preference: preference})
		return nil
	}
	text := strings.TrimSpace(cmd.Text)
	if text == "" && len(msg.Attachments) == 0 && !cmd.Reset {
		return s.renderTextWithMode("empty", msg.ID, card.SegmentError, "empty prompt", preference.ConversationMode)
	}
	attachments, failures, release := s.resolveAttachments(ctx, msg.Attachments)
	defer release()
	var summaryErr error
	if len(failures) > 0 {
		summaryErr = s.renderTextWithMode("attachment-failure", msg.ID, card.SegmentError, attachmentFailureSummary(failures, len(attachments) > 0), preference.ConversationMode)
	}
	if len(msg.Attachments) > 0 && len(attachments) == 0 && text == "" {
		return summaryErr
	}
	bin, home := s.resolveAgentBinHome(cmd.Agent, preference)
	receivedAt := time.Now()
	debounceWindow := DebounceFor(msg)
	input := session.Input{ID: msg.ID, Sender: msg.Sender, Text: text, Attachments: attachments, ReplyToMessageID: msg.ID, CardSessionID: cardSessionID, WorkDir: workDir, RequestedModel: preference.Model, RequestedEffort: preference.Effort, AgentBin: bin, AgentHome: home, ReplyMode: preference.ReplyMode, ConversationMode: preference.ConversationMode, BridgeInstructionsVersion: bridgeinstructions.CurrentVersion, Time: effectiveMessageTime(msg), DebounceUntil: receivedAt.Add(debounceWindow), DebounceWindow: debounceWindow, State: session.InputDebouncing, Reset: cmd.Reset}
	accepted, queued, err := s.Sessions.AcceptAndEnqueue(key, input, receivedAt, s.dedupTTL(), s.dedupMaxEntries(), s.batchLimits())
	if err != nil {
		action := "queue_rejected"
		message := "队列已满，未执行。"
		if !strings.Contains(err.Error(), "pending input limit") {
			action = "queue_persist_failed"
			message = "持久化失败，未执行。"
		}
		s.Audit.Record(msg.Sender, action, key.ID(), err.Error())
		return s.renderTextWithMode("queue-error", msg.ID, card.SegmentError, message, preference.ConversationMode)
	}
	if !accepted {
		return summaryErr
	}
	sess, _ := s.Sessions.Get(key)
	s.Audit.Record(msg.Sender, "queue_input", sess.ID, fmt.Sprintf("position=%d", queued.Position))
	s.startWaitingReaction(msg.ID)
	if summaryErr != nil {
		return summaryErr
	}
	return nil
}

func (s *Service) effectiveWorkDir(key session.Key, cmd Command) string {
	if cmd.WorkDir != "" {
		return cmd.WorkDir
	}
	if existing, ok := s.Sessions.Get(key); ok && existing.WorkDir != "" {
		return existing.WorkDir
	}
	return s.Config.DefaultWorkDir
}

// DrainReady freezes due queues and starts each independent scope concurrently.
func (s *Service) DrainReady(now time.Time) error {
	s.mu.Lock()
	if !s.accepting {
		s.mu.Unlock()
		return nil
	}
	s.dispatchWG.Add(1)
	s.mu.Unlock()
	defer s.dispatchWG.Done()
	s.retryPendingCompletions(now, false)
	for _, key := range s.Sessions.ReadyKeys(now) {
		sess, batch, err := s.Sessions.FreezeReadyBatch(key, now, s.batchLimits())
		if err != nil {
			return err
		}
		if batch != nil {
			s.startBatch(context.Background(), sess, *batch)
		}
	}
	return nil
}

func (s *Service) startBatch(parent context.Context, sess session.Session, batch session.Batch) {
	s.closeBatchWaitingReactions(batch)
	if !s.isAccepting() {
		s.finishBatchOrRemember(sess, batch.ID, session.BatchCompletion{Status: session.InputCancelled, At: time.Now()}, "batch_finish_failed")
		return
	}
	if len(batch.Inputs) == 0 {
		return
	}
	anchor := batch.Inputs[len(batch.Inputs)-1]
	id := runCardSessionID(sess.ID, anchor)
	runCtx, cancel := context.WithCancel(parent)
	typing := s.startTypingReaction(anchor.ReplyToMessageID)
	sources := make([]string, 0, len(batch.Inputs)*2)
	for _, in := range batch.Inputs {
		sources = append(sources, in.ID, in.ReplyToMessageID)
	}
	stream := newAgentCardStream(s, id, sess, anchor)
	mode := anchor.EffectiveReplyMode()
	if s.CardTarget != nil {
		latestScope := ""
		if mode == config.ReplyModeLatestCard {
			latestScope = sess.ID
		}
		policy := reply.NewPolicy(s.CardTarget, s.Replies)
		policy.Resolver = s.SequenceResolver
		policyRun, err := policy.BeginBound(runCtx, mode, sess.ID, feishu.RenderBinding{
			BaseSessionID: sess.ID, BatchID: batch.ID, LatestScope: latestScope, RunCardSessionID: id,
		}, anchor.ReplyToMessageID)
		if err != nil {
			cancel()
			typing.Close()
			_, _ = s.finishBatchOrRemember(sess, batch.ID, session.BatchCompletion{Status: session.InputFailed, At: time.Now()}, "batch_finish_failed")
			s.Audit.Record("system", "reply_policy_start_failed", sess.ID, err.Error())
			_ = s.Cards.Render(card.Event{Type: "error", SessionID: id, ReplyToMessageID: anchor.ReplyToMessageID, ReplyInThread: anchor.ConversationMode == config.ConversationModeTopic, Segments: []card.Segment{{Kind: card.SegmentError, Text: "回复卡片初始化失败，请重试。"}}})
			return
		}
		var renderer card.Renderer = policyRun
		if mode == config.ReplyModeAppend {
			renderer = reply.NewMarkdownCardRenderer(policyRun)
		}
		stream = newAgentCardStreamWithRenderer(s, id, sess, anchor, card.NewLimitRenderer(renderer, s.Config.CardMaxChars), policyRun)
	}
	s.storeActiveRun(id, activeRun{BaseSessionID: sess.ID, BatchID: batch.ID, SourceMessageIDs: sources, Key: sess.Key, WorkDir: sess.WorkDir, Cancel: cancel, Stream: stream, Typing: typing})
	if s.afterStoreActiveRunHook != nil {
		s.afterStoreActiveRunHook()
	}
	if !s.isAccepting() || runCtx.Err() != nil {
		s.finishStartingBatch(sess, batch, id, session.InputCancelled, "stopped", AgentRunResult{})
		return
	}
	if err := stream.Start(); err != nil {
		cancel()
		s.finishStartingBatch(sess, batch, id, session.InputFailed, "failed", AgentRunResult{})
		s.Audit.Record("system", "card_render_failed", sess.ID, err.Error())
		return
	}
	marked, _, err := s.Sessions.MarkBatchRunning(batchKey(sess, batch), batch.ID, stream.RenderRef(), time.Now())
	if err != nil {
		cancel()
		s.finishStartingBatch(sess, batch, id, session.InputFailed, "failed", AgentRunResult{})
		s.Audit.Record("system", "batch_start_persist_failed", sess.ID, err.Error())
		return
	}
	if runCtx.Err() != nil {
		s.finishStartingBatch(marked, batch, id, session.InputCancelled, "stopped", AgentRunResult{})
		return
	}
	s.runWG.Add(1)
	go s.executeBatch(runCtx, marked, batch, id)
}

func batchKey(sess session.Session, _ session.Batch) session.Key { return sess.Key }

func (s *Service) finishStartingBatch(sess session.Session, batch session.Batch, id string, status session.InputState, cardStatus string, result AgentRunResult) {
	if run, ok := s.activeRun(id); ok && run.BatchID == batch.ID && run.Stream != nil {
		s.finishStreamAndAudit(run.Stream, cardStatus, metaFromSession(sess), result, sess.ID)
	}
	_, _ = s.finishBatchOrRemember(sess, batch.ID, session.BatchCompletion{Status: status, At: time.Now()}, "batch_finish_failed")
	s.clearActiveRun(id)
	_ = s.DrainReady(time.Now())
}

func (s *Service) executeBatch(ctx context.Context, sess session.Session, batch session.Batch, id string) {
	defer s.runWG.Done()
	defer func() { s.clearActiveRun(id); _ = s.DrainReady(time.Now()) }()
	prompt := BuildBatchPrompt(batch)
	if batch.Inputs[0].Reset && strings.TrimSpace(prompt) == "" {
		if run, ok := s.activeRun(id); ok && run.BatchID == batch.ID && run.Stream != nil {
			s.finishStreamAndAudit(run.Stream, "completed", metaFromSession(sess), AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "Agent session is ready. Send a message in this chat/topic to continue."}}}, sess.ID)
		}
		_, _ = s.finishBatchOrRemember(sess, batch.ID, session.BatchCompletion{Status: session.InputCompleted, At: time.Now()}, "completion_persist_failed")
		return
	}
	var actualModelMu sync.Mutex
	streamedActualModel := ""
	bin, home := batch.Inputs[0].AgentBin, batch.Inputs[0].AgentHome
	if strings.TrimSpace(bin) == "" {
		bin, home = s.resolveAgentBinHome(sess.Key.Agent, s.runtimePreference())
	}
	s.Audit.Record("system", "bridge_instructions_selected", sess.ID, "version="+sess.BridgeInstructionsVersion+" agent="+string(sess.Key.Agent))
	result, err := s.Runner.Run(ctx, AgentRunRequest{Kind: sess.Key.Agent, Bin: bin, Home: home, Prompt: prompt, WorkDir: sess.WorkDir, Images: codexImagePaths(batch), AgentSessionID: sess.AgentSessionID, Model: batch.Inputs[0].RequestedModel, Effort: batch.Inputs[0].RequestedEffort, BridgeInstructionsVersion: sess.BridgeInstructionsVersion, OnEvent: func(update AgentStreamUpdate) {
		if model := strings.TrimSpace(update.Model); model != "" {
			actualModelMu.Lock()
			streamedActualModel = model
			actualModelMu.Unlock()
		}
		if run, ok := s.activeRun(id); ok && run.BatchID == batch.ID && run.Stream != nil {
			run.Stream.Handle(update)
		}
	}})
	if strings.TrimSpace(result.Model) == "" {
		actualModelMu.Lock()
		result.Model = streamedActualModel
		actualModelMu.Unlock()
	} else {
		result.Model = strings.TrimSpace(result.Model)
	}
	status, cardStatus := session.InputCompleted, "completed"
	if errors.Is(ctx.Err(), context.Canceled) {
		status, cardStatus = session.InputCancelled, "stopped"
	} else if err != nil {
		status, cardStatus = session.InputFailed, "failed"
		result.Segments = append(result.Segments, card.Segment{Kind: card.SegmentError, Text: err.Error()})
	}
	if len(result.Segments) == 0 && status == session.InputCompleted {
		result.Segments = []card.Segment{{Kind: card.SegmentText, Text: "Agent 未返回内容。"}}
	}
	requestedModel := strings.TrimSpace(batch.Inputs[0].RequestedModel)
	if sess.Key.Agent == agent.Claude && result.Model != "" && requestedModel != "" && !strings.EqualFold(requestedModel, "default") && result.Model != requestedModel {
		s.Audit.Record("system", "model_requested_actual_mismatch", sess.ID, fmt.Sprintf("requested=%s actual=%s", requestedModel, result.Model))
	}
	if result.ProtocolUnknown > 0 || result.ProtocolAnomalies > 0 {
		s.Audit.Record("system", "codex_protocol_drift", sess.ID, fmt.Sprintf("unknown=%d anomalies=%d", result.ProtocolUnknown, result.ProtocolAnomalies))
	}
	if run, ok := s.activeRun(id); ok && run.BatchID == batch.ID && run.Stream != nil {
		if status == session.InputCompleted {
			s.finishStreamWithOutputImages(ctx, run.Stream, cardStatus, metaFromSession(sess), result, sess, batch)
		} else {
			s.finishStreamAndAudit(run.Stream, cardStatus, metaFromSession(sess), result, sess.ID)
		}
	}
	_, _ = s.finishBatchOrRemember(sess, batch.ID, session.BatchCompletion{Status: status, AgentSessionID: result.AgentSessionID, Model: result.Model, Tokens: result.Tokens, At: time.Now()}, "completion_persist_failed")
}

func codexImagePaths(batch session.Batch) []string {
	paths := make([]string, 0)
	for _, input := range batch.Inputs {
		for _, attachment := range input.Attachments {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.MIME)), "image/") && strings.TrimSpace(attachment.Path) != "" {
				paths = append(paths, attachment.Path)
			}
		}
	}
	return paths
}

// finishStreamAndAudit 收敛终态卡片,并在渲染失败时记录 audit,而不是静默吞掉错误
// (终态渲染失败意味着用户卡片停在旧状态,必须可观测)。
func (s *Service) finishStreamAndAudit(stream *agentCardStream, cardStatus string, meta card.Meta, result AgentRunResult, sessionID string) {
	if _, err := stream.Finish(cardStatus, meta, result); err != nil {
		s.Audit.Record("system", "terminal_reply_render_failed", sessionID, fmt.Sprintf("status=%s error=%v", cardStatus, err))
	}
}

func completionKey(key session.Key, batchID string) string { return key.ID() + "\x00" + batchID }

func (s *Service) finishBatchOrRemember(sess session.Session, batchID string, completion session.BatchCompletion, auditAction string) (session.Session, error) {
	updated, err := s.Sessions.FinishBatch(sess.Key, batchID, completion)
	if err == nil {
		return updated, nil
	}
	s.mu.Lock()
	key := completionKey(sess.Key, batchID)
	if pending, ok := s.pendingCompletions[key]; ok {
		// A repeat terminal observation can refresh the payload, but cannot
		// weaken a retry schedule that is already backing off.
		pending.Completion = completion
		s.pendingCompletions[key] = pending
	} else {
		failedAt := completion.At
		if failedAt.IsZero() {
			failedAt = time.Now()
		}
		s.pendingCompletions[key] = pendingCompletion{Key: sess.Key, BatchID: batchID, Completion: completion, NextRetryAt: failedAt.Add(time.Second)}
	}
	s.mu.Unlock()
	s.Audit.Record("system", auditAction, sess.ID, err.Error())
	return sess, err
}

// retryPendingCompletions only commits terminal outcomes already produced by a
// runner. It never renders cards or starts runners, so retries cannot replay.
func (s *Service) retryPendingCompletions(now time.Time, force bool) {
	s.mu.Lock()
	pending := make([]pendingCompletion, 0, len(s.pendingCompletions))
	for key, completion := range s.pendingCompletions {
		if completion.Retrying || (!force && completion.NextRetryAt.After(now)) {
			continue
		}
		completion.Retrying = true
		s.pendingCompletions[key] = completion
		pending = append(pending, completion)
	}
	s.mu.Unlock()
	for _, pendingCompletion := range pending {
		key := completionKey(pendingCompletion.Key, pendingCompletion.BatchID)
		if sess, ok := s.Sessions.Get(pendingCompletion.Key); !ok || sess.ActiveBatch == nil || sess.ActiveBatch.ID != pendingCompletion.BatchID {
			s.mu.Lock()
			delete(s.pendingCompletions, key)
			s.mu.Unlock()
			continue
		}
		if s.beforePendingCompletionRetryHook != nil {
			s.beforePendingCompletionRetryHook()
		}
		if _, err := s.Sessions.FinishBatch(pendingCompletion.Key, pendingCompletion.BatchID, pendingCompletion.Completion); err == nil {
			s.mu.Lock()
			delete(s.pendingCompletions, key)
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			if current, ok := s.pendingCompletions[key]; ok {
				current.Retrying = false
				current.Attempts++
				current.NextRetryAt = now.Add(completionRetryDelay(current.Attempts))
				s.pendingCompletions[key] = current
			}
			s.mu.Unlock()
		}
	}
}

func completionRetryDelay(attempts int) time.Duration {
	if attempts <= 0 {
		return time.Second
	}
	if attempts >= 5 {
		return 30 * time.Second
	}
	return time.Duration(1<<attempts) * time.Second
}

func effectiveMessageTime(msg Message) time.Time {
	if msg.Time.IsZero() {
		return time.Now()
	}
	return msg.Time
}

func (s *Service) batchLimits() session.BatchLimits {
	limits := session.BatchLimits{MaxInputs: s.Config.BatchMaxInputs, MaxTextRunes: s.Config.BatchMaxTextRunes, MaxAttachments: 10, MaxAttachmentBytes: s.Config.MediaMaxBatchBytes, MaxPending: s.Config.QueueMaxPending}
	if limits.MaxInputs <= 0 {
		limits.MaxInputs = 10
	}
	if limits.MaxTextRunes <= 0 {
		limits.MaxTextRunes = 64 << 10
	}
	if limits.MaxAttachmentBytes <= 0 {
		limits.MaxAttachmentBytes = 100 << 20
	}
	if limits.MaxPending <= 0 {
		limits.MaxPending = 20
	}
	return limits
}

func (s *Service) resolveAttachments(ctx context.Context, refs []media.Ref) ([]media.Attachment, []media.Failure, func()) {
	if len(refs) == 0 {
		return nil, nil, func() {}
	}
	if s.MediaCache == nil || s.MediaDownloader == nil {
		failures := make([]media.Failure, 0, len(refs))
		for _, ref := range refs {
			failures = append(failures, media.Failure{Ref: ref, Code: "media_unavailable", Detail: "attachment resolver is not configured"})
		}
		return nil, failures, func() {}
	}
	s.SweepMediaCache()
	resolution := s.MediaCache.Resolve(ctx, s.MediaDownloader, refs)
	release := resolution.Release
	if release == nil {
		release = func() {}
	}
	return resolution.Attachments, resolution.Failures, release
}

const mediaGCRetryAttempts = 2

// SweepMediaCache runs a runtime sweep with fresh durable live snapshots.
// Failures are observable through audit but never block ordinary message flow.
func (s *Service) SweepMediaCache() {
	s.sweepMediaCache(false)
}

// SweepMediaCacheStartup additionally clears crash-leftover download temp
// files. Call it before accepting any message or starting a media Resolve.
func (s *Service) SweepMediaCacheStartup() {
	s.sweepMediaCache(true)
}

func (s *Service) sweepMediaCache(startup bool) {
	if s.MediaGC == nil || s.Sessions == nil {
		return
	}
	for attempt := 1; attempt <= mediaGCRetryAttempts; attempt++ {
		live := s.Sessions.LiveAttachmentPaths()
		var result media.SweepResult
		var err error
		if startup {
			result, err = s.MediaGC.SweepStartup(live)
		} else {
			result, err = s.MediaGC.Sweep(live)
		}
		if err != nil {
			s.Audit.Record("system", "media_gc_failed", "", err.Error())
			return
		}
		if !result.Stale {
			return
		}
	}
	s.Audit.Record("system", "media_gc_stale", "", "bounded retry exhausted")
}

func attachmentFailureSummary(failures []media.Failure, partial bool) string {
	if len(failures) == 0 {
		return ""
	}
	prefix := "附件处理失败，未执行："
	if partial {
		prefix = "部分附件未处理："
	}
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		part := failure.Code
		if failure.Detail != "" {
			part += " (" + failure.Detail + ")"
		}
		parts = append(parts, part)
	}
	return prefix + strings.Join(parts, "; ")
}

func (s *Service) dedupTTL() time.Duration {
	if s.Config.DedupTTL <= 0 {
		return 24 * time.Hour
	}
	return s.Config.DedupTTL
}

func (s *Service) dedupMaxEntries() int {
	if s.Config.DedupMaxEntries <= 0 {
		return 10000
	}
	return s.Config.DedupMaxEntries
}

func runCardSessionID(baseSessionID string, input session.Input) string {
	if input.CardSessionID != "" {
		return input.CardSessionID
	}
	return runID(baseSessionID, input.ReplyToMessageID)
}

func (s *Service) HandleAction(ctx context.Context, req ActionRequest) error {
	_, err := s.HandleActionResult(ctx, req)
	return err
}

func (s *Service) HandleActionResult(ctx context.Context, req ActionRequest) (ActionResult, error) {
	s.Audit.Record(req.Actor, "card_action", req.SessionID, req.ActionID+" "+req.Value)
	if (req.ActionID == "config.save" || req.ActionID == "agent_mode.save") && !s.canRunAdminCommand(req.Actor) {
		s.Audit.Record(req.Actor, "admin_denied", req.SessionID, req.ActionID)
		return s.renderActionEvent(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "❌ 此操作仅管理员可用。"}}})
	}
	switch req.ActionID {
	case "stop":
		if run, ok := s.cancelActiveRun(req.SessionID); ok {
			s.Audit.Record(req.Actor, "batch_stop_requested", run.BaseSessionID, run.BatchID)
			event := stoppedActionEvent(req.SessionID)
			return s.renderActionEvent(event)
		}
		return s.renderActionEvent(stoppedActionEvent(req.SessionID))
	case "create_workdir":
		pending, hasPending := s.popPendingRun(req.SessionID)
		workDir := req.Value
		if hasPending && pending.WorkDir != "" {
			workDir = pending.WorkDir
		}
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return ActionResult{}, err
		}
		result, err := s.renderActionEvent(workDirActionEvent("workdir_created", req.SessionID, workDir))
		if err != nil {
			return result, err
		}
		if hasPending {
			if err := s.runWithPreference(ctx, pending.Command, pending.Message, pending.RunCardSessionID, pending.Preference); err != nil {
				return result, err
			}
		}
		return result, nil
	case "cancel_workdir":
		pending, hasPending := s.popPendingRun(req.SessionID)
		workDir := req.Value
		if hasPending && pending.WorkDir != "" {
			workDir = pending.WorkDir
		}
		return s.renderActionEvent(workDirActionEvent("workdir_cancelled", req.SessionID, workDir))
	case "config.save":
		if s.Preferences == nil {
			err := errors.New("preference store is not configured")
			s.Audit.Record(req.Actor, "config_save_failed", req.SessionID, err.Error())
			return s.renderActionEvent(configSaveErrorEvent(req.SessionID))
		}
		current := s.Preferences.Get()
		selectedAgent := strings.TrimSpace(req.FormValues["agent"])
		if selectedAgent == "" {
			selectedAgent = current.Agent
		}
		groupMessageMode := current.GroupMessageMode
		if raw, ok := req.FormValues["group_message_mode"]; ok && strings.TrimSpace(raw) != "" {
			groupMessageMode = config.GroupMessageMode(raw)
		}
		respondToBots := current.RespondToBots
		if raw, ok := req.FormValues["respond_to_bots"]; ok {
			parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				s.Audit.Record(req.Actor, "config_save_failed", req.SessionID, "invalid respond_to_bots")
				return s.renderActionEvent(configSaveErrorEvent(req.SessionID))
			}
			respondToBots = parsed
		}
		preference := config.RuntimePreference{Model: req.FormValues["model"], Effort: req.FormValues["effort"], ReplyMode: config.ReplyMode(req.FormValues["reply_mode"]), ConversationMode: config.ConversationMode(req.FormValues["conversation_mode"]), GroupMessageMode: groupMessageMode, RespondToBots: respondToBots, Agent: selectedAgent, AgentHome: req.FormValues["agent_home"], AgentBin: req.FormValues["agent_bin"]}
		if !strings.EqualFold(strings.TrimSpace(current.Agent), strings.TrimSpace(preference.Agent)) {
			if _, ok := s.Agents.HomePath(preference.Agent, preference.AgentHome); !ok {
				preference.AgentHome = ""
			}
			if _, ok := s.Agents.BinPath(preference.Agent, preference.AgentBin); !ok {
				preference.AgentBin = ""
			}
		}
		if err := s.Preferences.Set(preference); err != nil {
			s.Audit.Record(req.Actor, "config_save_failed", req.SessionID, err.Error())
			return s.renderActionEvent(configSaveErrorEvent(req.SessionID))
		}
		preference = s.Preferences.Get()
		s.Audit.Record(req.Actor, "config_saved", req.SessionID, fmt.Sprintf("agent=%s agent_home=%s agent_bin=%s model=%s effort=%s reply_mode=%s conversation_mode=%s group_message_mode=%s respond_to_bots=%t", preference.Agent, preference.AgentHome, preference.AgentBin, preference.Model, preference.Effort, preference.ReplyMode, preference.ConversationMode, preference.GroupMessageMode, preference.RespondToBots))
		s.Audit.Record(req.Actor, "group_message_mode_saved", req.SessionID, fmt.Sprintf("mode=%s respond_to_bots=%t", preference.GroupMessageMode, preference.RespondToBots))
		result, err := s.renderActionEvent(card.Event{
			Type:       "config_saved",
			SessionID:  req.SessionID,
			ConfigForm: s.configForm(preference),
			Segments:   []card.Segment{{Kind: card.SegmentText, Text: fmt.Sprintf("偏好已保存。\n\nagent=`%s`\nagent home=`%s`\nagent bin=`%s`\nmodel=`%s`\neffort=`%s`\nreply mode=`%s`\nconversation mode=`%s`\ngroup message mode=`%s`\nrespond to bots=`%t`\n\n下一条新消息开始生效。", preference.Agent, orDefault(preference.AgentHome, config.DefaultHomeLabel), orDefault(preference.AgentBin, config.DefaultBinLabelFor(preference.Agent)), preference.Model, preference.Effort, preference.ReplyMode, preference.ConversationMode, preference.GroupMessageMode, preference.RespondToBots)}},
		})
		if err == nil {
			s.ensureGroupMessageScope(req.SessionID, preference.GroupMessageMode)
		}
		return result, err
	case "agent_mode.save":
		if s.Preferences == nil {
			err := errors.New("preference store is not configured")
			s.Audit.Record(req.Actor, "agent_mode_save_failed", req.SessionID, err.Error())
			return s.renderActionEvent(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "Agent mode 保存失败。"}}})
		}
		kind := strings.ToLower(strings.TrimSpace(req.FormValues["agent"]))
		if _, ok := agent.ParseKind(kind); !ok || !s.agentKindConfigured(kind) {
			err := fmt.Errorf("agent mode %q is not configured", kind)
			s.Audit.Record(req.Actor, "agent_mode_save_failed", req.SessionID, err.Error())
			return s.renderActionEvent(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "Agent mode 选项无效。"}}})
		}
		preference := s.Preferences.Get()
		preference.Agent = kind
		preference.AgentHome = ""
		preference.AgentBin = ""
		if err := s.Preferences.Set(preference); err != nil {
			s.Audit.Record(req.Actor, "agent_mode_save_failed", req.SessionID, err.Error())
			return s.renderActionEvent(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "Agent mode 保存失败。"}}})
		}
		preference = s.Preferences.Get()
		s.Audit.Record(req.Actor, "agent_mode_saved", req.SessionID, fmt.Sprintf("agent=%s", preference.Agent))
		return s.renderActionEvent(card.Event{Type: "agent_mode_saved", SessionID: req.SessionID, AgentModeForm: &card.AgentModeForm{Agent: preference.Agent, Agents: toCardOptions(s.Agents.AgentOptions())}})
	default:
		return s.renderActionEvent(card.Event{Type: "error", SessionID: req.SessionID, Segments: []card.Segment{{Kind: card.SegmentError, Text: "unknown action: " + req.ActionID}}})
	}
}

func (s *Service) agentKindConfigured(kind string) bool {
	_, ok := s.Agents.Find(kind)
	return ok
}

func (s *Service) configForm(preference config.RuntimePreference) *card.ConfigForm {
	agentKind := strings.TrimSpace(preference.Agent)
	if agentKind == "" {
		agentKind = config.DefaultAgentKind
	}
	form := &card.ConfigForm{
		Agent: agentKind, AgentHome: preference.AgentHome, AgentBin: preference.AgentBin,
		Model: preference.Model, Effort: preference.Effort, ReplyMode: string(preference.ReplyMode), ConversationMode: string(preference.ConversationMode),
		GroupMessageMode: string(preference.GroupMessageMode), RespondToBots: strconv.FormatBool(preference.RespondToBots),
		Agents: toCardOptions(s.Agents.AgentOptions()), AgentHomes: toCardOptions(s.Agents.HomeOptions(agentKind)), AgentBins: toCardOptions(s.Agents.BinOptions(agentKind)),
		Models: s.configModelOptions(), Efforts: []string{"default", "low", "medium", "high"},
		ReplyModes:        []string{string(config.ReplyModeAppend), string(config.ReplyModeAppendCleanCard), string(config.ReplyModeLatestCard)},
		ConversationModes: []string{string(config.ConversationModeChat), string(config.ConversationModeTopic)},
	}
	s.populateAccessConfigForm(form)
	return form
}

func configSaveErrorEvent(sessionID string) card.Event {
	return card.Event{
		Type:      "error",
		SessionID: sessionID,
		Segments:  []card.Segment{{Kind: card.SegmentError, Text: "偏好保存失败，请检查选项或存储状态。"}},
	}
}

func actionResultFromEvent(event card.Event) ActionResult {
	if event.Type == "" && event.SessionID == "" {
		return ActionResult{}
	}
	return ActionResult{Event: &event}
}

func (s *Service) renderActionEvent(event card.Event) (ActionResult, error) {
	if err := s.Cards.Render(event); err != nil {
		return ActionResult{}, err
	}
	return actionResultFromEvent(event), nil
}

func stoppedActionEvent(sessionID string) card.Event {
	return card.Event{
		Type:           "stopped",
		SessionID:      sessionID,
		StopButton:     card.StopButton{Visible: true, Disabled: true},
		Message:        "stopped",
		HeaderTitle:    "⏹ 已停止 · ⏱ 0s",
		HeaderTemplate: "grey",
	}
}

func workDirActionEvent(eventType, sessionID, workDir string) card.Event {
	message := "workdir: " + workDir
	if eventType == "workdir_created" {
		message = "Workdir created: " + workDir
	} else if eventType == "workdir_cancelled" {
		message = "Workdir creation cancelled: " + workDir
	}
	return card.Event{
		Type:      eventType,
		SessionID: sessionID,
		Segments:  []card.Segment{{Kind: card.SegmentText, Text: message}},
		Actions:   card.WorkDirActions(workDir, true),
		Meta:      card.Meta{WorkDir: workDir},
	}
}

func (s *Service) Cleanup(ctx context.Context) error {
	return s.Shutdown(ctx)
}

// Shutdown is idempotent. It first prevents new durable intake and scheduler
// starts, then waits for active batches. Once its deadline expires it cancels
// every remaining run and waits for their terminal persistence path.
func (s *Service) Shutdown(ctx context.Context) error {
	s.scopeMu.Lock()
	if s.activeScopeCancel != nil {
		s.activeScopeCancel()
		s.activeScopeCancel = nil
	}
	if s.scopeCancel != nil {
		s.scopeCancel()
		s.scopeCancel = nil
	}
	s.scopeMu.Unlock()
	if err := waitGroupContext(ctx, &s.scopeWG); err != nil {
		return err
	}
	s.mu.Lock()
	s.accepting = false
	if s.loopsCancel != nil {
		s.loopsCancel()
		s.loopsCancel = nil
	}
	s.mu.Unlock()
	s.closeAllWaitingReactions()
	if err := waitGroupContext(ctx, &s.dispatchWG); err != nil {
		s.cancelAllActiveRuns()
		_ = waitGroupContext(context.Background(), &s.dispatchWG)
		_ = waitGroupContext(context.Background(), &s.runWG)
		return err
	}
	if err := waitGroupContext(ctx, &s.runWG); err != nil {
		s.cancelAllActiveRuns()
		_ = waitGroupContext(context.Background(), &s.runWG)
		return err
	}
	s.retryPendingCompletions(time.Now(), true)
	return nil
}

func waitGroupContext(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) RenderPendingRunTimeouts(now time.Time) error {
	for _, pending := range s.duePendingRuns(now) {
		s.Audit.Record("system", "pending_run_timeout", pending.SessionID, pending.WorkDir)
		if err := s.Cards.Render(card.Event{
			Type:      "action",
			SessionID: pending.SessionID,
			Message:   "workdir creation timed out: cancelled",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) StartBackgroundLoops(ctx context.Context, outputEvery time.Duration) {
	if outputEvery <= 0 {
		outputEvery = time.Second
	}
	pendingTicker := time.NewTicker(outputEvery)
	readyTicker := time.NewTicker(50 * time.Millisecond)
	mediaTicker := time.NewTicker(6 * time.Hour)
	s.mu.Lock()
	if s.loopsStarted {
		s.mu.Unlock()
		pendingTicker.Stop()
		readyTicker.Stop()
		mediaTicker.Stop()
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	s.loopsCancel, s.loopsStarted = cancel, true
	s.mu.Unlock()
	s.startBackgroundLoopsWithMediaTicks(loopCtx, pendingTicker.C, readyTicker.C, mediaTicker.C)
	go func() {
		<-loopCtx.Done()
		pendingTicker.Stop()
		readyTicker.Stop()
		mediaTicker.Stop()
	}()
}

// startBackgroundLoopsWithTicks is the package-level deterministic test seam.
// The caller has already made the loop unique and owns both tick channels.
func (s *Service) startBackgroundLoopsWithTicks(ctx context.Context, pendingTicks, readyTicks <-chan time.Time) {
	s.startBackgroundLoopsWithMediaTicks(ctx, pendingTicks, readyTicks, nil)
}

func (s *Service) startBackgroundLoopsWithMediaTicks(ctx context.Context, pendingTicks, readyTicks, mediaTicks <-chan time.Time) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-pendingTicks:
				_ = s.RenderPendingRunTimeouts(now)
			case now := <-readyTicks:
				_ = s.DrainReady(now)
			case <-mediaTicks:
				s.SweepMediaCache()
			}
		}
	}()
}

func (s *Service) isAccepting() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.accepting }

func (s *Service) cancelAllActiveRuns() {
	s.mu.Lock()
	runs := make([]activeRun, 0, len(s.activeRuns))
	for _, run := range s.activeRuns {
		runs = append(runs, run)
	}
	s.mu.Unlock()
	for _, run := range runs {
		run.Cancel()
	}
}

func (s *Service) storeActiveRun(id string, run activeRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeRuns[id] = run
}

func (s *Service) clearActiveRun(id string) {
	s.mu.Lock()
	run, ok := s.activeRuns[id]
	delete(s.activeRuns, id)
	s.mu.Unlock()
	if ok {
		run.Typing.Close()
	}
}

func (s *Service) activeRun(id string) (activeRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.activeRuns[id]
	return run, ok
}

func (s *Service) cancelActiveRun(id string) (activeRun, bool) {
	s.mu.Lock()
	run, ok := s.activeRuns[id]
	if ok {
		run.Cancel()
	}
	s.mu.Unlock()
	// 在锁外标记停止:阻止排队的 preview 在停止卡发出后再把卡片渲染回"运行中"。
	if ok && run.Stream != nil {
		run.Stream.markStopping()
	}
	return run, ok
}

func (s *Service) cancelActiveRunByMessageID(messageID string) (activeRun, bool) {
	if messageID == "" {
		return activeRun{}, false
	}
	s.mu.Lock()
	var matched activeRun
	found := false
	for _, run := range s.activeRuns {
		for _, sourceID := range run.SourceMessageIDs {
			if sourceID == messageID {
				matched, found = run, true
				break
			}
		}
		if found {
			break
		}
	}
	if found {
		matched.Cancel()
	}
	s.mu.Unlock()
	// 锁外标记停止,阻止停止后排队的 preview 把卡片渲染回"运行中"。
	if found && matched.Stream != nil {
		matched.Stream.markStopping()
	}
	return matched, found
}

func (s *Service) storePendingRun(sessionID string, pending pendingRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending.SessionID = sessionID
	if s.Config.InteractionTimeout > 0 && pending.ExpiresAt.IsZero() {
		pending.ExpiresAt = time.Now().Add(s.Config.InteractionTimeout)
	}
	s.pendingRuns[sessionID] = pending
}

func (s *Service) popPendingRun(sessionID string) (pendingRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.pendingRuns[sessionID]
	if ok {
		delete(s.pendingRuns, sessionID)
	}
	return pending, ok
}

func (s *Service) popPendingRunByMessageID(messageID string) (pendingRun, bool) {
	if messageID == "" {
		return pendingRun{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for sessionID, pending := range s.pendingRuns {
		if pending.Message.ID != messageID {
			continue
		}
		delete(s.pendingRuns, sessionID)
		return pending, true
	}
	return pendingRun{}, false
}

func (s *Service) duePendingRuns(now time.Time) []pendingRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []pendingRun
	for sessionID, pending := range s.pendingRuns {
		if pending.ExpiresAt.IsZero() {
			continue
		}
		if !now.Before(pending.ExpiresAt) {
			due = append(due, pending)
			delete(s.pendingRuns, sessionID)
		}
	}
	return due
}

func (s *Service) ensureWorkDirOrAsk(workDir, sessionID, replyToMessageID string, mode config.ConversationMode) (bool, error) {
	if workDir == "" {
		return false, fmt.Errorf("workdir is empty")
	}
	info, err := os.Stat(workDir)
	if err == nil && info.IsDir() {
		return false, nil
	}
	if err == nil && !info.IsDir() {
		return false, fmt.Errorf("workdir is not a directory: %s", workDir)
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	return true, s.Cards.Render(card.Event{
		Type:             "workdir_confirm",
		SessionID:        sessionID,
		ReplyToMessageID: replyToMessageID,
		ReplyInThread:    mode == config.ConversationModeTopic,
		Segments:         []card.Segment{{Kind: card.SegmentText, Text: "Workdir does not exist: " + workDir}},
		Actions:          card.WorkDirCreateActions(workDir),
	})
}

func (s *Service) renderText(id, replyToMessageID string, kind card.SegmentKind, text string) error {
	return s.renderTextWithMode(id, replyToMessageID, kind, text, s.runtimePreference().ConversationMode)
}

func (s *Service) renderTextWithMode(id, replyToMessageID string, kind card.SegmentKind, text string, mode config.ConversationMode) error {
	for i, page := range card.SplitLongText(text, s.Config.CardMaxChars) {
		eventID := runID(id, replyToMessageID)
		if i > 0 {
			eventID = fmt.Sprintf("%s-page-%d", eventID, i+1)
		}
		if err := s.Cards.Render(card.Event{Type: "message", SessionID: eventID, ReplyToMessageID: replyToMessageID, ReplyInThread: mode == config.ConversationModeTopic, Segments: []card.Segment{{Kind: kind, Text: page}}}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) renderStream(event card.Event) error {
	pages := card.SplitSegments(event.Segments, s.Config.CardMaxChars)
	for i, segments := range pages {
		pageEvent := event
		pageEvent.Segments = segments
		if len(pages) > 1 {
			pageEvent.Message = fmt.Sprintf("page %d/%d", i+1, len(pages))
		}
		if err := s.Cards.Render(pageEvent); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) statusText(kind agent.Kind, msg Message) string {
	return s.statusTextWithPreference(kind, msg, s.runtimePreference())
}

func (s *Service) statusTextWithPreference(kind agent.Kind, msg Message, preference config.RuntimePreference) string {
	key := sessionKeyForMode(kind, msg, preference.ConversationMode)
	var b strings.Builder
	fmt.Fprintf(&b, "mode=%s_oneshot\n", kind)
	fmt.Fprintf(&b, "agent=%s\n", kind)
	fmt.Fprintf(&b, "default_workdir=%s\n", s.Config.DefaultWorkDir)
	fmt.Fprintf(&b, "current_session=%s\n", key.ID())
	fmt.Fprintf(&b, "reply_mode=%s\n", preference.ReplyMode)
	fmt.Fprintf(&b, "conversation_mode=%s\n", preference.ConversationMode)
	sess := s.findSession(key.ID())
	if sess == nil {
		b.WriteString("state=not_started")
		return b.String()
	}
	fmt.Fprintf(&b, "state=%s\n", sess.State)
	fmt.Fprintf(&b, "workdir=%s\n", sess.WorkDir)
	fmt.Fprintf(&b, "queue=%d\n", len(sess.Queue))
	fmt.Fprintf(&b, "history=%d\n", len(sess.History))
	if sess.AgentSessionID != "" {
		fmt.Fprintf(&b, "agent_session=%s\n", sess.AgentSessionID)
	}
	if sess.Model != "" {
		fmt.Fprintf(&b, "model=%s\n", sess.Model)
	} else if kind == agent.Codex {
		fmt.Fprintf(&b, "model=由 Codex 配置决定\n")
	}
	if sess.Tokens > 0 {
		fmt.Fprintf(&b, "tokens=%d\n", sess.Tokens)
	}
	if msg.IsGroup {
		fmt.Fprintf(&b, "chat_sessions=%d\n", s.countChatSessions(msg.ChatID))
	}
	return strings.TrimSpace(b.String())
}

func (s *Service) findSession(id string) *session.Session {
	for _, sess := range s.Sessions.List() {
		if sess.ID == id {
			cp := sess
			return &cp
		}
	}
	return nil
}

func (s *Service) countChatSessions(chatID string) int {
	count := 0
	for _, sess := range s.Sessions.List() {
		if sess.Key.ChatID == chatID {
			count++
		}
	}
	return count
}

func (s *Service) sessionKey(kind agent.Kind, msg Message) session.Key {
	return sessionKeyForMode(kind, msg, s.runtimePreference().ConversationMode)
}

func sessionKeyForMode(kind agent.Kind, msg Message, mode config.ConversationMode) session.Key {
	key := session.Key{Agent: kind, ChatID: msg.ChatID}
	if mode == config.ConversationModeTopic {
		key.Thread = msg.ThreadID
	}
	return key
}

func metaFromSession(sess session.Session) card.Meta {
	userName, ip := runtimeIdentity()
	return card.Meta{Agent: string(sess.Key.Agent), Model: sess.Model, Tokens: sess.Tokens, TotalTokens: sess.Tokens, User: userName, IP: ip, WorkDir: sess.WorkDir, Status: string(sess.State)}
}

var (
	runtimeIdentityOnce sync.Once
	runtimeUserName     string
	runtimeIP           string
)

func runtimeIdentity() (string, string) {
	runtimeIdentityOnce.Do(func() {
		if current, err := osuser.Current(); err == nil && current != nil {
			runtimeUserName = strings.TrimSpace(current.Username)
			if idx := strings.LastIndex(runtimeUserName, "\\"); idx >= 0 {
				runtimeUserName = runtimeUserName[idx+1:]
			}
			if idx := strings.LastIndex(runtimeUserName, "/"); idx >= 0 {
				runtimeUserName = runtimeUserName[idx+1:]
			}
		}
		if runtimeUserName == "" {
			runtimeUserName = os.Getenv("USER")
		}
		runtimeIP = firstNonLoopbackIPv4()
	})
	return runtimeUserName, runtimeIP
}

func firstNonLoopbackIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil {
				continue
			}
			ipv4 := ip.To4()
			if ipv4 == nil || ipv4.IsLoopback() {
				continue
			}
			return ipv4.String()
		}
	}
	return ""
}

func runID(baseSessionID, replyToMessageID string) string {
	if replyToMessageID == "" {
		return baseSessionID
	}
	return baseSessionID + ":message:" + replyToMessageID
}

type CLIExecRunner struct{}

func (CLIExecRunner) Run(ctx context.Context, req AgentRunRequest) (AgentRunResult, error) {
	bin := strings.TrimSpace(req.Bin)
	if bin == "" {
		bin = req.ClaudeBin
	}
	command, err := agent.BuildOneShotCommand(agent.OneShotConfig{
		Kind:           req.Kind,
		Bin:            bin,
		WorkDir:        req.WorkDir,
		Prompt:         req.Prompt,
		AgentSessionID: req.AgentSessionID,
		Model:          req.Model,
		Effort:         req.Effort,
		Home:           req.Home,
		Images:         req.Images,
	})
	if err != nil {
		return AgentRunResult{}, err
	}
	if len(command) == 0 {
		return AgentRunResult{}, fmt.Errorf("empty agent command")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	agentEnv := agent.AgentEnv(req.Kind, req.Home)
	if req.WorkDir != "" {
		workDir, err := filepath.Abs(req.WorkDir)
		if err != nil {
			return AgentRunResult{}, err
		}
		cmd.Dir = workDir
		cmd.Env = childEnv(workDir, agentEnv)
	} else if len(agentEnv) > 0 {
		// No workdir override, but a home/config dir must still be injected.
		cmd.Env = childEnv("", agentEnv)
	}
	cmd.Stderr = &stderr
	if agent.PromptOnStdin(req.Kind) {
		cmd.Stdin = strings.NewReader(req.Prompt)
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return AgentRunResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return AgentRunResult{}, err
	}
	var result AgentRunResult
	var scanErr error
	if req.Kind == agent.Codex {
		result, scanErr = parseCodexStream(pipe, &stdout, req.OnEvent)
	} else {
		result, scanErr = parseClaudeStream(pipe, &stdout, req.OnEvent)
	}
	waitErr := cmd.Wait()
	if scanErr != nil {
		return result, scanErr
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return result, fmt.Errorf("%w: %s", waitErr, detail)
		}
		return result, waitErr
	}
	return result, nil
}

// childEnv builds the child process environment from the parent's, optionally
// overriding PWD (when workDir is non-empty) and applying extra KEY=VALUE
// overrides (last write wins for a given key). It preserves the prior behaviour
// of stripping any inherited PWD before appending the run's working directory.
func childEnv(workDir string, extra []string) []string {
	overrides := map[string]string{}
	for _, kv := range extra {
		if key, _, ok := splitEnv(kv); ok {
			overrides[key] = kv
		}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides)+1)
	for _, item := range os.Environ() {
		if workDir != "" && strings.HasPrefix(item, "PWD=") {
			continue
		}
		if key, _, ok := splitEnv(item); ok {
			if _, replaced := overrides[key]; replaced {
				continue
			}
		}
		env = append(env, item)
	}
	for _, kv := range overrides {
		env = append(env, kv)
	}
	if workDir != "" {
		env = append(env, "PWD="+workDir)
	}
	return env
}

func splitEnv(kv string) (key, value string, ok bool) {
	idx := strings.IndexByte(kv, '=')
	if idx <= 0 {
		return "", "", false
	}
	return kv[:idx], kv[idx+1:], true
}

func ParseClaudeStreamOutput(data []byte) AgentRunResult {
	result, _ := parseClaudeStream(bytes.NewReader(data), nil, nil)
	return result
}

func parseClaudeStream(input io.Reader, copyTo *bytes.Buffer, onEvent func(AgentStreamUpdate)) (AgentRunResult, error) {
	var result AgentRunResult
	var answer strings.Builder
	var thought strings.Builder
	var tool strings.Builder
	state := &claudeParseState{seenToolUse: map[string]struct{}{}}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	parsedJSON := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if copyTo != nil {
			copyTo.WriteString(line)
			copyTo.WriteByte('\n')
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			answer.WriteString(line)
			answer.WriteByte('\n')
			state.addAnswerSegment(line)
			state.appendOrdered(card.SegmentText, line)
			emitStreamUpdate(onEvent, AgentStreamUpdate{
				Segments: []card.Segment{{Kind: card.SegmentText, Text: line}},
				Activity: streamActivityAnswering,
			})
			continue
		}
		parsedJSON = true
		emitStreamUpdate(onEvent, streamUpdateFromClaudeEvent(event))
		consumeClaudeEvent(event, &answer, &thought, &tool, &result, state)
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if !parsedJSON && strings.TrimSpace(answer.String()) == "" {
		if copyTo != nil {
			answer.Write(copyTo.Bytes())
			state.addAnswerSegment(string(copyTo.Bytes()))
			state.appendOrdered(card.SegmentText, string(copyTo.Bytes()))
		}
	}
	addSegment := func(kind card.SegmentKind, text string) {
		text = strings.TrimSpace(text)
		if text != "" {
			result.Segments = append(result.Segments, card.Segment{Kind: kind, Text: text})
		}
	}
	addSegment(card.SegmentText, answer.String())
	addSegment(card.SegmentThought, thought.String())
	addSegment(card.SegmentTool, tool.String())
	result.OrderedSegments = append([]card.Segment(nil), state.orderedSegments...)
	result.AnswerSegments = state.answerSegments
	result.ToolCallCount = len(state.seenToolUse)
	return result, nil
}

// claudeParseState 跟踪解析 Claude stream-json 时的跨行状态:
// 按 assistant message 边界收集的正文分段,以及去重后的 tool_use.id 集合。
type claudeParseState struct {
	answerSegments  []string
	orderedSegments []card.Segment
	seenToolUse     map[string]struct{}
}

func (s *claudeParseState) addAnswerSegment(text string) {
	if s == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text != "" {
		s.answerSegments = append(s.answerSegments, text)
	}
}

func (s *claudeParseState) appendOrdered(kind card.SegmentKind, text string) {
	s.appendOrderedSegment(card.Segment{Kind: kind, Text: text})
}

func (s *claudeParseState) appendOrderedSegment(segment card.Segment) {
	if s == nil || segment.Kind == card.SegmentThought {
		return
	}
	text := strings.TrimSpace(segment.Text)
	if text == "" {
		return
	}
	segment.Text = text
	s.orderedSegments = append(s.orderedSegments, segment)
}

func consumeClaudeEvent(event map[string]any, answer, thought, tool *strings.Builder, result *AgentRunResult, state *claudeParseState) {
	if id, ok := event["session_id"].(string); ok && result.AgentSessionID == "" {
		result.AgentSessionID = id
	}
	if model, ok := event["model"].(string); ok && result.Model == "" {
		result.Model = model
	}
	result.Tokens += tokensFromValue(event["usage"])
	if eventType, _ := event["type"].(string); eventType == "result" {
		if text, ok := event["result"].(string); ok && strings.TrimSpace(text) != "" && strings.TrimSpace(answer.String()) == "" {
			answer.WriteString(text)
			answer.WriteByte('\n')
			state.addAnswerSegment(text)
			state.appendOrdered(card.SegmentText, text)
		}
		return
	}
	message, _ := event["message"].(map[string]any)
	if message == nil {
		return
	}
	if id, ok := message["session_id"].(string); ok && result.AgentSessionID == "" {
		result.AgentSessionID = id
	}
	if model, ok := message["model"].(string); ok && result.Model == "" {
		result.Model = model
	}
	result.Tokens += tokensFromValue(message["usage"])
	// 只有 assistant role 的 message 才创建正文分段;user / tool-result message 的文本不进正文。
	role, _ := message["role"].(string)
	isAssistant := role == "" || role == "assistant"
	content, _ := message["content"].([]any)
	var messageText strings.Builder
	for _, raw := range content {
		block, _ := raw.(map[string]any)
		if block == nil {
			continue
		}
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			writeBlockText(answer, block)
			var ordered strings.Builder
			writeBlockText(&ordered, block)
			state.appendOrdered(card.SegmentText, ordered.String())
			if isAssistant {
				writeBlockText(&messageText, block)
			}
		case "thinking", "reasoning", "redacted_thinking":
			writeBlockText(thought, block)
		case "tool_use":
			writeToolUse(tool, block)
			var ordered strings.Builder
			writeToolUse(&ordered, block)
			state.appendOrderedSegment(card.Segment{Kind: card.SegmentTool, Text: ordered.String(), Tool: claudeToolUseMeta(block)})
			if id, ok := block["id"].(string); ok && id != "" {
				state.seenToolUse[id] = struct{}{}
			}
		case "tool_result":
			writeToolResult(tool, block)
			var ordered strings.Builder
			writeToolResult(&ordered, block)
			state.appendOrderedSegment(card.Segment{Kind: card.SegmentTool, Text: ordered.String(), Tool: claudeToolResultMeta(block)})
		}
	}
	// 同一个 assistant message 内的多个 text block 合并为一段回复(而非逐 block / 逐 delta 拆分)。
	if isAssistant {
		state.addAnswerSegment(messageText.String())
	}
}

func emitStreamUpdate(onEvent func(AgentStreamUpdate), update AgentStreamUpdate) {
	if onEvent == nil {
		return
	}
	if update.Model == "" && update.Tokens == 0 && update.AgentSessionID == "" && update.Activity == "" && len(update.Segments) == 0 {
		return
	}
	onEvent(update)
}

func streamUpdateFromClaudeEvent(event map[string]any) AgentStreamUpdate {
	var update AgentStreamUpdate
	eventType, _ := event["type"].(string)
	if id, ok := event["session_id"].(string); ok {
		update.AgentSessionID = id
	}
	if eventType == "stream_event" {
		if nested, _ := event["event"].(map[string]any); nested != nil {
			nestedUpdate := streamUpdateFromClaudeEvent(nested)
			nestedUpdate.PartialMessage = true
			if nestedUpdate.AgentSessionID == "" {
				nestedUpdate.AgentSessionID = update.AgentSessionID
			}
			return nestedUpdate
		}
	}
	if model, ok := event["model"].(string); ok {
		update.Model = model
	}
	update.Tokens += tokensFromValue(event["usage"])
	if eventType == "result" {
		return update
	}
	if message, _ := event["message"].(map[string]any); message != nil {
		if id, ok := message["session_id"].(string); ok && update.AgentSessionID == "" {
			update.AgentSessionID = id
		}
		if model, ok := message["model"].(string); ok && update.Model == "" {
			update.Model = model
		}
		update.Tokens += tokensFromValue(message["usage"])
		if content, _ := message["content"].([]any); len(content) > 0 {
			for _, raw := range content {
				if block, _ := raw.(map[string]any); block != nil {
					update.Segments = append(update.Segments, streamSegmentsFromBlock(block)...)
				}
			}
		}
		role, _ := message["role"].(string)
		if role == "" || role == "assistant" {
			update.AssistantSnapshot = len(update.Segments) > 0
			for _, segment := range update.Segments {
				if segment.Kind == card.SegmentText {
					update.AnswerSnapshot = true
					break
				}
			}
		}
		if len(update.Segments) > 0 {
			update.Activity = activityFromSegments(update.Segments)
		}
		return update
	}
	update.Segments = append(update.Segments, streamSegmentsFromTopLevelEvent(event)...)
	if eventType == "content_block_start" || eventType == "content_block_delta" {
		update.PartialMessage = true
	}
	if eventType == "content_block_delta" {
		update.Incremental = true
	}
	if len(update.Segments) > 0 {
		update.Activity = activityFromSegments(update.Segments)
	}
	return update
}

func streamSegmentsFromBlock(block map[string]any) []card.Segment {
	blockType, _ := block["type"].(string)
	if blockType == "tool_use" {
		var text strings.Builder
		writeToolUse(&text, block)
		return []card.Segment{{Kind: card.SegmentTool, Text: text.String(), Tool: claudeToolUseMeta(block)}}
	}
	if blockType == "tool_result" {
		var text strings.Builder
		writeToolResult(&text, block)
		return []card.Segment{{Kind: card.SegmentTool, Text: text.String(), Tool: claudeToolResultMeta(block)}}
	}
	switch blockType {
	case "text":
		return segmentFromText(card.SegmentText, firstString(block, "text", "content"))
	case "thinking", "reasoning", "redacted_thinking":
		return segmentFromText(card.SegmentThought, firstString(block, "thinking", "text", "content"))
	case "tool_use":
		var b strings.Builder
		writeToolUse(&b, block)
		return segmentFromText(card.SegmentTool, b.String())
	case "tool_result":
		var b strings.Builder
		writeToolResult(&b, block)
		return segmentFromText(card.SegmentTool, b.String())
	}
	return nil
}

func claudeToolUseMeta(block map[string]any) *card.ToolMeta {
	name, _ := block["name"].(string)
	return &card.ToolMeta{
		ID:      firstString(block, "id"),
		Name:    strings.TrimSpace(name),
		Summary: toolInputSummary(block["input"]),
		Phase:   "use",
	}
}

func claudeToolResultMeta(block map[string]any) *card.ToolMeta {
	isError, _ := block["is_error"].(bool)
	return &card.ToolMeta{ID: firstString(block, "tool_use_id"), Phase: "result", IsError: isError}
}

func toolInputSummary(value any) string {
	record, _ := value.(map[string]any)
	for _, key := range []string{"command", "file_path", "path", "query", "url"} {
		if text := firstString(record, key); text != "" {
			return text
		}
	}
	return ""
}

func streamSegmentsFromTopLevelEvent(event map[string]any) []card.Segment {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "content_block_start":
		if block, _ := event["content_block"].(map[string]any); block != nil {
			return streamSegmentsFromBlock(block)
		}
	case "content_block_delta":
		if delta, _ := event["delta"].(map[string]any); delta != nil {
			return streamSegmentsFromDelta(delta)
		}
	case "thinking", "reasoning", "reasoning_content":
		return segmentFromText(card.SegmentThought, firstString(event, "reasoning_content", "thinking", "content", "delta", "text"))
	case "text":
		return segmentFromText(card.SegmentText, firstString(event, "text", "content", "delta"))
	case "tool_use", "tool_use_delta":
		return segmentFromText(card.SegmentTool, firstString(event, "content", "delta", "tool_input", "input"))
	case "tool_result":
		return segmentFromText(card.SegmentTool, firstString(event, "tool_result", "content", "result"))
	}
	if text := firstString(event, "reasoning_content", "thinking"); text != "" {
		return segmentFromText(card.SegmentThought, text)
	}
	if text := firstString(event, "text", "content", "delta"); text != "" {
		return segmentFromText(card.SegmentText, text)
	}
	return nil
}

func streamSegmentsFromDelta(delta map[string]any) []card.Segment {
	deltaType, _ := delta["type"].(string)
	switch deltaType {
	case "text_delta":
		return segmentFromText(card.SegmentText, firstString(delta, "text"))
	case "thinking_delta", "reasoning_delta":
		return segmentFromText(card.SegmentThought, firstString(delta, "thinking", "text", "reasoning"))
	case "input_json_delta":
		return segmentFromText(card.SegmentTool, firstString(delta, "partial_json"))
	}
	if text := firstString(delta, "thinking", "reasoning"); text != "" {
		return segmentFromText(card.SegmentThought, text)
	}
	if text := firstString(delta, "text", "content", "delta"); text != "" {
		return segmentFromText(card.SegmentText, text)
	}
	if text := firstString(delta, "partial_json"); text != "" {
		return segmentFromText(card.SegmentTool, text)
	}
	return nil
}

func segmentFromText(kind card.SegmentKind, text string) []card.Segment {
	if text == "" {
		return nil
	}
	return []card.Segment{{Kind: kind, Text: text}}
}

func activityFromSegments(segments []card.Segment) string {
	for _, segment := range segments {
		if segment.Kind == card.SegmentTool {
			return streamActivityTool
		}
	}
	for _, segment := range segments {
		if segment.Kind == card.SegmentText {
			return streamActivityAnswering
		}
	}
	for _, segment := range segments {
		if segment.Kind == card.SegmentThought {
			return streamActivityReasoning
		}
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := m[key].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func writeBlockText(b *strings.Builder, block map[string]any) {
	for _, key := range []string{"text", "thinking", "content"} {
		if text, ok := block[key].(string); ok && strings.TrimSpace(text) != "" {
			b.WriteString(text)
			b.WriteByte('\n')
			return
		}
	}
}

// writeToolUse / writeToolResult 把工具事件渲染成独立的 Markdown 块。
// 关键排版约束(避免飞书卡片里代码围栏与列表项粘连):
//   - 每个条目之间留一个空行(用 toolBlockSeparator 保证);
//   - 代码围栏 ``` 前后各有一个空行,否则围栏不被识别、与上一行粘连;
//   - result 文本另起一行,不跟在 id 行尾。
func writeToolUse(b *strings.Builder, block map[string]any) {
	toolBlockSeparator(b)
	name, _ := block["name"].(string)
	if name == "" {
		name = "tool_use"
	}
	b.WriteString("- ")
	b.WriteString(name)
	if id, ok := block["id"].(string); ok && id != "" {
		b.WriteString(" `")
		b.WriteString(id)
		b.WriteString("`")
	}
	if input, ok := block["input"]; ok {
		if payload, err := json.Marshal(input); err == nil && len(payload) > 0 {
			// 代码围栏前需要空行,围栏后另起一行。
			b.WriteString("\n\n```json\n")
			b.Write(payload)
			b.WriteString("\n```")
		}
	}
}

func writeToolResult(b *strings.Builder, block map[string]any) {
	toolBlockSeparator(b)
	b.WriteString("- tool_result")
	if id, ok := block["tool_use_id"].(string); ok && id != "" {
		b.WriteString(" `")
		b.WriteString(id)
		b.WriteString("`")
	}
	if text := toolResultText(block); text != "" {
		// result 文本另起一行,并用代码块包裹,避免多行输出破坏列表结构。
		b.WriteString("\n\n```\n")
		b.WriteString(text)
		b.WriteString("\n```")
	}
}

// toolBlockSeparator 在已有内容后插入一个空行,使前后工具条目之间保持段落分隔。
func toolBlockSeparator(b *strings.Builder) {
	if b.Len() == 0 {
		return
	}
	existing := b.String()
	trimmed := strings.TrimRight(existing, "\n")
	b.Reset()
	b.WriteString(trimmed)
	b.WriteString("\n\n")
}

// toolResultText 提取 tool_result 的文本内容(不追加多余换行)。
func toolResultText(block map[string]any) string {
	for _, key := range []string{"text", "thinking", "content"} {
		if text, ok := block[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func tokensFromValue(value any) int {
	switch v := value.(type) {
	case map[string]any:
		total := 0
		for key, item := range v {
			if strings.HasSuffix(key, "tokens") {
				total += intFromJSONNumber(item)
				continue
			}
			total += tokensFromValue(item)
		}
		return total
	case []any:
		total := 0
		for _, item := range v {
			total += tokensFromValue(item)
		}
		return total
	default:
		return 0
	}
}

func intFromJSONNumber(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
