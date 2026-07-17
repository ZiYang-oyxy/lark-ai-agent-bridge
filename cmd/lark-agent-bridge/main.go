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
	"strings"
	"syscall"
	"time"

	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/bridge"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/doctor"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/tmux"
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
	case "simulate-output":
		return runSimulateOutput(args[1:])
	case "serve":
		return runServe(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runSimulateOutput(args []string) error {
	fs := flag.NewFlagSet("simulate-output", flag.ContinueOnError)
	output := fs.String("output", "Tool permission required\nAllow once\nReject", "captured tmux pane output")
	captureError := fs.String("capture-error", "", "simulate tmux capture-pane error")
	sessionID := fs.String("session", "claude:chat-demo", "session id")
	primeText := fs.String("prime-text", "/claude hello", "message to create a session before polling")
	timeoutNow := fs.Bool("timeout-now", false, "immediately trigger pending interaction timeout after polling")
	var nextMessages stringList
	fs.Var(&nextMessages, "next-text", "additional queued message text before output polling, repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.LoadFromEnv()
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	recording := tmux.NewRecordingRunner()
	manager := tmux.NewManager(cfg.TmuxSession, recording)
	svc := bridge.NewService(cfg, renderer, manager, recorder)
	msg := bridge.Message{
		ID:        fmt.Sprintf("local-%d", time.Now().UnixNano()),
		ChatID:    "chat-demo",
		Sender:    "user-demo",
		Text:      *primeText,
		Mentioned: true,
		Time:      time.Now(),
	}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		return err
	}
	for _, nextText := range nextMessages {
		nextMsg := bridge.Message{
			ID:        fmt.Sprintf("local-%d", time.Now().UnixNano()),
			ChatID:    "chat-demo",
			Sender:    "user-demo",
			Text:      nextText,
			Mentioned: true,
			Time:      time.Now(),
		}
		if err := svc.HandleMessage(context.Background(), nextMsg); err != nil {
			return err
		}
	}
	key := fmt.Sprintf("tmux capture-pane -p -t %s:%s -S -300", cfg.TmuxSession, "agent-claude-chat-demo")
	if *captureError != "" {
		recording.Fail[key] = errors.New(*captureError)
	} else {
		recording.Responses[key] = [][]byte{[]byte(decodeFlagEscapes(*output))}
	}
	if err := svc.PollSessionOutput(context.Background(), *sessionID); err != nil {
		return err
	}
	if *timeoutNow {
		if err := svc.RenderInteractionTimeouts(context.Background(), time.Now().Add(cfg.InteractionTimeout+time.Second)); err != nil {
			return err
		}
	}
	out := struct {
		Events []card.Event           `json:"events"`
		Audit  []audit.Event          `json:"audit"`
		Tmux   []tmux.RecordedCommand `json:"tmux,omitempty"`
	}{Events: renderer.Events(), Audit: recorder.Events(), Tmux: recording.Snapshot()}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func runSimulateAction(args []string) error {
	fs := flag.NewFlagSet("simulate-action", flag.ContinueOnError)
	actionID := fs.String("action", "stop", "action id")
	value := fs.String("value", "", "action value")
	sessionID := fs.String("session", "claude:chat-demo", "session id")
	actor := fs.String("actor", "user-demo", "actor id")
	primeText := fs.String("prime-text", "/claude hello", "message to create a session before action; empty disables")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.LoadFromEnv()
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	recording := tmux.NewRecordingRunner()
	svc := bridge.NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, recording), recorder)
	if *primeText != "" {
		msg := bridge.Message{
			ID:        fmt.Sprintf("local-%d", time.Now().UnixNano()),
			ChatID:    "chat-demo",
			Sender:    *actor,
			Text:      *primeText,
			Mentioned: true,
			Time:      time.Now(),
		}
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
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
	out := struct {
		Events []card.Event           `json:"events"`
		Audit  []audit.Event          `json:"audit"`
		Tmux   []tmux.RecordedCommand `json:"tmux,omitempty"`
	}{Events: renderer.Events(), Audit: recorder.Events(), Tmux: recording.Snapshot()}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	_ = fs.Parse(args)
	cfg := config.LoadFromEnv()
	fmt.Println(doctor.Summary(doctor.Run(cfg)))
	return nil
}

func runSimulate(args []string) error {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	text := fs.String("text", "/help", "message text")
	chat := fs.String("chat", "chat-demo", "chat id")
	thread := fs.String("thread", "", "thread id")
	sender := fs.String("sender", "user-demo", "sender id")
	group := fs.Bool("group", false, "simulate group chat")
	mentioned := fs.Bool("mentioned", true, "whether bot was mentioned")
	realTmux := fs.Bool("real-tmux", false, "use real tmux instead of recording runner")
	timeoutNow := fs.Bool("timeout-now", false, "immediately trigger pending confirmation timeout after message handling")
	var nextMessages stringList
	fs.Var(&nextMessages, "next-text", "additional message text for the same chat, repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.LoadFromEnv()
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	var manager *tmux.Manager
	var recording *tmux.RecordingRunner
	if *realTmux {
		manager = tmux.NewManager(cfg.TmuxSession, nil)
	} else {
		recording = tmux.NewRecordingRunner()
		manager = tmux.NewManager(cfg.TmuxSession, recording)
	}
	baseMsg := bridge.Message{
		ChatID:    *chat,
		ThreadID:  *thread,
		Sender:    *sender,
		Text:      *text,
		IsGroup:   *group,
		Mentioned: *mentioned,
		Time:      time.Now(),
	}
	svc := bridge.NewService(cfg, renderer, manager, recorder)
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
	out := struct {
		Events []card.Event           `json:"events"`
		Audit  []audit.Event          `json:"audit"`
		Tmux   []tmux.RecordedCommand `json:"tmux,omitempty"`
	}{Events: renderer.Events(), Audit: recorder.Events()}
	if recording != nil {
		out.Tmux = recording.Snapshot()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.LoadFromEnv()
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
	svc := bridge.NewService(cfg, renderer, tmux.NewManager(cfg.TmuxSession, nil), recorder)
	client := feishu.NewLongConnClient(feishu.LongConnConfig{
		AppID:     appID,
		AppSecret: appSecret,
		BotOpenID: botOpenID,
		ActionHandler: func(ctx context.Context, action feishu.CardAction) (*feishu.CardActionResponse, error) {
			if err := svc.HandleAction(ctx, bridge.ActionRequest{
				SessionID: action.SessionID,
				ActionID:  action.ActionID,
				Value:     action.Value,
				Actor:     action.Actor,
			}); err != nil {
				return nil, err
			}
			if action.ActionID == "stop" || action.ActionID == "interrupt" {
				return &feishu.CardActionResponse{Card: card.BuildLarkCard(card.Event{
					Type:       "stop_button",
					SessionID:  action.SessionID,
					StopButton: card.StopButton{Visible: true, Disabled: true},
					Message:    "stopped",
				})}, nil
			}
			return nil, nil
		},
	})
	defer func() {
		_ = svc.Cleanup(context.Background())
	}()
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
	svc.StartBackgroundLoops(ctx, cfg.CardUpdateEvery, cfg.IdleCheckEvery)
	return runLongConnUntilStopped(ctx, client, func(ctx context.Context, in feishu.InboundMessage) error {
		msg := bridge.MessageFromFeishu(in)
		return svc.HandleMessage(ctx, msg)
	})
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
  lark-agent-bridge doctor
  lark-agent-bridge simulate -text "/claude hello"
  lark-agent-bridge simulate -text "/resume codex --last"
  lark-agent-bridge simulate -text "/claude first" -next-text "/claude second"
  lark-agent-bridge simulate -text "/claude --workdir /tmp/missing hello" -timeout-now
  lark-agent-bridge simulate-action -action stop -session claude:chat-demo
  lark-agent-bridge simulate-output -output "Tool permission required\nAllow once\nReject"
  lark-agent-bridge simulate-output -output "Tool permission required\nAllow once\nReject" -timeout-now
  lark-agent-bridge simulate-output -output "done\n>" -next-text "/claude second"
  lark-agent-bridge simulate-output -capture-error "pane missing"
  lark-agent-bridge serve

Environment:
  E2E_TMUX_SESSION       defaults to lark-agent-bridge
  E2E_DEFAULT_AGENT      defaults to claude
  E2E_DEFAULT_WORKDIR    defaults to current directory
  E2E_CARD_UPDATE_MS     defaults to 800
  E2E_CARD_MAX_CHARS     defaults to 12000
  E2E_INTERACTION_TIMEOUT_SEC defaults to 120
  E2E_IDLE_REMINDER_AFTER_SEC defaults to 86400
  E2E_IDLE_CHECK_MS      defaults to 3600000
  E2E_QUEUE_QUIET_POLLS  optional ready fallback after N quiet output polls
  E2E_AUDIT_LOG          defaults to <workdir>/.lark-agent-bridge/audit.jsonl
  E2E_CALLBACK_ADDR      optional legacy HTTP callback listen address, e.g. :8080
  LARK_APP_ID            required for serve
  LARK_APP_SECRET        required for serve
  LARK_BOT_OPEN_ID       optional override; serve auto-fetches bot open_id by default`)
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

func decodeFlagEscapes(value string) string {
	return strings.NewReplacer(`\n`, "\n", `\t`, "\t").Replace(value)
}
