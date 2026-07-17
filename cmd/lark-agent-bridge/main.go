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
	cfg := config.LoadFromEnv()
	actionID := fs.String("action", "stop", "action id")
	value := fs.String("value", "", "action value")
	sessionID := fs.String("session", "claude:chat-demo:message:local-id", "session id")
	actor := fs.String("actor", "user-demo", "actor id")
	primeText := fs.String("prime-text", "/new hello", "message to create a session before action; empty disables")
	defaultWorkDir := fs.String("default-workdir", cfg.DefaultWorkDir, "default workdir for messages without --workdir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	applyDefaultWorkDir(&cfg, *defaultWorkDir)
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
	cfg := config.LoadFromEnv()
	defaultWorkDir := fs.String("default-workdir", cfg.DefaultWorkDir, "default workdir for messages without --workdir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	applyDefaultWorkDir(&cfg, *defaultWorkDir)
	fmt.Println(doctor.Summary(doctor.Run(cfg)))
	return nil
}

func runSimulate(args []string) error {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	cfg := config.LoadFromEnv()
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
	applyDefaultWorkDir(&cfg, *defaultWorkDir)
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
	cfg := config.LoadFromEnv()
	defaultWorkDir := fs.String("default-workdir", cfg.DefaultWorkDir, "default workdir for messages without --workdir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	applyDefaultWorkDir(&cfg, *defaultWorkDir)
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
	cardClient := feishu.NewCardKitClient(appID, appSecret)
	sender := feishu.NewSDKSender(appID, appSecret)
	recorder, closeAudit, err := newServeAuditRecorder(cfg)
	if err != nil {
		return err
	}
	defer closeAudit()
	renderer := feishu.NewReactionCardRenderer(sender, feishu.NewCardKitRouterRendererWithObserver(cardClient, recorder))
	sessions := session.NewManagerWithStore(cfg.SessionStorePath)
	notices, err := sessions.Restore()
	if err != nil {
		return fmt.Errorf("restore session store: %w", err)
	}
	for _, notice := range notices {
		recorder.Record("system", "session_recovery_"+string(notice.Status), notice.SessionID, "reply="+notice.ReplyToMessageID+" card_session="+notice.CardSessionID)
	}
	svc := bridge.NewServiceWithSessions(cfg, renderer, nil, recorder, sessions, notices)
	client := feishu.NewLongConnClient(feishu.LongConnConfig{
		AppID:     appID,
		AppSecret: appSecret,
		BotOpenID: botOpenID,
		ActionHandler: func(ctx context.Context, action feishu.CardAction) (*feishu.CardActionResponse, error) {
			result, err := svc.HandleActionResult(ctx, bridge.ActionRequest{
				SessionID: action.SessionID,
				ActionID:  action.ActionID,
				Value:     action.Value,
				Actor:     action.Actor,
			})
			if err != nil {
				return nil, err
			}
			return &feishu.CardActionResponse{Card: result.BuildCard(cfg.CardMaxChars)}, nil
		},
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
		server := &http.Server{Addr: addr, Handler: bridge.NewCallbackHTTPHandler(svc)}
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
  lark-agent-bridge doctor [--default-workdir /path]
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
  E2E_INTERACTION_TIMEOUT_SEC defaults to 120
  E2E_AUDIT_LOG          defaults to <workdir>/.lark-agent-bridge/audit.jsonl
  E2E_CALLBACK_ADDR      optional legacy HTTP callback listen address, e.g. :8080
  LARK_APP_ID            required for serve
  LARK_APP_SECRET        required for serve
  LARK_BOT_OPEN_ID       optional override; serve auto-fetches bot open_id by default`)
}

func applyDefaultWorkDir(cfg *config.Config, workDir string) {
	if workDir == "" {
		return
	}
	cfg.DefaultWorkDir = workDir
	if os.Getenv("E2E_AUDIT_LOG") == "" {
		cfg.AuditLogPath = filepath.Join(workDir, ".lark-agent-bridge", "audit.jsonl")
	}
	if os.Getenv("E2E_SESSION_STORE") == "" {
		cfg.SessionStorePath = filepath.Join(workDir, ".lark-agent-bridge", "sessions.json")
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
