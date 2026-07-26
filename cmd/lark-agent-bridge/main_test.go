package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/bridge"
	"lark-agent-bridge/internal/buildinfo"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/schedule"
	"lark-agent-bridge/internal/session"
)

func TestRunVersionJSONReportsBuildInfo(t *testing.T) {
	oldVersion, oldCommit, oldBuildTime := buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime = oldVersion, oldCommit, oldBuildTime
	})
	buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime = "1.2.3", "abc123", "2026-07-21T12:00:00Z"
	var out bytes.Buffer
	if err := runVersion([]string{"--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var got buildinfo.Info
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode version output: %v\n%s", err, out.String())
	}
	if got.Version != "1.2.3" || got.Commit != "abc123" || got.GOOS == "" || got.GOARCH == "" {
		t.Fatalf("version output = %#v", got)
	}
}

func TestRunVersionHumanPrefixesReleaseVersion(t *testing.T) {
	old := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = old })
	buildinfo.Version = "1.2.3"
	var out bytes.Buffer
	if err := runVersion(nil, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "v1.2.3") {
		t.Fatalf("human version output = %q", got)
	}
}

func TestNewRuntimeUpdateManagerFollowsConfiguration(t *testing.T) {
	if got := newRuntimeUpdateManager(config.Config{}, nil); got != nil {
		t.Fatalf("manager without URL = %#v", got)
	}
	got := newRuntimeUpdateManager(config.Config{
		UpdateManifestURL:           "https://updates.example/manifest.json",
		UpdatePrereleaseManifestURL: "https://updates.example/prerelease.json",
	}, nil)
	if got == nil || got.Client == nil || got.Client.ManifestURL != "https://updates.example/manifest.json" {
		t.Fatalf("configured manager = %#v", got)
	}
	if got.Client.PrereleaseURL != "https://updates.example/prerelease.json" {
		t.Fatalf("prerelease URL not wired = %q", got.Client.PrereleaseURL)
	}
	// A nil dev-mode store must yield a stable (non-prerelease) channel, not panic.
	if got.Client.Prerelease == nil || got.Client.Prerelease() {
		t.Fatalf("nil dev-mode store must report stable channel")
	}
}

func TestNewRuntimeUpdateManagerWarnsWhenPrereleaseURLMissing(t *testing.T) {
	// Only stable configured — the bridge silently only tracked stable before,
	// so a supervisor started with just LAB_UPDATE_MANIFEST_URL got no signal
	// that /help would ignore any published rc. Now startup must warn.
	var buf bytes.Buffer
	got := newRuntimeUpdateManagerTo(config.Config{
		UpdateManifestURL: "https://updates.example/manifest.json",
	}, nil, &buf)
	if got == nil {
		t.Fatalf("manager must still be built when only stable is set")
	}
	warn := buf.String()
	if !strings.Contains(warn, "LAB_UPDATE_PRERELEASE_MANIFEST_URL") {
		t.Fatalf("warning must name the missing env var, got %q", warn)
	}
	if !strings.Contains(warn, "/help") || !strings.Contains(warn, "rc") {
		t.Fatalf("warning must mention /help and rc, got %q", warn)
	}
}

func TestNewRuntimeUpdateManagerSilentWhenBothURLsSetOrUnset(t *testing.T) {
	// Both configured — no warning: everything is wired for both channels.
	var buf bytes.Buffer
	newRuntimeUpdateManagerTo(config.Config{
		UpdateManifestURL:           "https://updates.example/manifest.json",
		UpdatePrereleaseManifestURL: "https://updates.example/prerelease.json",
	}, nil, &buf)
	if buf.Len() != 0 {
		t.Fatalf("must not warn when prerelease URL is set, got %q", buf.String())
	}
	// Neither configured — no warning either: an unconfigured bridge already
	// surfaces "self-upgrade disabled" through the /help card; we don't want a
	// separate stderr line for the same fact.
	buf.Reset()
	if got := newRuntimeUpdateManagerTo(config.Config{}, nil, &buf); got != nil {
		t.Fatalf("manager without stable URL = %#v", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("must not warn when neither URL is set, got %q", buf.String())
	}
}

type serveCardKitClientFake struct {
	fullUpdates    int
	elementUpdates int
}

func TestSimulateRunnerReportsSelectedAgent(t *testing.T) {
	result, err := (simulateRunner{}).Run(context.Background(), bridge.AgentRunRequest{Kind: agent.Codex, Prompt: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "simulate-codex" || result.AgentSessionID != "simulate-thread" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunScheduleProposeValidatesRequiredEnvironment(t *testing.T) {
	t.Setenv("LAB_SCHEDULE_SOCKET", "")
	t.Setenv("LAB_SCHEDULE_TOKEN", "")
	err := runSchedulePropose([]string{"--kind", "cron", "--cron", "0 9 * * *", "--timezone", "Asia/Shanghai", "--prompt", "总结日报"})
	if err == nil || !strings.Contains(err.Error(), "LAB_SCHEDULE_SOCKET") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunScheduleProposeCreatesDraft(t *testing.T) {
	now := time.Now()
	store, err := schedule.NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := schedule.NewContextRegistry(time.Minute)
	token, err := registry.Issue(schedule.ProposalContext{
		OriginRunID: "run", Creator: "ou", Target: schedule.Target{ChatID: "oc"},
		Execution: schedule.FrozenExecution{Agent: "claude", WorkDir: t.TempDir()},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "lab-main-schedule-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "control.sock")
	server := schedule.NewControlServer(socketPath, store, registry, schedule.ControlConfig{Now: func() time.Time { return now }})
	if err := server.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv("LAB_SCHEDULE_SOCKET", socketPath)
	t.Setenv("LAB_SCHEDULE_TOKEN", token)
	if err := runSchedulePropose([]string{"--kind", "cron", "--cron", "0 9 * * *", "--timezone", "Asia/Shanghai", "--description", "日报", "--prompt", "总结日报"}); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Drafts()); got != 1 {
		t.Fatalf("draft count = %d", got)
	}
}

func TestOpenSessionStateAttachesCatalogAndRejectsCorruption(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{SessionStorePath: filepath.Join(root, "sessions.json")}
	manager, _, err := openSessionState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	workDir, err := session.CanonicalWorkDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordSession(session.CatalogEntry{SessionID: "session", Agent: agent.Claude, WorkDir: workDir, UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("catalog was not attached: %v", err)
	}
	if err := os.WriteFile(session.CatalogPath(cfg.SessionStorePath), []byte(`{"schema_version":99,"entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openSessionState(cfg); err == nil || !strings.Contains(err.Error(), "open session catalog") {
		t.Fatalf("corrupt catalog error = %v", err)
	}
}

func TestSimulateRejectsUnknownSenderType(t *testing.T) {
	err := runSimulate([]string{"-sender-type", "service-account"})
	if err == nil || !strings.Contains(err.Error(), "sender-type must be user or bot") {
		t.Fatalf("runSimulate error = %v", err)
	}
}

func TestSimulateAttachesPreferenceStoreForConfigSet(t *testing.T) {
	workDir := t.TempDir()
	preferencePath := filepath.Join(workDir, "preferences.json")
	t.Setenv("E2E_DEFAULT_WORKDIR", workDir)
	t.Setenv("E2E_PREFERENCE_STORE", preferencePath)

	if err := runSimulate([]string{"-text", "/config set effort=high"}); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenPreferenceStore(preferencePath, runtimePreferenceDefaults(config.LoadFromEnv()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get().Effort; got != "high" {
		t.Fatalf("simulated preference effort = %q, want high", got)
	}
}

func (f *serveCardKitClientFake) CreateCard(context.Context, feishu.CardKitCreateRequest) (feishu.CardKitCreateResult, error) {
	return feishu.CardKitCreateResult{CardID: "card"}, nil
}

func (f *serveCardKitClientFake) ReplyCard(context.Context, feishu.CardKitReplyRequest) (feishu.CardKitReplyResult, error) {
	return feishu.CardKitReplyResult{MessageID: "message"}, nil
}

func (f *serveCardKitClientFake) UpdateCard(context.Context, feishu.CardKitUpdateCardRequest) error {
	f.fullUpdates++
	return nil
}

func (f *serveCardKitClientFake) UpdateSettings(context.Context, feishu.CardKitUpdateSettingsRequest) error {
	return nil
}

func (f *serveCardKitClientFake) UpdateElementContent(context.Context, feishu.CardKitUpdateElementContentRequest) error {
	f.elementUpdates++
	return nil
}

type serveNativeJournalFake struct {
	prepares int
}

func (f *serveNativeJournalFake) PrepareNative(context.Context, feishu.NativeSequenceIntent) error {
	f.prepares++
	return nil
}

func (*serveNativeJournalFake) ConfirmNative(context.Context, feishu.NativeSequenceIntent) error {
	return nil
}

func (*serveNativeJournalFake) AbortNative(context.Context, feishu.NativeSequenceIntent) error {
	return nil
}

func renderServeNativePreview(t *testing.T, router *feishu.CardKitRouterRenderer) {
	t.Helper()
	renderer, err := router.NewStreamingBound(t.Context(), feishu.RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", RunCardSessionID: "run",
	}, "reply")
	if err != nil {
		t.Fatal(err)
	}
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true, SessionID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Render(card.Event{Type: "stream", Streaming: true, SessionID: "run", Segments: []card.Segment{{Kind: card.SegmentText, Text: "answer"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestNewServeCardRouterAlwaysInjectsDurableJournal(t *testing.T) {
	client := &serveCardKitClientFake{}
	journal := &serveNativeJournalFake{}
	router := newServeCardRouter(client, nil, journal)
	renderServeNativePreview(t, router)
	if client.elementUpdates != 1 || journal.prepares != 1 || client.fullUpdates != 0 {
		t.Fatalf("production router element/prepare/full = %d/%d/%d", client.elementUpdates, journal.prepares, client.fullUpdates)
	}
}

func TestServeCardRouterSwitchesToNativeAfterAnswerLayoutStabilizes(t *testing.T) {
	client := &serveCardKitClientFake{}
	journal := &serveNativeJournalFake{}
	router := newServeCardRouter(client, nil, journal)
	renderer, err := router.NewStreamingBound(t.Context(), feishu.RenderBinding{
		BaseSessionID: "claude:chat", BatchID: "batch", RunCardSessionID: "run",
	}, "reply")
	if err != nil {
		t.Fatal(err)
	}
	base := card.Event{
		Type: "stream", Streaming: true, SessionID: "run",
		StopButton: card.StopButton{Visible: true}, HeaderTemplate: "blue",
	}
	initial := base
	initial.Activity = "reasoning"
	initial.HeaderTitle = "🧠 正在推理 · ⏱ 1s"
	initial.Message = "思考中…"
	if err := renderer.Render(initial); err != nil {
		t.Fatal(err)
	}
	firstAnswer := base
	firstAnswer.Activity = "answering"
	firstAnswer.HeaderTitle = "✍️ 正在回复 · ⏱ 3s"
	firstAnswer.Segments = []card.Segment{{Kind: card.SegmentText, Text: "first"}}
	if err := renderer.Render(firstAnswer); err != nil {
		t.Fatal(err)
	}
	nextAnswer := firstAnswer
	nextAnswer.HeaderTitle = "✍️ 正在回复 · ⏱ 4s"
	nextAnswer.Segments = []card.Segment{{Kind: card.SegmentText, Text: "first second"}}
	if err := renderer.Render(nextAnswer); err != nil {
		t.Fatal(err)
	}
	if client.fullUpdates != 1 || client.elementUpdates != 1 || journal.prepares != 1 {
		t.Fatalf("production router full/element/prepare = %d/%d/%d, want 1/1/1", client.fullUpdates, client.elementUpdates, journal.prepares)
	}
}

var _ feishu.CardKitClientAPI = (*serveCardKitClientFake)(nil)
var _ feishu.NativeSequenceJournal = (*serveNativeJournalFake)(nil)

type recordingInteractionFencer struct {
	mu       sync.Mutex
	sessions []string
	releases int
}

type transportMessageDeleter struct {
	err error
}

func (d transportMessageDeleter) DeleteMessage(context.Context, string) error {
	return d.err
}

func TestConfigCloseActionTransportReturnsNoReplacementCard(t *testing.T) {
	svc := bridge.NewService(config.Config{}, card.NewFakeRenderer(), simulateRunner{}, audit.NewRecorder())
	svc.MessageDeleter = transportMessageDeleter{}
	handler, _ := newServeActionTransports(bridge.ActionGateway{Service: svc}, 1000)

	response, err := handler(t.Context(), feishu.CardAction{SessionID: "config-card", ActionID: "config.close", Actor: "admin", OpenMessageID: "om_config"})
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Card != nil || response.ToastContent != "" {
		t.Fatalf("response = %#v, want success without replacement card or toast", response)
	}
}

func TestConfigCloseActionTransportReturnsDeleteFailure(t *testing.T) {
	svc := bridge.NewService(config.Config{}, card.NewFakeRenderer(), simulateRunner{}, audit.NewRecorder())
	svc.MessageDeleter = transportMessageDeleter{err: errors.New("delete denied")}
	handler, _ := newServeActionTransports(bridge.ActionGateway{Service: svc}, 1000)

	response, err := handler(t.Context(), feishu.CardAction{SessionID: "config-card", ActionID: "config.close", Actor: "admin", OpenMessageID: "om_config"})
	if err == nil || response != nil {
		t.Fatalf("response/error = %#v / %v, want nil response and error", response, err)
	}
}

func (f *recordingInteractionFencer) BeginCardInteraction(sessionID string) func() {
	f.mu.Lock()
	f.sessions = append(f.sessions, sessionID)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.releases++
		f.mu.Unlock()
	}
}

func TestServeActionTransportsShareGatewayFencer(t *testing.T) {
	fencer := &recordingInteractionFencer{}
	longConnHandler, callbackHandler := newServeActionTransports(bridge.ActionGateway{Fencer: fencer}, 1000)
	if _, err := longConnHandler(t.Context(), feishu.CardAction{SessionID: "long-session", ActionID: "stop"}); err == nil {
		t.Fatal("long-connection action error = nil, want unavailable service")
	}
	req := httptest.NewRequest(http.MethodPost, "/card/callback", strings.NewReader(`{"operator":{"open_id":"user"},"action":{"value":{"session":"http-session","action_id":"stop"}}}`))
	rec := httptest.NewRecorder()
	callbackHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("callback status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	fencer.mu.Lock()
	defer fencer.mu.Unlock()
	if got, want := fencer.sessions, []string{"long-session", "http-session"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fenced sessions = %#v, want %#v", got, want)
	}
	if fencer.releases != 2 {
		t.Fatalf("fence releases = %d, want 2", fencer.releases)
	}
}

func TestNewServeAuditRecorderWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "audit.jsonl")
	recorder, closeFn, err := newServeAuditRecorder(config.Config{AuditLogPath: path})
	if err != nil {
		t.Fatalf("new recorder error: %v", err)
	}
	recorder.Record("u1", "run_input", "claude:chat", "token=secret-value")
	closeFn()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log error: %v", err)
	}
	log := string(data)
	if strings.Contains(log, "secret-value") {
		t.Fatalf("secret leaked in audit log: %s", log)
	}
	if !strings.Contains(log, `"Action":"run_input"`) || !strings.Contains(log, `[REDACTED]`) {
		t.Fatalf("audit log missing expected fields: %s", log)
	}
}

func TestActionRequestFromFeishuClonesFormValues(t *testing.T) {
	action := feishu.CardAction{
		SessionID:     "claude:chat",
		ActionID:      "config.save",
		Actor:         "user",
		ChatID:        "oc_chat",
		OpenMessageID: "om_config",
		FormValues:    map[string]string{"model": "opus", "effort": "high"},
		GrantID:       "grant-1",
	}
	req := actionRequestFromFeishu(action)
	action.FormValues["model"] = "haiku"
	if req.SessionID != "claude:chat" || req.ActionID != "config.save" || req.Actor != "user" || req.ChatID != "oc_chat" || req.OpenMessageID != "om_config" || req.FormValues["model"] != "opus" || req.FormValues["effort"] != "high" || req.GrantID != "grant-1" {
		t.Fatalf("action request form values = %#v", req.FormValues)
	}
}

func TestApplyDefaultWorkDirPreservesExplicitAuditLog(t *testing.T) {
	t.Setenv("E2E_AUDIT_LOG", "/tmp/custom-audit.jsonl")
	cfg := config.Config{AuditLogPath: "/tmp/custom-audit.jsonl"}
	if err := applyDefaultWorkDir(&cfg, "/tmp/work"); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultWorkDir != "/tmp/work" {
		t.Fatalf("default workdir = %q, want /tmp/work", cfg.DefaultWorkDir)
	}
	if cfg.AuditLogPath != "/tmp/custom-audit.jsonl" {
		t.Fatalf("audit path = %q, want explicit path preserved", cfg.AuditLogPath)
	}
}

func TestApplyDefaultWorkDirRebasesImplicitSessionStore(t *testing.T) {
	t.Setenv("E2E_AUDIT_LOG", "")
	t.Setenv("E2E_SESSION_STORE", "")
	cfg := config.Config{}
	if err := applyDefaultWorkDir(&cfg, "/tmp/work"); err != nil {
		t.Fatal(err)
	}
	if cfg.SessionStorePath != filepath.Join("/tmp/work", ".lark-agent-bridge", "sessions.json") {
		t.Fatalf("session store = %q", cfg.SessionStorePath)
	}
}

func TestApplyDefaultWorkDirRebasesImplicitSchedulePaths(t *testing.T) {
	t.Setenv("E2E_SCHEDULE_STORE", "")
	t.Setenv("E2E_SCHEDULE_SOCKET", "")
	cfg := config.Config{}
	if err := applyDefaultWorkDir(&cfg, "/tmp/work"); err != nil {
		t.Fatal(err)
	}
	if cfg.ScheduleStorePath != filepath.Join("/tmp/work", ".lark-agent-bridge", "schedules.json") {
		t.Fatalf("schedule store = %q", cfg.ScheduleStorePath)
	}
	if cfg.ScheduleSocketPath == "" || !strings.Contains(cfg.ScheduleSocketPath, "schedule.sock") {
		t.Fatalf("schedule socket = %q", cfg.ScheduleSocketPath)
	}
}

func TestApplyDefaultWorkDirRebasesImplicitPreferenceStore(t *testing.T) {
	t.Setenv("E2E_PREFERENCE_STORE", "")
	cfg := config.Config{}
	if err := applyDefaultWorkDir(&cfg, "/tmp/work"); err != nil {
		t.Fatal(err)
	}
	if cfg.PreferenceStorePath != filepath.Join("/tmp/work", ".lark-agent-bridge", "preferences.json") {
		t.Fatalf("preference store = %q", cfg.PreferenceStorePath)
	}
}

func TestApplyDefaultWorkDirRebasesImplicitReplyStore(t *testing.T) {
	t.Setenv("E2E_REPLY_STORE", "")
	cfg := config.Config{}
	if err := applyDefaultWorkDir(&cfg, "/tmp/work"); err != nil {
		t.Fatal(err)
	}
	if cfg.ReplyStorePath != filepath.Join("/tmp/work", ".lark-agent-bridge", "replies.json") {
		t.Fatalf("reply store path = %q", cfg.ReplyStorePath)
	}
}

func TestApplyDefaultWorkDirRebasesImplicitMediaCacheAsAbsolute(t *testing.T) {
	t.Setenv("E2E_MEDIA_CACHE_DIR", "")
	relative := filepath.Join("relative", "workspace")
	cfg := config.Config{}
	if err := applyDefaultWorkDir(&cfg, relative); err != nil {
		t.Fatal(err)
	}
	wantWorkDir, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wantWorkDir, ".lark-agent-bridge", "media")
	if cfg.MediaCacheDir != want || !filepath.IsAbs(cfg.MediaCacheDir) {
		t.Fatalf("media cache = %q, want absolute %q", cfg.MediaCacheDir, want)
	}
}

func TestApplyDefaultWorkDirPreservesExplicitMediaCache(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "media")
	t.Setenv("E2E_MEDIA_CACHE_DIR", explicit)
	cfg := config.Config{MediaCacheDir: explicit}
	if err := applyDefaultWorkDir(&cfg, filepath.Join("relative", "workspace")); err != nil {
		t.Fatal(err)
	}
	if cfg.MediaCacheDir != explicit {
		t.Fatalf("media cache = %q, want explicit %q", cfg.MediaCacheDir, explicit)
	}
}

func TestRuntimeCommandsRejectInvalidMediaEnvironment(t *testing.T) {
	t.Setenv("E2E_MEDIA_MAX_FILE_MB", "0")
	for _, tc := range []struct {
		name string
		run  func([]string) error
	}{
		{name: "simulate-action", run: runSimulateAction},
		{name: "doctor", run: runDoctor},
		{name: "simulate", run: runSimulate},
		{name: "serve", run: runServe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(nil); err == nil || !strings.Contains(err.Error(), "E2E_MEDIA_MAX_FILE_MB") {
				t.Fatalf("error = %v, want strict media config rejection", err)
			}
		})
	}
}

func TestRunDoctorWrapperPreflightWarningFailsOnlyInStrictMode(t *testing.T) {
	workDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(workDir, "fake-claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'PRIVATE_WRAPPER_OUTPUT' >&2\nexit 78\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("E2E_CLAUDE_BIN", bin)
	t.Setenv("LARK_APP_ID", "app")
	t.Setenv("LARK_APP_SECRET", "secret")
	if err := runDoctor([]string{"--default-workdir", workDir}); err != nil {
		t.Fatalf("non-strict doctor error = %v, want warning-only success", err)
	}
	if err := runDoctor([]string{"--strict", "--default-workdir", workDir}); err == nil || err.Error() != "doctor strict verification failed" {
		t.Fatalf("strict doctor error = %v, want strict verification failure", err)
	}
}

func TestNewServeMediaUsesConfiguredLimitsAndSharedTokenSource(t *testing.T) {
	tokens := feishu.NewTenantTokenSource("app", "secret")
	cfg := config.Config{
		MediaCacheDir:      filepath.Join(t.TempDir(), "media"),
		MediaMaxFileBytes:  25 << 20,
		MediaMaxBatchBytes: 100 << 20,
		MediaCacheMaxBytes: 500 << 20,
		MediaRetention:     72 * time.Hour,
	}
	wiring := newServeMedia(cfg, tokens)
	if wiring.cache == nil || wiring.gc == nil || wiring.downloader == nil {
		t.Fatalf("incomplete media wiring: %#v", wiring)
	}
	if wiring.downloader.Tokens != tokens {
		t.Fatal("media downloader did not reuse the shared tenant token source")
	}
}

func TestRunLongConnUntilStoppedReturnsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	client := fakeLongConnClient{
		run: func(ctx context.Context, _ func(context.Context, feishu.InboundMessage) error) error {
			close(started)
			<-ctx.Done()
			<-release
			return ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- runLongConnUntilStopped(ctx, client, func(context.Context, feishu.InboundMessage) error { return nil })
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runLongConnUntilStopped error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runLongConnUntilStopped did not return after context cancel")
	}
	close(release)
}

func TestRunLongConnUntilStoppedReturnsClientError(t *testing.T) {
	want := errors.New("connect failed")
	client := fakeLongConnClient{
		run: func(context.Context, func(context.Context, feishu.InboundMessage) error) error {
			return want
		},
	}
	err := runLongConnUntilStopped(context.Background(), client, func(context.Context, feishu.InboundMessage) error { return nil })
	if !errors.Is(err, want) {
		t.Fatalf("runLongConnUntilStopped error = %v, want %v", err, want)
	}
}

func TestRuntimePreferenceDefaultsIncludeGroupMessageSettings(t *testing.T) {
	cfg := config.Config{
		Model:            "opus",
		Effort:           "high",
		ReplyMode:        config.ReplyModeLatestCard,
		ConversationMode: config.ConversationModeTopic,
		GroupMessageMode: config.GroupMessageModeParticipatedTopics,
		RespondToBots:    true,
	}
	got := runtimePreferenceDefaults(cfg)
	if got.GroupMessageMode != config.GroupMessageModeParticipatedTopics || !got.RespondToBots {
		t.Fatalf("runtime preference defaults = %#v", got)
	}
}

func TestOpenParticipationStoreFailsClosedOnCorruptSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "topics.json")
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := openParticipationStore(config.Config{ParticipatedTopicsStorePath: path})
	if err == nil || !strings.Contains(err.Error(), "open participation store") {
		t.Fatalf("openParticipationStore error = %v", err)
	}
}

type fakeLongConnClient struct {
	run func(context.Context, func(context.Context, feishu.InboundMessage) error) error
}

func (f fakeLongConnClient) Run(ctx context.Context, handler func(context.Context, feishu.InboundMessage) error) error {
	return f.run(ctx, handler)
}

// TestApplyResetStoreDeletesNamed 只删指定 store 文件，其余保留；缺失是 no-op。
func TestApplyResetStoreDeletesNamed(t *testing.T) {
	dir := t.TempDir()
	sess := filepath.Join(dir, "sessions.json")
	repl := filepath.Join(dir, "replies.json")
	if err := os.WriteFile(sess, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repl, []byte("r"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{SessionStorePath: sess, ReplyStorePath: repl}
	if err := applyResetStore(cfg, "sessions.json"); err != nil {
		t.Fatalf("applyResetStore: %v", err)
	}
	if _, err := os.Stat(sess); !os.IsNotExist(err) {
		t.Fatalf("sessions.json should be deleted, err=%v", err)
	}
	if _, err := os.Stat(repl); err != nil {
		t.Fatalf("replies.json should remain: %v", err)
	}
	// Missing file after reset is a no-op.
	if err := applyResetStore(cfg, "sessions.json"); err != nil {
		t.Fatalf("re-applying reset on missing file should be no-op, got %v", err)
	}
}

// TestApplyResetStoreAll 清除全部已知 store。
func TestApplyResetStoreAll(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{
		"sessions.json":            filepath.Join(dir, "sessions.json"),
		"workspaces.json":          filepath.Join(dir, "workspaces.json"),
		"preferences.json":         filepath.Join(dir, "preferences.json"),
		"replies.json":             filepath.Join(dir, "replies.json"),
		"access.json":              filepath.Join(dir, "access.json"),
		"action-grants.json":       filepath.Join(dir, "action-grants.json"),
		"dev-mode.json":            filepath.Join(dir, "dev-mode.json"),
		"participated-topics.json": filepath.Join(dir, "participated-topics.json"),
		"topic-aliases.json":       filepath.Join(dir, "topic-aliases.json"),
		"schedules.json":           filepath.Join(dir, "schedules.json"),
	}
	for _, p := range paths {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		SessionStorePath:            paths["sessions.json"],
		WorkspaceStorePath:          paths["workspaces.json"],
		PreferenceStorePath:         paths["preferences.json"],
		ReplyStorePath:              paths["replies.json"],
		AccessStorePath:             paths["access.json"],
		ActionGrantStorePath:        paths["action-grants.json"],
		DevModeStorePath:            paths["dev-mode.json"],
		ParticipatedTopicsStorePath: paths["participated-topics.json"],
		TopicAliasStorePath:         paths["topic-aliases.json"],
		ScheduleStorePath:           paths["schedules.json"],
	}
	if err := applyResetStore(cfg, "all"); err != nil {
		t.Fatalf("applyResetStore all: %v", err)
	}
	for name, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s not cleared: %v", name, err)
		}
	}
}

// TestApplyResetStoreUnknown 拒绝未知 store 名并给出可用清单。
func TestApplyResetStoreUnknown(t *testing.T) {
	cfg := config.Config{}
	err := applyResetStore(cfg, "config.json")
	if err == nil {
		t.Fatalf("applyResetStore should reject unknown name")
	}
	if !strings.Contains(err.Error(), "unknown store") {
		t.Fatalf("error should mention 'unknown store': %v", err)
	}
}

// TestApplyResetStoreEmptyIsNoop 空 spec = 未使用 flag = no-op。
func TestApplyResetStoreEmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	sess := filepath.Join(dir, "sessions.json")
	if err := os.WriteFile(sess, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{SessionStorePath: sess}
	if err := applyResetStore(cfg, ""); err != nil {
		t.Fatalf("empty spec: %v", err)
	}
	if _, err := os.Stat(sess); err != nil {
		t.Fatalf("sessions.json should remain untouched: %v", err)
	}
}

// TestFirstNonEmptyPrefersEarlier 凭据白名单前缀切换测试用：优先 LAB_ 前缀，回落到旧名。
func TestFirstNonEmptyPrefersEarlier(t *testing.T) {
	if got := firstNonEmpty("", "  ", "hello", "world"); got != "hello" {
		t.Fatalf("firstNonEmpty = %q, want hello", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("all empty should return \"\", got %q", got)
	}
	if got := firstNonEmpty("  new  "); got != "new" {
		t.Fatalf("firstNonEmpty should trim: got %q", got)
	}
}
