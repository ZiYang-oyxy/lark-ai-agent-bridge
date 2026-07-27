package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/actiongrant"
	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/bridge"
	"lark-agent-bridge/internal/bridgeinstructions"
	"lark-agent-bridge/internal/buildinfo"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/devmode"
	"lark-agent-bridge/internal/doctor"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/media"
	"lark-agent-bridge/internal/participation"
	"lark-agent-bridge/internal/reply"
	"lark-agent-bridge/internal/schedule"
	"lark-agent-bridge/internal/session"
	bridgeupdate "lark-agent-bridge/internal/update"
	"lark-agent-bridge/internal/workspace"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "version":
		return runVersion(args[1:], os.Stdout)
	case "doctor":
		return runDoctor(args[1:])
	case "simulate":
		return runSimulate(args[1:])
	case "simulate-action":
		return runSimulateAction(args[1:])
	case "serve":
		return runServe(args[1:])
	case "schedule":
		if len(args) < 2 || args[1] != "propose" {
			return errors.New("usage: lark-agent-bridge schedule propose [flags]")
		}
		return runSchedulePropose(args[2:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runVersion(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print build information as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: lark-agent-bridge version [--json]")
	}
	info := buildinfo.Current()
	if *jsonOutput {
		return json.NewEncoder(out).Encode(info)
	}
	displayVersion := info.Version
	if buildinfo.IsRelease() {
		displayVersion = "v" + displayVersion
	}
	_, err := fmt.Fprintf(out, "lark-agent-bridge %s commit=%s built=%s %s/%s\n", displayVersion, info.Commit, info.BuildTime, info.GOOS, info.GOARCH)
	return err
}

func runSchedulePropose(args []string) error {
	fs := flag.NewFlagSet("schedule propose", flag.ContinueOnError)
	kindValue := fs.String("kind", "", "cron or timer")
	cronExpr := fs.String("cron", "", "standard five-field cron expression")
	atValue := fs.String("at", "", "one-shot RFC3339 timestamp")
	timezone := fs.String("timezone", "Asia/Shanghai", "IANA timezone")
	description := fs.String("description", "", "short human-readable label")
	prompt := fs.String("prompt", "", "Agent task prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	socketPath := strings.TrimSpace(os.Getenv("LAB_SCHEDULE_SOCKET"))
	token := strings.TrimSpace(os.Getenv("LAB_SCHEDULE_TOKEN"))
	if socketPath == "" || token == "" {
		return errors.New("LAB_SCHEDULE_SOCKET and LAB_SCHEDULE_TOKEN are required")
	}
	kind := schedule.Kind(strings.ToLower(strings.TrimSpace(*kindValue)))
	proposal := schedule.Proposal{
		Kind: kind, CronExpr: strings.TrimSpace(*cronExpr), Timezone: strings.TrimSpace(*timezone),
		Description: strings.TrimSpace(*description), Prompt: strings.TrimSpace(*prompt),
	}
	if *atValue != "" {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(*atValue))
		if err != nil {
			return fmt.Errorf("parse --at as RFC3339: %w", err)
		}
		proposal.ScheduledAt = at
	}
	if kind == schedule.KindCron && (proposal.CronExpr == "" || !proposal.ScheduledAt.IsZero()) {
		return errors.New("cron proposal requires --cron and forbids --at")
	}
	if kind == schedule.KindTimer && (proposal.CronExpr != "" || proposal.ScheduledAt.IsZero()) {
		return errors.New("timer proposal requires --at and forbids --cron")
	}
	if kind != schedule.KindCron && kind != schedule.KindTimer {
		return errors.New("--kind must be cron or timer")
	}
	response, err := (schedule.ProposeClient{SocketPath: socketPath, Timeout: 5 * time.Second}).Propose(context.Background(), schedule.ProposeRequest{Token: token, Proposal: proposal})
	if err != nil {
		return err
	}
	fmt.Printf("Draft %s proposed: %s. Waiting for user confirmation.\n", response.DraftID, response.Description)
	return nil
}

func runSimulateAction(args []string) error {
	fs := flag.NewFlagSet("simulate-action", flag.ContinueOnError)
	cfg, err := config.LoadFromEnvStrict()
	if err != nil {
		return err
	}
	actionID := fs.String("action", "stop", "action id")
	value := fs.String("value", "", "action value")
	sessionID := fs.String("session", "claude:chat-demo:message:local-id", "session id")
	actor := fs.String("actor", "user-demo", "actor id")
	primeText := fs.String("prime-text", "/new hello", "message to create a session before action; empty disables")
	primeIsGroup := fs.Bool("prime-is-group", false, "prime message is a group message (needed for helpContext write path)")
	primeChatID := fs.String("prime-chat-id", "chat-demo", "chat id used for the prime message")
	defaultWorkDir := fs.String("default-workdir", cfg.DefaultWorkDir, "default workdir for messages without --workdir")
	chatID := fs.String("chat-id", "", "ActionRequest.ChatID (target chat for the action)")
	openMessageID := fs.String("open-message-id", "", "ActionRequest.OpenMessageID (card message id to close/edit)")
	formValues := fs.String("form-values", "", "ActionRequest.FormValues as JSON object of string->string (e.g. {\"agent\":\"claude\"})")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyDefaultWorkDir(&cfg, *defaultWorkDir); err != nil {
		return err
	}
	var parsedFormValues map[string]string
	if *formValues != "" {
		if err := json.Unmarshal([]byte(*formValues), &parsedFormValues); err != nil {
			return fmt.Errorf("--form-values: %w", err)
		}
	}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	svc := bridge.NewService(cfg, renderer, simulateRunner{}, recorder)
	loadAgentsInto(svc, cfg, recorder)
	// 装配 PreferenceStore:config.save / local_config.save / agent_mode.save 等分支
	// 需要它写偏好。store 路径由 cfg.PreferenceStorePath 决定(通过 E2E_PREFERENCE_STORE env),
	// runner 会为每个测试步骤分配独立 workdir,天然隔离。
	if cfg.PreferenceStorePath != "" {
		preferences, err := config.OpenPreferenceStore(cfg.PreferenceStorePath, runtimePreferenceDefaults(cfg), cfg.AllowedModels, svc.Agents.Agents...)
		if err != nil {
			return fmt.Errorf("open preference store: %w", err)
		}
		svc.Preferences = preferences
	}
	// 装配 DevMode / Workspace store:/.devel、/cd、/ws 的成功路径需要这两个 store。
	// runner 已把两个路径重定向到 tmp workdir(见 applyDefaultWorkDir),不污染实机。
	if cfg.DevModeStorePath != "" {
		devModeStore, err := devmode.OpenStore(cfg.DevModeStorePath)
		if err != nil {
			return fmt.Errorf("open dev-mode store: %w", err)
		}
		svc.DevMode = devModeStore
	}
	if cfg.WorkspaceStorePath != "" {
		workspaces, err := workspace.OpenWorkspaceStore(cfg.WorkspaceStorePath)
		if err != nil {
			return fmt.Errorf("open workspace store: %w", err)
		}
		svc.Workspaces = workspaces
	}
	if *primeText != "" {
		msg := bridge.Message{
			ID:                 "local-id",
			ChatID:             *primeChatID,
			Sender:             *actor,
			Text:               *primeText,
			IsGroup:            *primeIsGroup,
			Mentioned:          true,
			ExplicitBotMention: true,
			Time:               time.Now(),
		}
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			return err
		}
		if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
			return err
		}
	}
	if err := svc.HandleAction(context.Background(), bridge.ActionRequest{
		SessionID:     *sessionID,
		ActionID:      *actionID,
		Value:         *value,
		Actor:         *actor,
		ChatID:        *chatID,
		OpenMessageID: *openMessageID,
		FormValues:    parsedFormValues,
	}); err != nil {
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	out := struct {
		Events []card.Event  `json:"events"`
		Audit  []audit.Event `json:"audit"`
	}{Events: renderer.Events(), Audit: recorder.Events()}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	cfg, err := config.LoadFromEnvStrict()
	if err != nil {
		return err
	}
	defaultWorkDir := fs.String("default-workdir", cfg.DefaultWorkDir, "default workdir for messages without --workdir")
	strict := fs.Bool("strict", false, "treat wrapper preflight warnings as failures")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyDefaultWorkDir(&cfg, *defaultWorkDir); err != nil {
		return err
	}
	checks := doctor.RunWithOptions(context.Background(), cfg, doctor.RunOptions{
		WrapperPreflight: true,
		Strict:           *strict,
		PreflightTimeout: 20 * time.Second,
	})
	fmt.Println(doctor.Summary(checks))
	if *strict {
		for _, check := range checks {
			if !check.OK {
				return errors.New("doctor strict verification failed")
			}
		}
	}
	return nil
}

func runSimulate(args []string) error {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	cfg, err := config.LoadFromEnvStrict()
	if err != nil {
		return err
	}
	text := fs.String("text", "/help", "message text")
	chat := fs.String("chat", "chat-demo", "chat id")
	thread := fs.String("thread", "", "thread id")
	sender := fs.String("sender", "user-demo", "sender id")
	senderType := fs.String("sender-type", "user", "sender type: user or bot")
	group := fs.Bool("group", false, "simulate group chat")
	mentioned := fs.Bool("mentioned", true, "whether bot was mentioned")
	messageType := fs.String("message-type", "text", "Feishu message type: text or post (post triggers topic precreate under topic conversation mode)")
	fakeThread := fs.String("fake-thread", "", "if set, wire an in-memory Sender that returns this thread_id for SendReply so simulate can exercise the topic precreate path without Feishu")
	timeoutNow := fs.Bool("timeout-now", false, "immediately trigger pending confirmation timeout after message handling")
	defaultWorkDir := fs.String("default-workdir", cfg.DefaultWorkDir, "default workdir for messages without --workdir")
	quoteText := fs.String("quote-text", "", "if set, simulate a Feishu quoted (replied-to) message with this text body; also sets --parent-id when unspecified")
	quoteSender := fs.String("quote-sender", "", "sender open_id of the simulated quoted message; empty leaves the generic label")
	quoteSenderType := fs.String("quote-sender-type", "", "sender_type of the simulated quoted message: user / app / anonymous / empty (unknown)")
	parentID := fs.String("parent-id", "", "explicit parent_id for the simulated inbound message; defaults to a fake id when --quote-text is set")
	var nextMessages stringList
	fs.Var(&nextMessages, "next-text", "additional message text for the same chat, repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyDefaultWorkDir(&cfg, *defaultWorkDir); err != nil {
		return err
	}
	if *senderType != "user" && *senderType != "bot" {
		return fmt.Errorf("sender-type must be user or bot")
	}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	baseMsg := bridge.Message{
		ChatID:             *chat,
		ThreadID:           *thread,
		Sender:             *sender,
		SenderType:         *senderType,
		Text:               *text,
		MessageType:        *messageType,
		IsGroup:            *group,
		Mentioned:          *mentioned,
		ExplicitBotMention: *mentioned,
		Time:               time.Now(),
	}
	svc := bridge.NewService(cfg, renderer, simulateRunner{}, recorder)
	topics, err := openParticipationStore(cfg)
	if err != nil {
		return err
	}
	svc.TopicParticipation = topics
	if strings.TrimSpace(*fakeThread) != "" {
		svc.Notifier = &simulateFakeSender{threadID: *fakeThread}
		svc.TopicAliases = bridge.NewTopicAliasStore()
	}
	// simulate 侧的引用消息注入：--quote-text 非空时装配 in-memory MessageFetcher，
	// 让 bridge.resolveQuotedMessage 拿到我们预设的 QuotedMessage 而不走 SDK。
	// 用来在无飞书链路下断言主体身份边框的渲染（L2）。
	simulateParentID := strings.TrimSpace(*parentID)
	if strings.TrimSpace(*quoteText) != "" {
		if simulateParentID == "" {
			simulateParentID = fmt.Sprintf("om_simulate_parent_%d", time.Now().UnixNano())
		}
		svc.MessageFetcher = simulateQuotedFetcher{
			text:       *quoteText,
			senderID:   strings.TrimSpace(*quoteSender),
			senderType: strings.TrimSpace(*quoteSenderType),
		}
	}
	loadAgentsInto(svc, cfg, recorder)
	// 装配 PreferenceStore:任何走 handleActionCommand 的白名单动作(如 help.open_config /
	// local_config.edit)以及 /config /local-config /agent-mode 命令自身都依赖它。
	// runner 会为每个测试步骤分配独立 workdir 隔离 store 文件。
	if cfg.PreferenceStorePath != "" {
		preferences, err := config.OpenPreferenceStore(cfg.PreferenceStorePath, runtimePreferenceDefaults(cfg), cfg.AllowedModels, svc.Agents.Agents...)
		if err != nil {
			return fmt.Errorf("open preference store: %w", err)
		}
		svc.Preferences = preferences
	}
	// 装配 DevMode / Workspace store:/.devel、/cd、/ws 的成功路径需要这两个 store。
	// runner 已把两个路径重定向到 tmp workdir(见 applyDefaultWorkDir),不污染实机。
	if cfg.DevModeStorePath != "" {
		devModeStore, err := devmode.OpenStore(cfg.DevModeStorePath)
		if err != nil {
			return fmt.Errorf("open dev-mode store: %w", err)
		}
		svc.DevMode = devModeStore
	}
	if cfg.WorkspaceStorePath != "" {
		workspaces, err := workspace.OpenWorkspaceStore(cfg.WorkspaceStorePath)
		if err != nil {
			return fmt.Errorf("open workspace store: %w", err)
		}
		svc.Workspaces = workspaces
	}
	msg := bridge.Message{
		ID:                 fmt.Sprintf("local-%d", time.Now().UnixNano()),
		ChatID:             *chat,
		ThreadID:           *thread,
		Sender:             *sender,
		SenderType:         *senderType,
		Text:               *text,
		MessageType:        *messageType,
		IsGroup:            *group,
		Mentioned:          *mentioned,
		ExplicitBotMention: *mentioned,
		Time:               baseMsg.Time,
		ParentID:           simulateParentID,
	}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		return err
	}
	for _, nextText := range nextMessages {
		nextMsg := bridge.Message{
			ID:                 fmt.Sprintf("local-%d", time.Now().UnixNano()),
			ChatID:             *chat,
			ThreadID:           *thread,
			Sender:             *sender,
			SenderType:         *senderType,
			Text:               nextText,
			MessageType:        *messageType,
			IsGroup:            *group,
			Mentioned:          *mentioned,
			ExplicitBotMention: *mentioned,
			Time:               time.Now(),
		}
		if err := svc.HandleMessage(context.Background(), nextMsg); err != nil {
			return err
		}
	}
	if *timeoutNow {
		if err := svc.RenderPendingRunTimeouts(time.Now().Add(cfg.InteractionTimeout + time.Second)); err != nil {
			return err
		}
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	out := struct {
		Events []card.Event  `json:"events"`
		Audit  []audit.Event `json:"audit"`
	}{Events: renderer.Events(), Audit: recorder.Events()}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfg, err := config.LoadFromEnvStrict()
	if err != nil {
		return err
	}
	defaultWorkDir := fs.String("default-workdir", cfg.DefaultWorkDir, "default workdir for messages without --workdir")
	resetStore := fs.String("reset-store", "", "comma-separated list of store file names to delete before start (e.g. sessions.json,replies.json). Use \"all\" to reset every known state store. Config is never reset.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyDefaultWorkDir(&cfg, *defaultWorkDir); err != nil {
		return err
	}
	if err := applyResetStore(cfg, *resetStore); err != nil {
		return err
	}
	// 凭据白名单：优先接受 persist 前缀 LAB_LARK_*，回落到旧的 LARK_* 以兼容真机部署。
	// secret 只在内存持有，绝不落盘（Config.Config 不序列化）。
	appID := firstNonEmpty(os.Getenv("LAB_LARK_APP_ID"), os.Getenv("LARK_APP_ID"))
	appSecret := firstNonEmpty(os.Getenv("LAB_LARK_APP_SECRET"), os.Getenv("LARK_APP_SECRET"))
	if appID == "" || appSecret == "" {
		return fmt.Errorf("LAB_LARK_APP_ID and LAB_LARK_APP_SECRET are required for serve (旧 LARK_APP_ID/SECRET 仍兼容)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	botOpenID := firstNonEmpty(os.Getenv("LAB_LARK_BOT_OPEN_ID"), os.Getenv("LARK_BOT_OPEN_ID"))
	if botOpenID == "" {
		var err error
		botOpenID, err = feishu.FetchBotOpenID(ctx, appID, appSecret)
		if err != nil {
			return fmt.Errorf("fetch feishu bot open_id: %w", err)
		}
	}
	tokens := feishu.NewTenantTokenSource(appID, appSecret)
	cardClient := feishu.NewCardKitClientWithTokenSource(tokens)
	sender := feishu.NewSDKSender(appID, appSecret)
	recorder, closeAudit, err := newServeAuditRecorder(cfg)
	if err != nil {
		return err
	}
	defer closeAudit()
	instructions, err := bridgeinstructions.NewRuntime()
	if err != nil {
		return fmt.Errorf("initialize bridge instructions: %w", err)
	}
	defer instructions.Close()
	sessions, notices, err := openSessionState(cfg)
	if err != nil {
		return err
	}
	agents, agentsErr := config.LoadAgentsConfig(cfg.AgentsConfigPath)
	if agentsErr != nil {
		recorder.Record("system", "agents_config_fallback", "", agentsErr.Error())
	}
	preferences, err := config.OpenPreferenceStore(cfg.PreferenceStorePath, runtimePreferenceDefaults(cfg), cfg.AllowedModels, agents.Agents...)
	if err != nil {
		return fmt.Errorf("open runtime preference store: %w", err)
	}
	topicStore, err := openParticipationStore(cfg)
	if err != nil {
		return err
	}
	accessStore, err := access.OpenStore(cfg.AccessStorePath)
	if err != nil {
		return fmt.Errorf("open access store: %w", err)
	}
	actionGrants, err := actiongrant.OpenStore(cfg.ActionGrantStorePath)
	if err != nil {
		return fmt.Errorf("open action grant store: %w", err)
	}
	devModeStore, err := devmode.OpenStore(cfg.DevModeStorePath)
	if err != nil {
		return fmt.Errorf("open dev-mode store: %w", err)
	}
	workspaces, err := workspace.OpenWorkspaceStore(cfg.WorkspaceStorePath)
	if err != nil {
		return fmt.Errorf("open workspace store: %w", err)
	}
	replies, err := reply.OpenStore(cfg.ReplyStorePath)
	if err != nil {
		return fmt.Errorf("open reply store: %w", err)
	}
	sequenceJournal, err := bridge.NewNativeSequenceJournal(filepath.Join(filepath.Dir(cfg.SessionStorePath), "native-sequence-journal.json"), sessions, replies)
	if err != nil {
		return fmt.Errorf("open native sequence journal: %w", err)
	}
	topicAliases, err := bridge.OpenTopicAliasStore(cfg.TopicAliasStorePath, 10_000)
	if err != nil {
		return fmt.Errorf("open topic alias store: %w", err)
	}
	topicJoinObserver := bridge.NewTopicJoinObserver(recorder, topicStore)
	topicJoinObserver.Aliases = topicAliases
	cardRouter := newServeCardRouter(cardClient, topicJoinObserver, sequenceJournal)
	renderer := feishu.NewReactionCardRenderer(sender, cardRouter)
	runner := &bridge.CLIExecRunner{Instructions: instructions}
	svc := bridge.NewServiceWithSessions(cfg, renderer, runner, recorder, sessions, notices)
	svc.Updates = newRuntimeUpdateManager(cfg, devModeStore)
	svc.Agents = agents
	svc.Preferences = preferences
	svc.TopicParticipation = topicStore
	svc.TopicAliases = topicAliases
	svc.Access = accessStore
	svc.ActionGrants = actionGrants
	svc.DevMode = devModeStore
	svc.PrereleaseManifestURL = cfg.UpdatePrereleaseManifestURL
	svc.Workspaces = workspaces
	svc.AccessControls = access.NewRuntimeControls()
	svc.AccessAppID = appID
	accessInfo := &feishu.AccessInfoClient{Tokens: tokens}
	svc.AccessInfo = accessInfo
	svc.ScopeInspector = accessInfo
	svc.ScopeGrants = feishu.NewSDKScopeGrantProvider()
	svc.BotOpenID = botOpenID
	if err := access.RefreshOwner(ctx, svc.AccessControls, svc.AccessInfo, appID); err != nil {
		recorder.Record("system", "owner_refresh_failed", "", err.Error())
	}
	svc.Replies = replies
	svc.SequenceResolver = sequenceJournal
	svc.CardTarget = cardRouter
	svc.Reactions = sender
	svc.OutputImages = sender
	svc.MessageDeleter = sender
	svc.MessageFetcher = quotedMessageFetcher{sender: sender}
	svc.Notifier = sender
	actionGateway := bridge.ActionGateway{Service: svc, Fencer: cardRouter}
	actionHandler, callbackHandler := newServeActionTransports(actionGateway, cfg.CardMaxChars)
	svc.ProcessRecoveryNotices(ctx)
	// 若本次启动是一次成功自升级的结果(env 携带升级上下文),往原对话回一条升级成功消息。
	go svc.NotifyUpgradeSuccessIfPending(ctx)
	mediaWiring := newServeMedia(cfg, tokens)
	svc.MediaCache = mediaWiring.cache
	svc.MediaDownloader = mediaWiring.downloader
	svc.MediaGC = mediaWiring.gc
	svc.SweepMediaCacheStartup()
	scheduleStore, err := schedule.NewStore(cfg.ScheduleStorePath)
	if err != nil {
		return fmt.Errorf("open schedule store: %w", err)
	}
	scheduleContexts := schedule.NewContextRegistry(cfg.ScheduleTimeout)
	scheduleControl := schedule.NewControlServer(cfg.ScheduleSocketPath, scheduleStore, scheduleContexts, schedule.ControlConfig{DraftTTL: cfg.ScheduleDraftTTL})
	scheduleEngine := schedule.NewEngine(scheduleStore, svc, svc, schedule.EngineConfig{
		CatchUpWindow: cfg.ScheduleCatchUp, ExecutionTimeout: cfg.ScheduleTimeout, HistoryRetention: cfg.ScheduleRetention,
	})
	svc.Schedules = scheduleStore
	svc.Scheduler = scheduleEngine
	svc.ScheduleContexts = scheduleContexts
	svc.ScheduleSocket = cfg.ScheduleSocketPath
	if err := scheduleControl.Start(ctx); err != nil {
		return fmt.Errorf("start schedule control: %w", err)
	}
	if err := scheduleEngine.Start(ctx); err != nil {
		_ = scheduleControl.Close()
		return fmt.Errorf("start schedule engine: %w", err)
	}
	go svc.RunAccessRefresh(ctx)
	client := feishu.NewLongConnClient(feishu.LongConnConfig{
		AppID:         appID,
		AppSecret:     appSecret,
		BotOpenID:     botOpenID,
		ActionHandler: actionHandler,
		MessageRecalledHandler: func(ctx context.Context, recall feishu.RecalledMessage) error {
			return svc.HandleMessageRecalled(ctx, bridge.MessageRecall{
				MessageID:  recall.MessageID,
				ChatID:     recall.ChatID,
				RecallType: recall.RecallType,
				Time:       recall.OccurredAt,
			})
		},
	})
	if addr := os.Getenv("E2E_CALLBACK_ADDR"); addr != "" {
		server := &http.Server{Addr: addr, Handler: callbackHandler}
		go func() {
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintln(os.Stderr, "callback server:", err)
				stop()
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		}()
	}
	svc.StartBackgroundLoops(ctx, cfg.CardUpdateEvery)
	longConnErr := runLongConnUntilStopped(ctx, client, func(ctx context.Context, in feishu.InboundMessage) error {
		msg := bridge.MessageFromFeishu(in)
		return svc.HandleMessage(ctx, msg)
	})
	controlErr := scheduleControl.Close()
	scheduleEngine.Stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	shutdownErr := svc.Shutdown(shutdownCtx)
	return errors.Join(longConnErr, controlErr, shutdownErr)
}

func newRuntimeUpdateManager(cfg config.Config, devMode *devmode.Store) *bridgeupdate.Manager {
	return newRuntimeUpdateManagerTo(cfg, devMode, os.Stderr)
}

// newRuntimeUpdateManagerTo is the testable core of newRuntimeUpdateManager:
// startup warnings go to warnOut so tests can capture them.
//
// Warning contract (only fires when stable is configured — an unconfigured
// bridge already fails --help / self-upgrade with a clear message elsewhere):
// if LAB_UPDATE_MANIFEST_URL is set but LAB_UPDATE_PRERELEASE_MANIFEST_URL is
// not, /help升级到 rc 不可用。历史踩坑:supervisor 手工 nohup 起,只 export 了
// stable 一条,发了 rc 后用户 /help 看不到,反复追根因才发现根本没订阅 rc 通道。
// 这条 warning 让"配了 stable 却漏了 prerelease"在启动时立刻可见,不必等到发
// rc 才发现。
func newRuntimeUpdateManagerTo(cfg config.Config, devMode *devmode.Store, warnOut io.Writer) *bridgeupdate.Manager {
	if strings.TrimSpace(cfg.UpdateManifestURL) == "" {
		return nil
	}
	if strings.TrimSpace(cfg.UpdatePrereleaseManifestURL) == "" && warnOut != nil {
		fmt.Fprintln(warnOut, "[warn] LAB_UPDATE_PRERELEASE_MANIFEST_URL 未配置：/help 升级卡将只跟踪 stable 通道，rc 版本不可见。若需接收 rc，配置该环境变量并重启，然后发送 /.devel 1 开启开发者模式。")
	}
	client := bridgeupdate.NewClient(cfg.UpdateManifestURL, nil)
	client.PrereleaseURL = cfg.UpdatePrereleaseManifestURL
	client.Prerelease = devMode.Prerelease
	return &bridgeupdate.Manager{Client: client, Installer: bridgeupdate.Installer{Stage: client.Stage}}
}

func openSessionState(cfg config.Config) (*session.Manager, []session.RecoveryNotice, error) {
	sessions := session.NewManagerWithStoreVersion(cfg.SessionStorePath, bridgeinstructions.CurrentVersion)
	notices, err := sessions.Restore()
	if err != nil {
		return nil, nil, fmt.Errorf("restore session store: %w", err)
	}
	catalog, err := session.OpenCatalog(session.CatalogPath(cfg.SessionStorePath))
	if err != nil {
		return nil, nil, fmt.Errorf("open session catalog: %w", err)
	}
	sessions.AttachCatalog(catalog)
	return sessions, notices, nil
}

func runtimePreferenceDefaults(cfg config.Config) config.RuntimePreference {
	return config.RuntimePreference{
		Model:              cfg.Model,
		Effort:             cfg.Effort,
		ReplyMode:          cfg.ReplyMode,
		AppendOverflowMode: cfg.AppendOverflowMode,
		ConversationMode:   cfg.ConversationMode,
		TopicSeedMode:      cfg.TopicSeedMode,
		GroupMessageMode:   cfg.GroupMessageMode,
		RespondToBots:      cfg.RespondToBots,
	}
}

func openParticipationStore(cfg config.Config) (*participation.Store, error) {
	store, err := participation.OpenStore(cfg.ParticipatedTopicsStorePath, 10_000)
	if err != nil {
		return nil, fmt.Errorf("open participation store: %w", err)
	}
	return store, nil
}

func newServeCardRouter(client feishu.CardKitClientAPI, observer feishu.CardKitRenderObserver, journal feishu.NativeSequenceJournal) *feishu.CardKitRouterRenderer {
	return feishu.NewCardKitRouterRendererWithObserverAndJournal(client, observer, journal)
}

func newServeActionTransports(gateway bridge.ActionGateway, cardMaxChars int) (func(context.Context, feishu.CardAction) (*feishu.CardActionResponse, error), http.Handler) {
	actionHandler := func(ctx context.Context, action feishu.CardAction) (*feishu.CardActionResponse, error) {
		result, err := gateway.Handle(ctx, actionRequestFromFeishu(action))
		if err != nil {
			return nil, err
		}
		prepared, err := result.PrepareCard(cardMaxChars)
		if err != nil {
			result.CancelDeferred()
			return nil, err
		}
		response := &feishu.CardActionResponse{Card: prepared.PayloadCopy()}
		result.StartDeferred()
		return response, nil
	}
	return actionHandler, bridge.NewCallbackHTTPHandler(gateway)
}

func actionRequestFromFeishu(action feishu.CardAction) bridge.ActionRequest {
	formValues := make(map[string]string, len(action.FormValues))
	for key, value := range action.FormValues {
		formValues[key] = value
	}
	if len(formValues) == 0 {
		formValues = nil
	}
	return bridge.ActionRequest{
		SessionID:     action.SessionID,
		ActionID:      action.ActionID,
		Value:         action.Value,
		Actor:         action.Actor,
		ChatID:        action.ChatID,
		OpenMessageID: action.OpenMessageID,
		FormValues:    formValues,
		GrantID:       action.GrantID,
	}
}

func runLongConnUntilStopped(ctx context.Context, client feishu.LongConnClient, handler func(context.Context, feishu.InboundMessage) error) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Run(ctx, handler)
	}()
	select {
	case err := <-errCh:
		if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
			return nil
		}
		return err
	case <-ctx.Done():
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		default:
		}
		return nil
	}
}

func printUsage() {
	fmt.Println(`lark-agent-bridge

Usage:
  lark-agent-bridge version [--json]
  lark-agent-bridge doctor [--strict] [--default-workdir /path]
  lark-agent-bridge simulate [--default-workdir /path] -text "/new hello"
  lark-agent-bridge simulate -text "/new first" -next-text "/new second"
  lark-agent-bridge simulate -text "/new --workdir /tmp/missing hello" -timeout-now
  lark-agent-bridge simulate-action -action stop -session claude:chat-demo:message:local-id
  lark-agent-bridge serve [--default-workdir /path]
  lark-agent-bridge schedule propose --kind cron|timer ...   # Agent-facing only

Environment:
  E2E_DEFAULT_AGENT      defaults to claude
  E2E_CLAUDE_BIN         claude executable path; defaults to "claude" (resolved via PATH).
                         Set to an absolute path such as /path/to/lark-agent-workspace/bin/claude
                         to launch Claude through a workspace wrapper.
  E2E_DEFAULT_WORKDIR    defaults to current directory
  E2E_CARD_UPDATE_MS     defaults to 800
  E2E_CARD_HEARTBEAT_SEC defaults to 5; refreshes a quiet running card
  E2E_CARD_MAX_CHARS     defaults to 12000
  E2E_CARD_MIN_DELTA_CHARS defaults to 30
  E2E_CARD_PREVIEW_MAX_CHARS defaults to 2000
  E2E_REPLY_MODE         Coder=append, Worker=append-clean-card, or Singleton=latest-card
  E2E_REPLY_STORE        defaults to <workdir>/.lark-agent-bridge/replies.json
  E2E_ACCESS_STORE       defaults to <workdir>/.lark-agent-bridge/access.json
  E2E_TOPIC_ALIAS_STORE  defaults to <workdir>/.lark-agent-bridge/topic-aliases.json
  E2E_SCHEDULE_STORE     defaults to <workdir>/.lark-agent-bridge/schedules.json
  E2E_SCHEDULE_SOCKET    defaults to a private per-workspace Unix socket
  E2E_SCHEDULE_DRAFT_TTL_MIN defaults to 10
  E2E_SCHEDULE_CATCHUP_MIN defaults to 5
  E2E_SCHEDULE_TIMEOUT_MIN defaults to 30
  E2E_SCHEDULE_RETENTION_DAYS defaults to 30
  E2E_INTERACTION_TIMEOUT_SEC defaults to 120
  E2E_AUDIT_LOG          defaults to <workdir>/.lark-agent-bridge/audit.jsonl
  E2E_CALLBACK_ADDR      optional legacy HTTP callback listen address, e.g. :8080
  LARK_APP_ID            required for serve
  LARK_APP_SECRET        required for serve
  LARK_BOT_OPEN_ID       optional override; serve auto-fetches bot open_id by default`)
}

func applyDefaultWorkDir(cfg *config.Config, workDir string) error {
	if workDir == "" {
		return nil
	}
	cfg.DefaultWorkDir = workDir
	if os.Getenv("E2E_AUDIT_LOG") == "" {
		cfg.AuditLogPath = filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl")
	}
	if os.Getenv("E2E_SESSION_STORE") == "" {
		cfg.SessionStorePath = filepath.Join(workDir, ".lark-agent-bridge", "sessions.json")
	}
	if os.Getenv("E2E_SCHEDULE_STORE") == "" {
		cfg.ScheduleStorePath = filepath.Join(workDir, ".lark-agent-bridge", "schedules.json")
	}
	if os.Getenv("E2E_SCHEDULE_SOCKET") == "" {
		cfg.ScheduleSocketPath = config.DefaultScheduleSocketPath(workDir)
	}
	if os.Getenv("E2E_PREFERENCE_STORE") == "" {
		cfg.PreferenceStorePath = filepath.Join(workDir, ".lark-agent-bridge", "preferences.json")
	}
	if os.Getenv("E2E_REPLY_STORE") == "" {
		cfg.ReplyStorePath = filepath.Join(workDir, ".lark-agent-bridge", "replies.json")
	}
	cfg.AgentsConfigPath = filepath.Join(workDir, ".lark-agent-bridge", "agents.json")
	if os.Getenv("E2E_ACCESS_STORE") == "" {
		cfg.AccessStorePath = filepath.Join(workDir, ".lark-agent-bridge", "access.json")
	}
	if os.Getenv("E2E_TOPIC_ALIAS_STORE") == "" {
		cfg.TopicAliasStorePath = filepath.Join(workDir, ".lark-agent-bridge", "topic-aliases.json")
	}
	if os.Getenv("E2E_MEDIA_CACHE_DIR") == "" {
		absoluteWorkDir, err := filepath.Abs(workDir)
		if err != nil {
			return fmt.Errorf("resolve default workdir for media cache: %w", err)
		}
		cfg.MediaCacheDir = filepath.Join(filepath.Clean(absoluteWorkDir), ".lark-agent-bridge", "media")
	}
	// DevMode/Workspace 也走 workdir 命名空间(对齐 preference/reply 惯例),
	// 让 simulate 层 --default-workdir=tmpdir 时这两个 store 互不污染。
	// 生产 serve 里由 config.LoadFromEnv 的 E2E_DEFAULT_WORKDIR 路径已经覆盖过,
	// 这里补齐 --default-workdir CLI 参数的分支。
	if os.Getenv("E2E_DEV_MODE_STORE") == "" {
		cfg.DevModeStorePath = filepath.Join(workDir, ".lark-agent-bridge", "dev-mode.json")
	}
	if os.Getenv("E2E_WORKSPACE_STORE") == "" {
		cfg.WorkspaceStorePath = filepath.Join(workDir, ".lark-agent-bridge", "workspaces.json")
	}
	return nil
}

type serveMediaWiring struct {
	cache      *media.Cache
	downloader *feishu.MediaDownloader
	gc         *media.GC
}

func newServeMedia(cfg config.Config, tokens feishu.TenantTokenSource) serveMediaWiring {
	cache := media.NewCache(cfg.MediaCacheDir, media.Limits{
		MaxFileBytes:    cfg.MediaMaxFileBytes,
		MaxBatchBytes:   cfg.MediaMaxBatchBytes,
		MaxFiles:        10,
		CacheQuotaBytes: cfg.MediaCacheMaxBytes,
	})
	return serveMediaWiring{
		cache:      cache,
		downloader: &feishu.MediaDownloader{Tokens: tokens},
		gc:         media.NewGC(cache, cfg.MediaRetention),
	}
}

func newServeAuditRecorder(cfg config.Config) (*audit.Recorder, func(), error) {
	if cfg.AuditLogPath == "" {
		return audit.NewRecorder(), func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.AuditLogPath), 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(cfg.AuditLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return audit.NewRecorderWithWriter(file), func() { _ = file.Close() }, nil
}

type stringList []string

func (l *stringList) String() string {
	return fmt.Sprint([]string(*l))
}

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

type simulateRunner struct{}

// loadAgentsInto attaches the agents catalogue from agents.json to a service,
// falling back to the built-in default (recorded as a warning) on error. Shared
// by the local simulate paths so they reflect agents.json like serve does.
func loadAgentsInto(svc *bridge.Service, cfg config.Config, recorder *audit.Recorder) {
	agents, err := config.LoadAgentsConfig(cfg.AgentsConfigPath)
	if err != nil {
		recorder.Record("system", "agents_config_fallback", "", err.Error())
	}
	svc.Agents = agents
}

func (simulateRunner) Run(_ context.Context, req bridge.AgentRunRequest) (bridge.AgentRunResult, error) {
	model := "simulate-claude"
	sessionID := "simulate-session"
	if req.Kind == agent.Codex {
		model = "simulate-codex"
		sessionID = "simulate-thread"
	}
	return bridge.AgentRunResult{
		Model:          model,
		Tokens:         len([]rune(req.Prompt)),
		AgentSessionID: sessionID,
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "simulated answer: " + req.Prompt},
			{Kind: card.SegmentThought, Text: "simulated reasoning for local workflow validation"},
			{Kind: card.SegmentTool, Text: "simulated tool call"},
		},
	}, nil
}

var _ bridge.AgentRunner = simulateRunner{}

// simulateFakeSender is a minimal feishu.Sender used by simulate --fake-thread
// to exercise the topic-precreate path without Feishu access. SendReply returns
// a canned SendResult carrying the caller-configured thread_id and a synthetic
// message_id ("om_probe_<threadID>"), so the recall step in
// precreateTopicForPost fires and lands in audit as topic_precreate_probe.
type simulateFakeSender struct {
	feishu.NoopSender
	threadID string
}

func (s *simulateFakeSender) SendReply(_ context.Context, _ feishu.Reply) (feishu.SendResult, error) {
	return feishu.SendResult{
		MessageID: "om_probe_" + s.threadID,
		ThreadID:  s.threadID,
	}, nil
}

func (s *simulateFakeSender) DeleteMessage(_ context.Context, _ string) error {
	return nil
}

// quotedMessageFetcher adapts *feishu.SDKSender to bridge.MessageFetcher,
// mapping the feishu-specific FetchedMessage into the bridge domain view so
// bridge stays free of feishu SDK types.
type quotedMessageFetcher struct {
	sender *feishu.SDKSender
}

func (f quotedMessageFetcher) FetchMessage(ctx context.Context, messageID string) (bridge.QuotedMessage, error) {
	fetched, err := f.sender.FetchMessage(ctx, messageID)
	if err != nil {
		return bridge.QuotedMessage{}, err
	}
	return bridge.QuotedMessage{
		Text:        fetched.Text,
		SenderID:    fetched.SenderID,
		SenderType:  fetched.SenderType,
		MessageType: fetched.MessageType,
		Attachments: fetched.Attachments,
	}, nil
}

var _ bridge.MessageFetcher = quotedMessageFetcher{}

// simulateQuotedFetcher 是 simulate 通道下的伪 MessageFetcher：任意 messageID
// 都返回同一份注入的 QuotedMessage，用来在无 SDK/无飞书链路下模拟引用消息触发
// 的 prompt 分支（含主体身份边框断言）。
type simulateQuotedFetcher struct {
	text       string
	senderID   string
	senderType string
}

func (f simulateQuotedFetcher) FetchMessage(_ context.Context, _ string) (bridge.QuotedMessage, error) {
	return bridge.QuotedMessage{
		Text:        f.text,
		SenderID:    f.senderID,
		SenderType:  f.senderType,
		MessageType: "text",
	}, nil
}

var _ bridge.MessageFetcher = simulateQuotedFetcher{}

// firstNonEmpty returns the first non-empty (trimmed) string among the inputs,
// or "" if all are blank. Used to accept a new env name while keeping the
// legacy one as a fallback during rollout.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// resetStoreCatalog maps user-facing store names (as printed in --reset-store)
// to their absolute paths in cfg. Config is intentionally NOT in this list —
// --reset-store must never clear operator credentials or preferences file.
func resetStoreCatalog(cfg config.Config) map[string]string {
	return map[string]string{
		"sessions.json":            cfg.SessionStorePath,
		"workspaces.json":          cfg.WorkspaceStorePath,
		"preferences.json":         cfg.PreferenceStorePath,
		"replies.json":             cfg.ReplyStorePath,
		"access.json":              cfg.AccessStorePath,
		"action-grants.json":       cfg.ActionGrantStorePath,
		"dev-mode.json":            cfg.DevModeStorePath,
		"participated-topics.json": cfg.ParticipatedTopicsStorePath,
		"topic-aliases.json":       cfg.TopicAliasStorePath,
		"schedules.json":           cfg.ScheduleStorePath,
	}
}

// applyResetStore deletes the state files named in spec before the serve loop
// starts. spec is a comma-separated list of store file names, or "all" to
// clear every known state store. Missing files are treated as a no-op. This
// implements the plan's "完全复位" escape hatch — accept losing state when the
// operator explicitly asks for it, but never touch config/credentials.
func applyResetStore(cfg config.Config, spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	catalog := resetStoreCatalog(cfg)
	var names []string
	if strings.EqualFold(spec, "all") {
		for name := range catalog {
			names = append(names, name)
		}
	} else {
		for _, raw := range strings.Split(spec, ",") {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			if _, ok := catalog[name]; !ok {
				return fmt.Errorf("--reset-store: unknown store %q; known: sessions.json, workspaces.json, preferences.json, replies.json, access.json, action-grants.json, dev-mode.json, participated-topics.json, topic-aliases.json, schedules.json, all", name)
			}
			names = append(names, name)
		}
	}
	for _, name := range names {
		path := catalog[name]
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("--reset-store %s: %w", name, err)
		}
		fmt.Fprintf(os.Stderr, "[reset-store] cleared %s\n", path)
	}
	return nil
}
