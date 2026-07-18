package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/bridge"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/doctor"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/media"
	"lark-agent-bridge/internal/reply"
	"lark-agent-bridge/internal/session"
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
	case "doctor":
		return runDoctor(args[1:])
	case "simulate":
		return runSimulate(args[1:])
	case "simulate-action":
		return runSimulateAction(args[1:])
	case "serve":
		return runServe(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
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
	defaultWorkDir := fs.String("default-workdir", cfg.DefaultWorkDir, "default workdir for messages without --workdir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyDefaultWorkDir(&cfg, *defaultWorkDir); err != nil {
		return err
	}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	svc := bridge.NewService(cfg, renderer, simulateRunner{}, recorder)
	if *primeText != "" {
		msg := bridge.Message{
			ID:        "local-id",
			ChatID:    "chat-demo",
			Sender:    *actor,
			Text:      *primeText,
			Mentioned: true,
			Time:      time.Now(),
		}
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			return err
		}
		if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
			return err
		}
	}
	if err := svc.HandleAction(context.Background(), bridge.ActionRequest{
		SessionID: *sessionID,
		ActionID:  *actionID,
		Value:     *value,
		Actor:     *actor,
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
	group := fs.Bool("group", false, "simulate group chat")
	mentioned := fs.Bool("mentioned", true, "whether bot was mentioned")
	timeoutNow := fs.Bool("timeout-now", false, "immediately trigger pending confirmation timeout after message handling")
	defaultWorkDir := fs.String("default-workdir", cfg.DefaultWorkDir, "default workdir for messages without --workdir")
	var nextMessages stringList
	fs.Var(&nextMessages, "next-text", "additional message text for the same chat, repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyDefaultWorkDir(&cfg, *defaultWorkDir); err != nil {
		return err
	}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	baseMsg := bridge.Message{
		ChatID:    *chat,
		ThreadID:  *thread,
		Sender:    *sender,
		Text:      *text,
		IsGroup:   *group,
		Mentioned: *mentioned,
		Time:      time.Now(),
	}
	svc := bridge.NewService(cfg, renderer, simulateRunner{}, recorder)
	msg := bridge.Message{
		ID:        fmt.Sprintf("local-%d", time.Now().UnixNano()),
		ChatID:    *chat,
		ThreadID:  *thread,
		Sender:    *sender,
		Text:      *text,
		IsGroup:   *group,
		Mentioned: *mentioned,
		Time:      baseMsg.Time,
	}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		return err
	}
	for _, nextText := range nextMessages {
		nextMsg := bridge.Message{
			ID:        fmt.Sprintf("local-%d", time.Now().UnixNano()),
			ChatID:    *chat,
			ThreadID:  *thread,
			Sender:    *sender,
			Text:      nextText,
			IsGroup:   *group,
			Mentioned: *mentioned,
			Time:      time.Now(),
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyDefaultWorkDir(&cfg, *defaultWorkDir); err != nil {
		return err
	}
	appID := os.Getenv("LARK_APP_ID")
	appSecret := os.Getenv("LARK_APP_SECRET")
	if appID == "" || appSecret == "" {
		return fmt.Errorf("LARK_APP_ID and LARK_APP_SECRET are required for serve")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	botOpenID := os.Getenv("LARK_BOT_OPEN_ID")
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
	sessions := session.NewManagerWithStore(cfg.SessionStorePath)
	notices, err := sessions.Restore()
	if err != nil {
		return fmt.Errorf("restore session store: %w", err)
	}
	preferences, err := config.OpenPreferenceStore(cfg.PreferenceStorePath, config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: cfg.ReplyMode, ConversationMode: cfg.ConversationMode}, cfg.AllowedModels)
	if err != nil {
		return fmt.Errorf("open runtime preference store: %w", err)
	}
	replies, err := reply.OpenStore(cfg.ReplyStorePath)
	if err != nil {
		return fmt.Errorf("open reply store: %w", err)
	}
	sequenceJournal, err := bridge.NewNativeSequenceJournal(filepath.Join(filepath.Dir(cfg.SessionStorePath), "native-sequence-journal.json"), sessions, replies)
	if err != nil {
		return fmt.Errorf("open native sequence journal: %w", err)
	}
	cardRouter := newServeCardRouter(cardClient, recorder, sequenceJournal)
	renderer := feishu.NewReactionCardRenderer(sender, cardRouter)
	svc := bridge.NewServiceWithSessions(cfg, renderer, nil, recorder, sessions, notices)
	svc.Preferences = preferences
	svc.Replies = replies
	svc.SequenceResolver = sequenceJournal
	svc.CardTarget = cardRouter
	svc.Reactions = sender
	actionGateway := bridge.ActionGateway{Service: svc, Fencer: cardRouter}
	actionHandler, callbackHandler := newServeActionTransports(actionGateway, cfg.CardMaxChars)
	svc.ProcessRecoveryNotices(ctx)
	mediaWiring := newServeMedia(cfg, tokens)
	svc.MediaCache = mediaWiring.cache
	svc.MediaDownloader = mediaWiring.downloader
	svc.MediaGC = mediaWiring.gc
	svc.SweepMediaCacheStartup()
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	shutdownErr := svc.Shutdown(shutdownCtx)
	if longConnErr != nil && shutdownErr != nil {
		return errors.Join(longConnErr, shutdownErr)
	}
	if longConnErr != nil {
		return longConnErr
	}
	return shutdownErr
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
			return nil, err
		}
		return &feishu.CardActionResponse{Card: prepared.PayloadCopy()}, nil
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
		SessionID:  action.SessionID,
		ActionID:   action.ActionID,
		Value:      action.Value,
		Actor:      action.Actor,
		FormValues: formValues,
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
  lark-agent-bridge doctor [--strict] [--default-workdir /path]
  lark-agent-bridge simulate [--default-workdir /path] -text "/new hello"
  lark-agent-bridge simulate -text "/new first" -next-text "/new second"
  lark-agent-bridge simulate -text "/new --workdir /tmp/missing hello" -timeout-now
  lark-agent-bridge simulate-action -action stop -session claude:chat-demo:message:local-id
  lark-agent-bridge serve [--default-workdir /path]

Environment:
  E2E_DEFAULT_AGENT      defaults to claude
  E2E_CLAUDE_BIN         claude executable path; defaults to "claude" (resolved via PATH).
                         Set to an absolute path such as /path/to/lark-agent-workspace/bin/claude
                         to launch Claude through a workspace wrapper.
  E2E_DEFAULT_WORKDIR    defaults to current directory
  E2E_CARD_UPDATE_MS     defaults to 800
  E2E_CARD_MAX_CHARS     defaults to 12000
  E2E_CARD_MIN_DELTA_CHARS defaults to 30
  E2E_CARD_PREVIEW_MAX_CHARS defaults to 2000
  E2E_REPLY_MODE         append, append-clean-card, or latest-card
  E2E_REPLY_STORE        defaults to <workdir>/.lark-agent-bridge/replies.json
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
	if os.Getenv("E2E_PREFERENCE_STORE") == "" {
		cfg.PreferenceStorePath = filepath.Join(workDir, ".lark-agent-bridge", "preferences.json")
	}
	if os.Getenv("E2E_REPLY_STORE") == "" {
		cfg.ReplyStorePath = filepath.Join(workDir, ".lark-agent-bridge", "replies.json")
	}
	if os.Getenv("E2E_MEDIA_CACHE_DIR") == "" {
		absoluteWorkDir, err := filepath.Abs(workDir)
		if err != nil {
			return fmt.Errorf("resolve default workdir for media cache: %w", err)
		}
		cfg.MediaCacheDir = filepath.Join(filepath.Clean(absoluteWorkDir), ".lark-agent-bridge", "media")
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

func (simulateRunner) Run(_ context.Context, req bridge.AgentRunRequest) (bridge.AgentRunResult, error) {
	return bridge.AgentRunResult{
		Model:           "simulate-claude",
		Tokens:          len([]rune(req.Prompt)),
		ClaudeSessionID: "simulate-session",
		Segments: []card.Segment{
			{Kind: card.SegmentText, Text: "simulated answer: " + req.Prompt},
			{Kind: card.SegmentThought, Text: "simulated reasoning for local workflow validation"},
			{Kind: card.SegmentTool, Text: "simulated tool call"},
		},
	}, nil
}

var _ bridge.AgentRunner = simulateRunner{}
