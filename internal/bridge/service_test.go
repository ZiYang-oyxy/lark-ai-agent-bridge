package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/feishu"
	"lark-agent-bridge/internal/media"
	"lark-agent-bridge/internal/reply"
	"lark-agent-bridge/internal/session"
)

type fakeRunner struct {
	mu      sync.Mutex
	calls   []AgentRunRequest
	results []AgentRunResult
	errs    []error
	updates []AgentStreamUpdate
	block   chan struct{}
	started chan struct{}
}

type bridgeReplyTarget struct {
	mu             sync.Mutex
	newCalls       int
	rehydrateCalls int
	newRef         session.RenderRef
	rehydratedRef  session.RenderRef
	events         []card.Event
	renderErr      error
	bindings       []feishu.RenderBinding
	newErr         error
}

func (t *bridgeReplyTarget) NewStreamingBound(ctx context.Context, binding feishu.RenderBinding, replyTo string) (feishu.ResumableRenderer, error) {
	t.mu.Lock()
	t.bindings = append(t.bindings, binding)
	t.mu.Unlock()
	return t.NewStreaming(ctx, binding.RunCardSessionID, replyTo)
}

func (t *bridgeReplyTarget) RehydrateBound(binding feishu.RenderBinding, ref session.RenderRef) feishu.ResumableRenderer {
	t.mu.Lock()
	t.bindings = append(t.bindings, binding)
	t.mu.Unlock()
	return t.Rehydrate(binding.RunCardSessionID, ref)
}

func (t *bridgeReplyTarget) NewStreaming(context.Context, string, string) (feishu.ResumableRenderer, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.newCalls++
	if t.newErr != nil {
		return nil, t.newErr
	}
	ref := t.newRef
	if ref.CardID == "" {
		ref = session.RenderRef{CardID: "new-card", ReplyMessageID: "new-reply"}
	}
	return &bridgeReplyRenderer{target: t, ref: ref}, nil
}

func (t *bridgeReplyTarget) AppendTerminal(_ context.Context, _ string, event card.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
	return nil
}

func (t *bridgeReplyTarget) Rehydrate(_ string, ref session.RenderRef) feishu.ResumableRenderer {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rehydrateCalls++
	t.rehydratedRef = ref
	return &bridgeReplyRenderer{target: t, ref: ref}
}

type bridgeReplyRenderer struct {
	target *bridgeReplyTarget
	ref    session.RenderRef
}

func (r *bridgeReplyRenderer) Render(event card.Event) error {
	r.target.mu.Lock()
	defer r.target.mu.Unlock()
	r.target.events = append(r.target.events, event)
	r.ref.Version++
	return r.target.renderErr
}

func (r *bridgeReplyRenderer) RenderRef() session.RenderRef { return r.ref }

type contextBlockingRecoveryTarget struct {
	renderer *contextBlockingRecoveryRenderer
}

func (t *contextBlockingRecoveryTarget) NewStreaming(context.Context, string, string) (feishu.ResumableRenderer, error) {
	return nil, errors.New("unexpected new streaming call")
}

func (t *contextBlockingRecoveryTarget) AppendTerminal(context.Context, string, card.Event) error {
	return errors.New("unexpected append terminal call")
}

func (t *contextBlockingRecoveryTarget) Rehydrate(string, session.RenderRef) feishu.ResumableRenderer {
	return t.renderer
}

type contextBlockingRecoveryRenderer struct {
	mu            sync.Mutex
	legacyCalled  bool
	contextCalled bool
	ref           session.RenderRef
}

func (r *contextBlockingRecoveryRenderer) Render(card.Event) error {
	r.mu.Lock()
	r.legacyCalled = true
	r.mu.Unlock()
	return errors.New("legacy render should not be used")
}

func (r *contextBlockingRecoveryRenderer) RenderContext(ctx context.Context, _ card.Event) error {
	r.mu.Lock()
	r.contextCalled = true
	r.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (r *contextBlockingRecoveryRenderer) RenderRef() session.RenderRef { return r.ref }

type fakeReactionSink struct {
	mu        sync.Mutex
	adds      []reactionCall
	deletes   []reactionDelete
	addErr    error
	deleteErr error
	blockType feishu.ReactionType
	entered   chan struct{}
	release   chan struct{}
}

type reactionCall struct {
	messageID string
	typeName  feishu.ReactionType
}

type reactionDelete struct {
	messageID  string
	reactionID string
}

func (s *fakeReactionSink) AddReaction(_ context.Context, messageID string, typeName feishu.ReactionType) (string, error) {
	s.mu.Lock()
	s.adds = append(s.adds, reactionCall{messageID: messageID, typeName: typeName})
	call := len(s.adds)
	block := s.blockType == typeName && s.release != nil
	if block && s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
	if block {
		<-s.release
	}
	if s.addErr != nil {
		return "", s.addErr
	}
	return fmt.Sprintf("reaction-%d", call), nil
}

func (s *fakeReactionSink) DeleteReaction(_ context.Context, messageID, reactionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes = append(s.deletes, reactionDelete{messageID: messageID, reactionID: reactionID})
	return s.deleteErr
}

func (s *fakeReactionSink) snapshot() ([]reactionCall, []reactionDelete) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]reactionCall(nil), s.adds...), append([]reactionDelete(nil), s.deletes...)
}

type failingAuditWriter struct{}

func (failingAuditWriter) Write([]byte) (int, error) { return 0, errors.New("audit disk full") }

func newFakeRunner() *fakeRunner {
	return &fakeRunner{started: make(chan struct{}, 10)}
}

func (r *fakeRunner) Run(ctx context.Context, req AgentRunRequest) (AgentRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req)
	idx := len(r.calls) - 1
	r.mu.Unlock()
	r.started <- struct{}{}
	for _, update := range r.updates {
		if req.OnEvent != nil {
			req.OnEvent(update)
		}
	}
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return AgentRunResult{}, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx < len(r.errs) && r.errs[idx] != nil {
		return AgentRunResult{}, r.errs[idx]
	}
	if idx < len(r.results) {
		return r.results[idx], nil
	}
	return AgentRunResult{Segments: []card.Segment{{Kind: card.SegmentText, Text: "ok"}}}, nil
}

type failOnceRenderer struct {
	mu     sync.Mutex
	failed bool
	next   *card.FakeRenderer
}

type blockingStreamRenderer struct {
	next    *card.FakeRenderer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type terminalBatchOrderRenderer struct {
	next       *card.FakeRenderer
	sessions   *session.Manager
	key        session.Key
	mu         sync.Mutex
	activeSeen bool
}

func (r *terminalBatchOrderRenderer) Render(e card.Event) error {
	if !e.Streaming {
		sess, ok := r.sessions.Get(r.key)
		r.mu.Lock()
		r.activeSeen = ok && sess.ActiveBatch != nil
		r.mu.Unlock()
	}
	return r.next.Render(e)
}

func (r *blockingStreamRenderer) Render(e card.Event) error {
	if e.Type == "stream" {
		r.once.Do(func() { close(r.entered) })
		<-r.release
	}
	return r.next.Render(e)
}

func (r *failOnceRenderer) Render(e card.Event) error {
	if e.Type != "stream" {
		return r.next.Render(e)
	}
	r.mu.Lock()
	if !r.failed {
		r.failed = true
		r.mu.Unlock()
		return errors.New("reply message revoked")
	}
	r.mu.Unlock()
	return r.next.Render(e)
}

func (r *fakeRunner) Calls() []AgentRunRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]AgentRunRequest, len(r.calls))
	copy(out, r.calls)
	return out
}

type resolutionCacheStub struct {
	resolution media.Resolution
	releases   int
}

type errorRenderer struct{}

func (errorRenderer) Render(card.Event) error { return errors.New("render unavailable") }

type nilRejectingDownloaderCache struct{ called bool }

func (c *nilRejectingDownloaderCache) Resolve(_ context.Context, downloader media.Downloader, _ []media.Ref) media.Resolution {
	c.called = true
	if downloader == nil {
		panic("resolver received nil downloader")
	}
	return media.Resolution{}
}

type unusedMediaDownloader struct{}

func (unusedMediaDownloader) Open(context.Context, media.Ref) (media.Download, error) {
	return media.Download{}, errors.New("unexpected downloader call")
}

func configureTestMedia(svc *Service, cache mediaResolver) {
	svc.MediaCache = cache
	svc.MediaDownloader = unusedMediaDownloader{}
}

type mediaSweepStub struct {
	mu           sync.Mutex
	results      []media.SweepResult
	err          error
	calls        []map[string]struct{}
	startupCalls []map[string]struct{}
	order        *[]string
}

func (s *mediaSweepStub) Sweep(live map[string]struct{}) (media.SweepResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.order != nil {
		*s.order = append(*s.order, "sweep")
	}
	s.calls = append(s.calls, clonePathSet(live))
	if s.err != nil {
		return media.SweepResult{}, s.err
	}
	if len(s.results) == 0 {
		return media.SweepResult{}, nil
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func (s *mediaSweepStub) SweepStartup(live map[string]struct{}) (media.SweepResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startupCalls = append(s.startupCalls, clonePathSet(live))
	return media.SweepResult{}, s.err
}

func (s *mediaSweepStub) callSnapshots() []map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]struct{}, len(s.calls))
	for i := range s.calls {
		out[i] = clonePathSet(s.calls[i])
	}
	return out
}

func (s *mediaSweepStub) startupCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.startupCalls)
}

type orderedMediaResolver struct {
	order      *[]string
	resolution media.Resolution
}

func (r *orderedMediaResolver) Resolve(context.Context, media.Downloader, []media.Ref) media.Resolution {
	*r.order = append(*r.order, "resolve")
	return r.resolution
}

func clonePathSet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for path := range in {
		out[path] = struct{}{}
	}
	return out
}

var errAttachmentFailureSummary = errors.New("attachment failure summary render failed")

type attachmentFailureSummaryRenderer struct{}

func (attachmentFailureSummaryRenderer) Render(event card.Event) error {
	if strings.HasPrefix(event.SessionID, "attachment-failure") {
		return errAttachmentFailureSummary
	}
	return nil
}

func TestRenderTextUsesDistinctCardSessionPerReply(t *testing.T) {
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	if err := svc.renderText("attachment-failure", "message-one", card.SegmentError, "first"); err != nil {
		t.Fatal(err)
	}
	if err := svc.renderText("attachment-failure", "message-two", card.SegmentError, "second"); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].SessionID == events[1].SessionID || events[0].SessionID != "attachment-failure:message:message-one" || events[1].SessionID != "attachment-failure:message:message-two" {
		t.Fatalf("card session IDs = %q, %q", events[0].SessionID, events[1].SessionID)
	}
}

func (c *resolutionCacheStub) Resolve(_ context.Context, _ media.Downloader, _ []media.Ref) media.Resolution {
	result := c.resolution
	release := result.Release
	result.Release = func() {
		c.releases++
		if release != nil {
			release()
		}
	}
	return result
}

func TestServiceResolvesAttachmentsBeforeDurableEnqueue(t *testing.T) {
	now := time.Now()
	imagePath := "/absolute/cache/image.png"
	textPath := "/absolute/cache/notes.md"
	cache := &resolutionCacheStub{resolution: media.Resolution{Attachments: []media.Attachment{
		{Path: imagePath, MIME: "image/png", Size: 7},
		{Path: textPath, MIME: "text/markdown", Size: 9},
	}}}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(testConfig(t), renderer, runner, audit.NewRecorder())
	configureTestMedia(svc, cache)
	if err := svc.HandleMessage(context.Background(), Message{
		ID: "media-success", ChatID: "chat", Sender: "alice", Text: "inspect these", Time: now,
		Attachments: []media.Ref{{MessageID: "media-success", FileKey: "image", Kind: "image"}, {MessageID: "media-success", FileKey: "notes", Kind: "file"}},
	}); err != nil {
		t.Fatal(err)
	}
	key := session.Key{Agent: agent.Claude, ChatID: "chat"}
	sess, ok := svc.Sessions.Get(key)
	if !ok || len(sess.Queue) != 1 || len(sess.Queue[0].Attachments) != 2 {
		t.Fatalf("durable queue = %#v, want two resolved attachments", sess)
	}
	if cache.releases != 1 {
		t.Fatalf("release calls = %d, want 1 after durable enqueue", cache.releases)
	}
	if err := svc.DrainReady(sess.Queue[0].DebounceUntil); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	prompt := runner.Calls()[0].Prompt
	if strings.Count(prompt, imagePath) != 1 || strings.Count(prompt, textPath) != 1 || !containsAll(prompt, "[image] "+imagePath, "[text-file] "+textPath, "inspect these") {
		t.Fatalf("agent prompt = %q", prompt)
	}
}

func TestServicePassesFrozenModelAndEffortToRunner(t *testing.T) {
	now := time.Now()
	runner := newFakeRunner()
	svc := NewService(testConfig(t), card.NewFakeRenderer(), runner, audit.NewRecorder())
	key := session.Key{Agent: agent.Claude, ChatID: "runtime-config"}
	_, _, err := svc.Sessions.EnqueueDurable(key, session.Input{
		ID:              "frozen-preference",
		Text:            "hello",
		WorkDir:         svc.Config.DefaultWorkDir,
		RequestedModel:  "opus",
		RequestedEffort: "high",
		Time:            now,
		DebounceUntil:   now,
		State:           session.InputQueued,
	}, svc.Config.DefaultWorkDir, session.BatchLimits{MaxPending: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	call := runner.Calls()[0]
	if call.Model != "opus" || call.Effort != "high" {
		t.Fatalf("runner preferences = model %q effort %q, want opus/high", call.Model, call.Effort)
	}
}

func TestServiceConfigCommandShowsCurrentPreferencesWithoutRunningAgent(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model = "default"
	cfg.Effort = "low"
	cfg.AllowedModels = []string{"default", "sonnet", "opus", "haiku", "claude-custom-1"}
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: "default", Effort: "low"}, cfg.AllowedModels)
	if err := store.Set(config.RuntimePreference{Model: "opus", Effort: "high", ReplyMode: config.ReplyModeLatestCard}); err != nil {
		t.Fatal(err)
	}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	svc.Preferences = store
	if err := svc.HandleMessage(context.Background(), Message{ID: "config-open", ChatID: "chat", Sender: "user", Text: "/config", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if len(events) != 1 || events[0].ConfigForm == nil || events[0].ConfigForm.Model != "opus" || events[0].ConfigForm.Effort != "high" || events[0].ConfigForm.ReplyMode != string(config.ReplyModeLatestCard) || events[0].ConfigForm.ConversationMode != string(config.ConversationModeChat) {
		t.Fatalf("config events = %#v", events)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("/config started Agent: %#v", runner.Calls())
	}
}

func TestServiceStatusShowsGlobalReplyModeBeforeSessionStarts(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model, cfg.Effort, cfg.ReplyMode = "default", "low", config.ReplyModeAppend
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: cfg.ReplyMode}, cfg.AllowedModels)
	if err := store.Set(config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppendCleanCard}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	status := svc.statusText(agent.Claude, Message{ChatID: "chat"})
	if !strings.Contains(status, "reply_mode=append-clean-card") || !strings.Contains(status, "conversation_mode=chat") {
		t.Fatalf("status = %q", status)
	}
}

func TestServiceChatConversationModeSharesSessionAcrossThreads(t *testing.T) {
	cfg := testConfig(t)
	cfg.ConversationMode = config.ConversationModeChat
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	now := time.Now()
	for _, msg := range []Message{
		{ID: "one", ChatID: "chat", ThreadID: "topic-a", Sender: "user", Text: "one", Time: now},
		{ID: "two", ChatID: "chat", ThreadID: "topic-b", Sender: "user", Text: "two", Time: now},
	} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	list := svc.Sessions.List()
	if len(list) != 1 || list[0].ID != "claude:chat" || len(list[0].Queue) != 2 {
		t.Fatalf("chat sessions = %#v", list)
	}
}

func TestServiceTopicConversationModeIsolatesSessionsAndRepliesInThread(t *testing.T) {
	cfg := testConfig(t)
	cfg.ConversationMode = config.ConversationModeTopic
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	now := time.Now()
	for _, msg := range []Message{
		{ID: "one", ChatID: "chat", ThreadID: "topic-a", Sender: "user", Text: "one", Time: now},
		{ID: "two", ChatID: "chat", ThreadID: "topic-b", Sender: "user", Text: "two", Time: now},
	} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	list := svc.Sessions.List()
	if len(list) != 2 {
		t.Fatalf("topic sessions = %#v", list)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "help", ChatID: "chat", ThreadID: "topic-a", Sender: "user", Text: "/help", Time: now}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if len(events) != 1 || !events[0].ReplyInThread {
		t.Fatalf("topic command events = %#v", events)
	}
}

func TestServiceFreezesConversationModeAtEnqueueTime(t *testing.T) {
	cfg := testConfig(t)
	cfg.ConversationMode = config.ConversationModeTopic
	defaults := config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeTopic}
	store, _ := testPreferenceStore(t, defaults, cfg.AllowedModels)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "queued-topic", ChatID: "chat", ThreadID: "topic-a", Sender: "user", Text: "hello", Time: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "config", ActionID: "config.save", Actor: "user", FormValues: map[string]string{"model": "default", "effort": "low", "reply_mode": "append", "conversation_mode": "chat"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForEvents(t, renderer, 3)
	foundRunEvent := false
	for _, event := range renderer.Events() {
		if event.ReplyToMessageID == "queued-topic" {
			foundRunEvent = true
			if !event.ReplyInThread {
				t.Fatalf("queued topic event lost frozen mode: %#v", event)
			}
		}
	}
	if !foundRunEvent {
		t.Fatalf("missing run event for queued topic input: %#v", renderer.Events())
	}
}

func TestServiceConfigResetRemovesOverride(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model, cfg.Effort = "sonnet", "low"
	cfg.AllowedModels = []string{"default", "sonnet", "opus", "haiku"}
	defaults := config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, Agent: config.DefaultAgentKind}
	store, path := testPreferenceStore(t, defaults, cfg.AllowedModels)
	if err := store.Set(config.RuntimePreference{Model: "opus", Effort: "high", ReplyMode: config.ReplyModeLatestCard}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	if err := svc.HandleMessage(context.Background(), Message{ID: "config-reset", ChatID: "chat", Sender: "user", Text: "/config reset", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got != defaults {
		t.Fatalf("preference after reset = %#v, want %#v", got, defaults)
	}
	reopened, err := config.OpenPreferenceStore(path, defaults, cfg.AllowedModels)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(); got != defaults {
		t.Fatalf("reopened preference after reset = %#v", got)
	}
}

func TestServiceConfigSavePersistsValidValuesAndRejectsInvalidValues(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model, cfg.Effort = "default", "low"
	cfg.AllowedModels = []string{"default", "sonnet", "opus", "haiku", "claude-custom-1"}
	defaults := config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort}
	store, path := testPreferenceStore(t, defaults, cfg.AllowedModels)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "config-card", ActionID: "config.save", Actor: "user", FormValues: map[string]string{"model": "claude-custom-1", "effort": "medium", "reply_mode": "latest-card", "conversation_mode": "topic"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.Event.Type != "config_saved" || store.Get() != (config.RuntimePreference{Model: "claude-custom-1", Effort: "medium", ReplyMode: config.ReplyModeLatestCard, ConversationMode: config.ConversationModeTopic, Agent: config.DefaultAgentKind}) {
		t.Fatalf("save result/store = %#v / %#v", result, store.Get())
	}
	reopened, err := config.OpenPreferenceStore(path, defaults, cfg.AllowedModels)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(); got != store.Get() {
		t.Fatalf("reopened preference = %#v, want %#v", got, store.Get())
	}
	before := store.Get()
	result, err = svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "config-card", ActionID: "config.save", Actor: "user", FormValues: map[string]string{"model": "unknown", "effort": "extreme", "reply_mode": "replace"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.Event.Type != "error" || store.Get() != before {
		t.Fatalf("invalid save result/store = %#v / %#v, want unchanged %#v", result, store.Get(), before)
	}
}

func TestServiceFreezesPreferencesAtEnqueueTime(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model, cfg.Effort = "sonnet", "low"
	cfg.AllowedModels = []string{"default", "sonnet", "opus", "haiku"}
	store, _ := testPreferenceStore(t, config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort}, cfg.AllowedModels)
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Preferences = store
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "before-config", ChatID: "chat", Sender: "user", Text: "first", Time: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "config-card", ActionID: "config.save", Actor: "user", FormValues: map[string]string{"model": "opus", "effort": "high"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "after-config", ChatID: "chat", Sender: "user", Text: "second", Time: now.Add(time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	sess, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"})
	if !ok || len(sess.Queue) != 2 {
		t.Fatalf("session queue = %#v", sess)
	}
	if first, second := sess.Queue[0], sess.Queue[1]; first.RequestedModel != "sonnet" || first.RequestedEffort != "low" || first.ReplyMode != config.ReplyModeAppend || second.RequestedModel != "opus" || second.RequestedEffort != "high" || second.ReplyMode != config.ReplyModeAppend {
		t.Fatalf("frozen queue preferences = %#v", sess.Queue)
	}
}

func TestServiceFreezesReplyModeBeforeQueuedBatchStarts(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model, cfg.Effort = "default", "low"
	cfg.ReplyMode = config.ReplyModeAppend
	preferences, _ := testPreferenceStore(t, config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: cfg.ReplyMode}, cfg.AllowedModels)
	replies, err := reply.OpenStore(filepath.Join(t.TempDir(), "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := replies.SetLatest("claude:chat", &session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 2}); err != nil {
		t.Fatal(err)
	}
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	target := &bridgeReplyTarget{}
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	svc.Preferences = preferences
	svc.Replies = replies
	svc.CardTarget = target
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "frozen-append", ChatID: "chat", Sender: "user", Text: "hello", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := preferences.Set(config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: config.ReplyModeLatestCard}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	target.mu.Lock()
	newCalls, rehydrateCalls := target.newCalls, target.rehydrateCalls
	target.mu.Unlock()
	if newCalls != 1 || rehydrateCalls != 0 {
		t.Fatalf("new/rehydrate calls = %d/%d, want frozen append mode", newCalls, rehydrateCalls)
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
}

func TestServiceRoutesBatchThroughReplyPolicyAndPersistsActiveRenderRef(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model, cfg.Effort = "default", "low"
	cfg.ReplyMode = config.ReplyModeAppend
	preferences, _ := testPreferenceStore(t, config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: cfg.ReplyMode}, cfg.AllowedModels)
	if err := preferences.Set(config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort, ReplyMode: config.ReplyModeLatestCard}); err != nil {
		t.Fatal(err)
	}
	replies, err := reply.OpenStore(filepath.Join(t.TempDir(), "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantRef := session.RenderRef{CardID: "latest-card", ReplyMessageID: "latest-reply", Version: 7}
	if err := replies.SetLatest("claude:chat", &wantRef); err != nil {
		t.Fatal(err)
	}
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	target := &bridgeReplyTarget{}
	fallback := card.NewFakeRenderer()
	svc := NewService(cfg, fallback, runner, audit.NewRecorder())
	svc.Preferences = preferences
	svc.Replies = replies
	svc.CardTarget = target
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "reply-mode", ChatID: "chat", Sender: "user", Text: "hello", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	sess, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"})
	if !ok || sess.ActiveBatch == nil || sess.ActiveBatch.RenderRef == nil {
		t.Fatalf("active session = %#v, want durable render ref", sess)
	}
	if got := *sess.ActiveBatch.RenderRef; got.CardID != wantRef.CardID || got.ReplyMessageID != wantRef.ReplyMessageID || got.Version != wantRef.Version+1 {
		t.Fatalf("active render ref = %#v, want advanced %#v", got, wantRef)
	}
	target.mu.Lock()
	newCalls, rehydrateCalls, rehydrated := target.newCalls, target.rehydrateCalls, target.rehydratedRef
	bindings := append([]feishu.RenderBinding(nil), target.bindings...)
	target.mu.Unlock()
	if newCalls != 0 || rehydrateCalls != 1 || rehydrated != wantRef {
		t.Fatalf("reply target new/rehydrate/ref = %d/%d/%#v", newCalls, rehydrateCalls, rehydrated)
	}
	if len(bindings) != 1 {
		t.Fatalf("reply target bindings = %#v, want one", bindings)
	}
	wantBinding := feishu.RenderBinding{
		BaseSessionID:    sess.ID,
		BatchID:          sess.ActiveBatch.ID,
		LatestScope:      sess.ID,
		RunCardSessionID: runCardSessionID(sess.ID, sess.ActiveBatch.Inputs[len(sess.ActiveBatch.Inputs)-1]),
	}
	if bindings[0] != wantBinding {
		t.Fatalf("reply target binding = %#v, want %#v", bindings[0], wantBinding)
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestReplyPolicyStartFailureKeepsFrozenTopicReplyMode(t *testing.T) {
	cfg := testConfig(t)
	fallback := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, fallback, runner, audit.NewRecorder())
	svc.CardTarget = &bridgeReplyTarget{newErr: errors.New("new streaming failed")}
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "topic-fallback", ChatID: "chat", ThreadID: "topic-a", Sender: "user", Text: "hello", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForEvents(t, fallback, 1)
	events := fallback.Events()
	if len(events) != 1 || events[0].Type != "error" || events[0].ReplyToMessageID != "topic-fallback" || !events[0].ReplyInThread {
		t.Fatalf("fallback events = %#v, want one in-thread error reply", events)
	}
	if calls := runner.Calls(); len(calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", calls)
	}
}

func TestServiceWaitingAndTypingReactionLifecycleForBatchedRun(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	sink := &fakeReactionSink{}
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	svc.Reactions = sink
	svc.reactionDelay = 5 * time.Millisecond
	now := time.Now()
	for _, msg := range []Message{
		{ID: "first", ChatID: "chat", Sender: "user", Text: "one", Time: now},
		{ID: "last", ChatID: "chat", Sender: "user", Text: "two", Time: now.Add(100 * time.Millisecond)},
	} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	waitForReactionCounts(t, sink, 2, 0)
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	waitForReactionCounts(t, sink, 3, 2)
	adds, _ := sink.snapshot()
	if adds[0].typeName != feishu.ReactionTypeOneSecond || adds[1].typeName != feishu.ReactionTypeOneSecond || adds[2] != (reactionCall{messageID: "last", typeName: feishu.ReactionTypeTyping}) {
		t.Fatalf("reaction adds = %#v", adds)
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
	waitForReactionCounts(t, sink, 3, 3)
	for _, event := range renderer.Events() {
		if event.Type == "reaction" {
			t.Fatalf("legacy reaction card event = %#v", event)
		}
	}
}

func TestServiceConfigPersistenceFailureShowsErrorAndAudits(t *testing.T) {
	cfg := testConfig(t)
	cfg.Model, cfg.Effort = "default", "low"
	cfg.AllowedModels = []string{"default", "sonnet", "opus", "haiku"}
	store, path := testPreferenceStore(t, config.RuntimePreference{Model: cfg.Model, Effort: cfg.Effort}, cfg.AllowedModels)
	if err := store.Set(config.RuntimePreference{Model: "sonnet", Effort: "medium"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	recorder := audit.NewRecorder()
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), recorder)
	svc.Preferences = store
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "config-card", ActionID: "config.save", Actor: "user", FormValues: map[string]string{"model": "opus", "effort": "high"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.Event.Type != "error" || store.Get() != (config.RuntimePreference{Model: "sonnet", Effort: "medium", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, Agent: config.DefaultAgentKind}) || !auditContainsAction(recorder.Events(), "config_save_failed") {
		t.Fatalf("failure result/store/audit = %#v / %#v / %#v", result, store.Get(), recorder.Events())
	}
}

func TestServiceKeepsSuccessfulAttachmentsAndShowsPartialFailureSummary(t *testing.T) {
	now := time.Now()
	cache := &resolutionCacheStub{resolution: media.Resolution{
		Attachments: []media.Attachment{{Path: "/absolute/cache/good.png", MIME: "image/png", Size: 7}},
		Failures:    []media.Failure{{Code: "download_failed", Detail: "upstream timeout"}},
	}}
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	configureTestMedia(svc, cache)
	if err := svc.HandleMessage(context.Background(), Message{ID: "media-partial", ChatID: "chat", Sender: "alice", Text: "inspect", Time: now, Attachments: []media.Ref{{FileKey: "good", Kind: "image"}, {FileKey: "bad", Kind: "file"}}}); err != nil {
		t.Fatal(err)
	}
	sess, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"})
	if !ok || len(sess.Queue) != 1 || len(sess.Queue[0].Attachments) != 1 {
		t.Fatalf("durable queue = %#v, want only successful attachment", sess)
	}
	if cache.releases != 1 || !eventsContainText(renderer.Events(), "download_failed") {
		t.Fatalf("release/events = %d/%#v, want released partial-failure summary", cache.releases, renderer.Events())
	}
}

func TestServiceRunsAttachmentOnlyMessageAndRejectsTotalFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		now := time.Now()
		path := "/absolute/cache/attachment.png"
		cache := &resolutionCacheStub{resolution: media.Resolution{Attachments: []media.Attachment{{Path: path, MIME: "image/png", Size: 7}}}}
		runner := newFakeRunner()
		svc := NewService(testConfig(t), card.NewFakeRenderer(), runner, audit.NewRecorder())
		configureTestMedia(svc, cache)
		if err := svc.HandleMessage(context.Background(), Message{ID: "attachment-only", ChatID: "chat", Sender: "alice", Time: now, Attachments: []media.Ref{{FileKey: "image", Kind: "image"}}}); err != nil {
			t.Fatal(err)
		}
		sess, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"})
		if !ok || len(sess.Queue) != 1 {
			t.Fatalf("session = %#v, want one attachment input", sess)
		}
		if err := svc.DrainReady(sess.Queue[0].DebounceUntil); err != nil {
			t.Fatal(err)
		}
		waitForCalls(t, runner, 1)
		if prompt := runner.Calls()[0].Prompt; !strings.Contains(prompt, "[image] "+path) || strings.Contains(prompt, "image_key") {
			t.Fatalf("attachment-only prompt = %q", prompt)
		}
		if cache.releases != 1 {
			t.Fatalf("release calls = %d, want 1", cache.releases)
		}
	})

	t.Run("total failure", func(t *testing.T) {
		cache := &resolutionCacheStub{resolution: media.Resolution{Failures: []media.Failure{{Code: "content_mismatch", Detail: "not an image"}}}}
		renderer := card.NewFakeRenderer()
		runner := newFakeRunner()
		svc := NewService(testConfig(t), renderer, runner, audit.NewRecorder())
		configureTestMedia(svc, cache)
		if err := svc.HandleMessage(context.Background(), Message{ID: "attachment-failure", ChatID: "chat", Sender: "alice", Attachments: []media.Ref{{FileKey: "bad", Kind: "image"}}, Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"}); ok || len(runner.Calls()) != 0 || !eventsContainText(renderer.Events(), "content_mismatch") {
			t.Fatalf("session/calls/events = %v/%d/%#v, want no enqueue or runner and visible failure", ok, len(runner.Calls()), renderer.Events())
		}
		if cache.releases != 1 {
			t.Fatalf("release calls = %d, want 1", cache.releases)
		}
	})
}

func TestServiceReleasesResolutionWhenDurableEnqueueFails(t *testing.T) {
	cache := &resolutionCacheStub{resolution: media.Resolution{Attachments: []media.Attachment{{Path: "/absolute/cache/image.png", MIME: "image/png", Size: 7}}}}
	storePath := filepath.Join(t.TempDir(), "missing-parent", "sessions.json")
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder(), session.NewManagerWithStore(storePath), nil)
	configureTestMedia(svc, cache)
	if err := svc.HandleMessage(context.Background(), Message{ID: "persist-failure", ChatID: "chat", Sender: "alice", Text: "inspect", Attachments: []media.Ref{{FileKey: "image", Kind: "image"}}, Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if cache.releases != 1 {
		t.Fatalf("release calls = %d, want 1 after enqueue failure", cache.releases)
	}
}

func TestServiceReleasesResolutionOnDuplicateAndReactionFailure(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		cache := &resolutionCacheStub{resolution: media.Resolution{Attachments: []media.Attachment{{Path: "/absolute/cache/image.png", MIME: "image/png", Size: 7}}}}
		svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
		configureTestMedia(svc, cache)
		msg := Message{ID: "duplicate", ChatID: "chat", Sender: "alice", Text: "inspect", Attachments: []media.Ref{{FileKey: "image", Kind: "image"}}, Time: time.Now()}
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		if cache.releases != 2 {
			t.Fatalf("release calls = %d, want 2 for accepted and duplicate paths", cache.releases)
		}
	})

	t.Run("reaction failure is non-fatal", func(t *testing.T) {
		cache := &resolutionCacheStub{resolution: media.Resolution{Attachments: []media.Attachment{{Path: "/absolute/cache/image.png", MIME: "image/png", Size: 7}}}}
		recorder := audit.NewRecorder()
		svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), recorder)
		svc.Reactions = &fakeReactionSink{addErr: errors.New("reaction unavailable")}
		svc.reactionDelay = 0
		configureTestMedia(svc, cache)
		err := svc.HandleMessage(context.Background(), Message{ID: "render-failure", ChatID: "chat", Sender: "alice", Text: "inspect", Attachments: []media.Ref{{FileKey: "image", Kind: "image"}}, Time: time.Now()})
		if err != nil || cache.releases != 1 {
			t.Fatalf("handle/release = %v/%d, want successful enqueue and one release", err, cache.releases)
		}
		waitForAuditAction(t, recorder, "reaction_add_failed")
	})
}

func TestServiceRejectsNilMediaDownloaderBeforeResolve(t *testing.T) {
	cache := &nilRejectingDownloaderCache{}
	renderer := card.NewFakeRenderer()
	svc := NewService(testConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.MediaCache = cache
	if err := svc.HandleMessage(context.Background(), Message{ID: "nil-downloader", ChatID: "chat", Sender: "alice", Attachments: []media.Ref{{FileKey: "image", Kind: "image"}}, Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if cache.called || !eventsContainText(renderer.Events(), "media_unavailable") {
		t.Fatalf("resolve/events = %v/%#v, want no resolve and visible media_unavailable", cache.called, renderer.Events())
	}
	if _, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"}); ok {
		t.Fatal("attachment-only unavailable media created a session")
	}
}

func TestServiceReturnsAttachmentFailureSummaryRenderError(t *testing.T) {
	t.Run("attachment-only total failure", func(t *testing.T) {
		cache := &resolutionCacheStub{resolution: media.Resolution{Failures: []media.Failure{{Code: "content_mismatch"}}}}
		svc := NewService(testConfig(t), attachmentFailureSummaryRenderer{}, newFakeRunner(), audit.NewRecorder())
		configureTestMedia(svc, cache)
		err := svc.HandleMessage(context.Background(), Message{ID: "total-summary-error", ChatID: "chat", Sender: "alice", Attachments: []media.Ref{{FileKey: "bad", Kind: "image"}}, Time: time.Now()})
		if !errors.Is(err, errAttachmentFailureSummary) || cache.releases != 1 {
			t.Fatalf("handle/release = %v/%d, want summary render error and one release", err, cache.releases)
		}
		if _, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"}); ok {
			t.Fatal("total failure created a session")
		}
	})

	t.Run("partial failure still enqueues", func(t *testing.T) {
		cache := &resolutionCacheStub{resolution: media.Resolution{
			Attachments: []media.Attachment{{Path: "/absolute/cache/good.png", MIME: "image/png", Size: 7}},
			Failures:    []media.Failure{{Code: "download_failed"}},
		}}
		svc := NewService(testConfig(t), attachmentFailureSummaryRenderer{}, newFakeRunner(), audit.NewRecorder())
		configureTestMedia(svc, cache)
		err := svc.HandleMessage(context.Background(), Message{ID: "partial-summary-error", ChatID: "chat", Sender: "alice", Text: "inspect", Attachments: []media.Ref{{FileKey: "good", Kind: "image"}, {FileKey: "bad", Kind: "file"}}, Time: time.Now()})
		if !errors.Is(err, errAttachmentFailureSummary) || cache.releases != 1 {
			t.Fatalf("handle/release = %v/%d, want summary render error and one release", err, cache.releases)
		}
		sess, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"})
		if !ok || len(sess.Queue) != 1 || len(sess.Queue[0].Attachments) != 1 {
			t.Fatalf("session = %#v, want durable partial attachment input", sess)
		}
	})
}

func eventsContainText(events []card.Event, want string) bool {
	for _, event := range events {
		for _, segment := range event.Segments {
			if strings.Contains(segment.Text, want) {
				return true
			}
		}
	}
	return false
}

func TestServiceNewRunsClaudeOneShotAndRendersResult(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.results = []AgentRunResult{{Model: "claude-sonnet", Tokens: 42, AgentSessionID: "sess-1", Segments: []card.Segment{
		{Kind: card.SegmentText, Text: "answer"},
		{Kind: card.SegmentThought, Text: "thinking"},
		{Kind: card.SegmentTool, Text: "Bash(ls)"},
	}}}
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg := Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(msg.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForEvents(t, renderer, 2)
	calls := runner.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	if calls[0].Kind != agent.Claude || calls[0].Prompt != "hello" || calls[0].AgentSessionID != "" {
		t.Fatalf("runner call = %#v", calls[0])
	}
	events := renderer.Events()
	initial := events[len(events)-2]
	if initial.ReplyToMessageID != "msg-1" || !initial.StopButton.Visible {
		t.Fatalf("initial event = %#v", initial)
	}
	if initial.HeaderTemplate != "blue" || !containsAll(initial.HeaderTitle, "正在推理", "⏱") {
		t.Fatalf("initial header = %q/%q", initial.HeaderTemplate, initial.HeaderTitle)
	}
	last := events[len(events)-1]
	if last.Type != "result" || last.Meta.Model != "claude-sonnet" || last.Meta.RunTokens != 42 || last.Meta.TotalTokens != 42 || last.HeaderTemplate != "green" {
		t.Fatalf("result event = %#v", last)
	}
	if !last.StopButton.Visible || !last.StopButton.Disabled {
		t.Fatalf("completed event should show disabled stop button: %#v", last.StopButton)
	}
	status := svc.statusText(agent.Claude, Message{ChatID: "chat"})
	if !containsAll(status, "agent_session=sess-1", "state=idle") {
		t.Fatalf("status = %q, want stored claude session", status)
	}
}

func TestServiceSeparatesRequestedAndActualModel(t *testing.T) {
	t.Run("reported actual and mismatch audit", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.Model, cfg.Effort = "opus", "high"
		renderer := card.NewFakeRenderer()
		runner := newFakeRunner()
		runner.results = []AgentRunResult{{Model: "claude-opus-4-1", Segments: []card.Segment{{Kind: card.SegmentText, Text: "answer"}}}}
		recorder := audit.NewRecorder()
		svc := NewService(cfg, renderer, runner, recorder)
		now := time.Now()
		if err := svc.HandleMessage(context.Background(), Message{ID: "model-provenance", ChatID: "chat", Sender: "user", Text: "hello", Time: now}); err != nil {
			t.Fatal(err)
		}
		if err := svc.DrainReady(now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		waitForCalls(t, runner, 1)
		waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
		events := renderer.Events()
		var initial, terminal *card.Event
		for i := range events {
			switch events[i].Type {
			case "stream":
				if initial == nil {
					initial = &events[i]
				}
			case "result":
				terminal = &events[i]
			}
		}
		if initial == nil || initial.Meta.ModelInfo.Requested != "opus" || initial.Meta.ModelInfo.Effort != "high" || initial.Meta.ModelInfo.Actual != "" {
			t.Fatalf("initial model provenance = %#v", initial)
		}
		if terminal == nil || terminal.Meta.ModelInfo.Requested != "opus" || terminal.Meta.ModelInfo.Actual != "claude-opus-4-1" || terminal.Meta.ModelInfo.Effort != "high" {
			t.Fatalf("terminal model provenance = %#v", terminal)
		}
		if !auditContainsAction(recorder.Events(), "model_requested_actual_mismatch") {
			t.Fatalf("audit = %#v, want model_requested_actual_mismatch", recorder.Events())
		}
	})

	t.Run("missing actual remains unknown", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.Model, cfg.Effort = "sonnet", "medium"
		renderer := card.NewFakeRenderer()
		runner := newFakeRunner()
		runner.results = []AgentRunResult{{Segments: []card.Segment{{Kind: card.SegmentText, Text: "answer"}}}}
		svc := NewService(cfg, renderer, runner, audit.NewRecorder())
		now := time.Now()
		if err := svc.HandleMessage(context.Background(), Message{ID: "model-unknown", ChatID: "chat", Sender: "user", Text: "hello", Time: now}); err != nil {
			t.Fatal(err)
		}
		if err := svc.DrainReady(now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
		events := renderer.Events()
		terminal := events[len(events)-1]
		if terminal.Meta.ModelInfo.Requested != "sonnet" || terminal.Meta.ModelInfo.Actual != "" {
			t.Fatalf("terminal model provenance = %#v", terminal.Meta.ModelInfo)
		}
		payload := card.BuildLarkCard(terminal)
		var text string
		for _, raw := range payload["body"].(map[string]any)["elements"].([]any) {
			element := raw.(map[string]any)
			if id, _ := element["element_id"].(string); id == "meta_primary" || id == "meta_runtime" {
				text += element["content"].(string)
			}
		}
		// v2:model 用实际值,缺失显 unknown;不再泄漏 requested 值(sonnet)。
		if !strings.Contains(text, "unknown") || strings.Contains(text, "sonnet") {
			t.Fatalf("rendered model provenance = %q", text)
		}
	})

	t.Run("stream-only actual reaches durable result and audit", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.Model, cfg.Effort = "opus", "high"
		runner := newFakeRunner()
		runner.updates = []AgentStreamUpdate{{Model: "claude-stream-only"}}
		runner.results = []AgentRunResult{{Segments: []card.Segment{{Kind: card.SegmentText, Text: "answer"}}}}
		recorder := audit.NewRecorder()
		svc := NewService(cfg, card.NewFakeRenderer(), runner, recorder)
		now := time.Now()
		key := session.Key{Agent: agent.Claude, ChatID: "stream-model"}
		if err := svc.HandleMessage(context.Background(), Message{ID: "stream-only-model", ChatID: key.ChatID, Sender: "user", Text: "hello", Time: now}); err != nil {
			t.Fatal(err)
		}
		if err := svc.DrainReady(now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		waitForSessionNoActiveBatch(t, svc, key)
		sess, ok := svc.Sessions.Get(key)
		if !ok || sess.Model != "claude-stream-only" || !auditContainsAction(recorder.Events(), "model_requested_actual_mismatch") {
			t.Fatalf("session/audit = %#v / %#v", sess, recorder.Events())
		}
	})
}

func TestServiceQueuesRunUntilExplicitDrain(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "queued-1", ChatID: "chat", Sender: "u", Text: "/new hello", Time: now}); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls before drain = %d, want 0", got)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
}

func TestServiceBatchesPlainDMInputsWithinDebounceCohort(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	for _, msg := range []Message{
		{ID: "dm-1", ChatID: "dm-chat", Sender: "u", Text: "first", Time: now},
		{ID: "dm-2", ChatID: "dm-chat", Sender: "u", Text: "second", Time: now.Add(100 * time.Millisecond)},
	} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatalf("handle %s: %v", msg.ID, err)
		}
	}
	if err := svc.DrainReady(now.Add(349 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls before DM cohort deadline = %d, want 0", got)
	}
	if err := svc.DrainReady(now.Add(350 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	calls := runner.Calls()
	if len(calls) != 1 || !containsAll(calls[0].Prompt, "first", "second") || strings.Index(calls[0].Prompt, "first") > strings.Index(calls[0].Prompt, "second") {
		t.Fatalf("runner calls = %#v, want one ordered DM batch", calls)
	}
}

func TestServiceFreezesInteractiveDebounceWindowAtIntake(t *testing.T) {
	cfg := testConfig(t)
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{
		ID: "interactive-window", ChatID: "chat", Sender: "u", Text: "card payload", MessageType: "interactive", Time: now,
	}); err != nil {
		t.Fatal(err)
	}
	sess, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"})
	if !ok || len(sess.Queue) != 1 {
		t.Fatalf("session = %#v, want one queued input", sess)
	}
	if got := sess.Queue[0].DebounceWindow; got != time.Second {
		t.Fatalf("debounce window = %s, want 1s", got)
	}
}

func TestServiceBatchesInteractiveAndText(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	receivedBefore := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{
		ID: "interactive", ChatID: "chat", Sender: "u", Text: "rich card payload", MessageType: "interactive", Time: receivedBefore,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(receivedBefore.Add(750 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls at 750ms = %d, want rich message still debouncing", got)
	}
	if err := svc.HandleMessage(context.Background(), Message{
		ID: "text", ChatID: "chat", Sender: "u", Text: "follow-up text", MessageType: "text", Time: receivedBefore.Add(750 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	calls := runner.Calls()
	if len(calls) != 1 || !containsAll(calls[0].Prompt, "rich card payload", "follow-up text") {
		t.Fatalf("runner calls = %#v, want one interactive+text batch", calls)
	}
}

func TestServiceBatchesImageAndText(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	imagePath := "/cache/sha-image.png"
	configureTestMedia(svc, &resolutionCacheStub{resolution: media.Resolution{Attachments: []media.Attachment{{Path: imagePath, MIME: "image/png", Size: 7}}}})
	receivedBefore := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{
		ID: "image", ChatID: "chat", Sender: "u", MessageType: "image", Time: receivedBefore,
		Attachments: []media.Ref{{MessageID: "image", FileKey: "img-key", Kind: "image"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(receivedBefore.Add(750 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls at 750ms = %d, want image still debouncing", got)
	}
	if err := svc.HandleMessage(context.Background(), Message{
		ID: "text", ChatID: "chat", Sender: "u", Text: "describe this image", MessageType: "text", Time: receivedBefore.Add(750 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	calls := runner.Calls()
	if len(calls) != 1 || !containsAll(calls[0].Prompt, imagePath, "describe this image") {
		t.Fatalf("runner calls = %#v, want one image+text batch", calls)
	}
}

func TestServiceStartsDMDebounceFromLocalReceiptTime(t *testing.T) {
	cfg := testConfig(t)
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	receivedAfter := time.Now()
	platformTime := receivedAfter.Add(-time.Second)
	for _, msg := range []Message{
		{ID: "dm-stale-1", ChatID: "dm-stale-chat", Sender: "u", Text: "first", Time: platformTime},
		{ID: "dm-stale-2", ChatID: "dm-stale-chat", Sender: "u", Text: "second", Time: platformTime.Add(52 * time.Millisecond)},
	} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatalf("handle %s: %v", msg.ID, err)
		}
	}
	sess, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "dm-stale-chat"})
	if !ok || len(sess.Queue) != 2 {
		t.Fatalf("queued session = %#v, want two DM inputs", sess)
	}
	for _, input := range sess.Queue {
		if !input.DebounceUntil.After(receivedAfter) {
			t.Fatalf("debounce deadline = %s, must start after local receipt %s", input.DebounceUntil, receivedAfter)
		}
	}
}

func TestServiceBatchesPlainGroupInputsWithinDebounceCohort(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	for _, msg := range []Message{
		{ID: "group-1", ChatID: "group-chat", Sender: "u", Text: "first", Time: now, IsGroup: true, Mentioned: true},
		{ID: "group-2", ChatID: "group-chat", Sender: "u", Text: "second", Time: now.Add(100 * time.Millisecond), IsGroup: true, Mentioned: true},
	} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatalf("handle %s: %v", msg.ID, err)
		}
	}
	if err := svc.DrainReady(now.Add(699 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls before group cohort deadline = %d, want 0", got)
	}
	if err := svc.DrainReady(now.Add(700 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	calls := runner.Calls()
	if len(calls) != 1 || !containsAll(calls[0].Prompt, "first", "second") || strings.Index(calls[0].Prompt, "first") > strings.Index(calls[0].Prompt, "second") {
		t.Fatalf("runner calls = %#v, want one ordered group batch", calls)
	}
}

func TestInitialCardRenderFailureDoesNotStartRunnerOrLeakActiveRun(t *testing.T) {
	cfg := testConfig(t)
	fake := card.NewFakeRenderer()
	renderer := &failOnceRenderer{next: fake}
	runner := newFakeRunner()
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, runner, recorder)
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls after failed card render = %#v, want none", runner.Calls())
	}
	status := svc.statusText(agent.Claude, Message{ChatID: "chat"})
	if !containsAll(status, "state=idle", "queue=0") {
		t.Fatalf("status after failed card render = %q, want idle with empty queue", status)
	}
	foundAudit := false
	for _, event := range recorder.Events() {
		if event.Action == "card_render_failed" && strings.Contains(event.Detail, "reply message revoked") {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %#v, want card_render_failed", recorder.Events())
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "/new second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].Prompt; got != "second" {
		t.Fatalf("runner prompt = %q, want second", got)
	}
	for _, event := range fake.Events() {
		if event.Type == "reaction" && event.Message == "queued" {
			t.Fatalf("second message was queued after failed card render: %#v", event)
		}
	}
}

func TestServiceStreamsRunnerUpdatesIntoSameCard(t *testing.T) {
	cfg := testConfig(t)
	cfg.CardUpdateEvery = time.Nanosecond
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.updates = []AgentStreamUpdate{
		{Segments: []card.Segment{{Kind: card.SegmentThought, Text: "plan"}}, Activity: streamActivityReasoning},
		{Segments: []card.Segment{{Kind: card.SegmentTool, Text: "Bash(ls)"}}, Activity: streamActivityTool},
		{Segments: []card.Segment{{Kind: card.SegmentText, Text: "answer"}}, Activity: streamActivityAnswering, Model: "claude-stream", Tokens: 3},
	}
	runner.results = []AgentRunResult{{Model: "claude-stream", Tokens: 3, Segments: []card.Segment{
		{Kind: card.SegmentText, Text: "answer"},
	}}}
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new hello", Time: time.Now()}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForEvents(t, renderer, 5)
	events := renderer.Events()
	// v2:面板固定折叠(ProcessExpanded=false),阶段仍由 Activity 反映。
	if events[1].Type != "stream" || events[1].Activity != streamActivityReasoning {
		t.Fatalf("thought stream event = %#v", events[1])
	}
	if events[2].Type != "stream" || events[2].Activity != streamActivityTool {
		t.Fatalf("tool stream event = %#v", events[2])
	}
	last := events[len(events)-1]
	if last.Type != "result" || last.HeaderTemplate != "green" {
		t.Fatalf("last event = %#v", last)
	}
	if !last.StopButton.Visible || !last.StopButton.Disabled {
		t.Fatalf("completed event should show disabled stop button: %#v", last.StopButton)
	}
	if last.Meta.RunTokens != 3 || last.Meta.TotalTokens != 3 {
		t.Fatalf("last meta = %#v, want run and total tokens", last.Meta)
	}
	if !last.OrderedLayout || len(last.Segments) != 3 ||
		last.Segments[0].Kind != card.SegmentThought || !containsAll(last.Segments[0].Text, "plan") ||
		last.Segments[1].Kind != card.SegmentTool || !containsAll(last.Segments[1].Text, "Bash(ls)") ||
		last.Segments[2].Kind != card.SegmentText || !containsAll(last.Segments[2].Text, "answer") {
		t.Fatalf("final segments = %#v", last.Segments)
	}
}

func TestServiceNewWithoutPromptCreatesReadySession(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new", Time: now}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForEvents(t, renderer, 2)
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls())
	}
	events := renderer.Events()
	if len(events) < 2 || events[len(events)-1].Type != "result" {
		t.Fatalf("events = %#v, want initial and ready terminal cards", events)
	}
}

func TestPlainTextAfterReadySessionKeepsWorkDir(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	workDir := t.TempDir()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + workDir, Time: time.Now()}); err != nil {
		t.Fatalf("ready message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForEvents(t, renderer, 2)
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none for ready session", runner.Calls())
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "show pwd", Time: time.Now()}); err != nil {
		t.Fatalf("plain message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	calls := runner.Calls()
	if calls[0].WorkDir != workDir {
		t.Fatalf("runner workdir = %q, want %q", calls[0].WorkDir, workDir)
	}
	if calls[0].AgentSessionID != "" {
		t.Fatalf("ready scope has no stored Claude session yet, got %q", calls[0].AgentSessionID)
	}
	waitForEvents(t, renderer, 3)
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Meta.WorkDir != workDir {
		t.Fatalf("result workdir = %q, want %q", last.Meta.WorkDir, workDir)
	}
}

func TestTopicPlainTextContinuesStoredClaudeSession(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.results = []AgentRunResult{
		{AgentSessionID: "sess-topic", Tokens: 2, Segments: []card.Segment{{Kind: card.SegmentText, Text: "first"}}},
		{AgentSessionID: "sess-topic", Tokens: 3, Segments: []card.Segment{{Kind: card.SegmentText, Text: "second"}}},
	}
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg1 := Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(msg1.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForEvents(t, renderer, 2)
	msg2 := Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "second", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(msg2.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[1].AgentSessionID != "sess-topic" {
		t.Fatalf("second call session id = %q, want sess-topic", calls[1].AgentSessionID)
	}
	waitForEvents(t, renderer, 4)
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Meta.RunTokens != 3 || last.Meta.TotalTokens != 5 {
		t.Fatalf("second run meta = %#v, want run=3 total=5", last.Meta)
	}
}

func TestServiceRunsConfiguredCodexPresetWithImagesAndResumesThread(t *testing.T) {
	cfg := testConfig(t)
	cfg.DefaultAgent = "claude"
	cfg.ConversationMode = config.ConversationModeChat
	agents := config.AgentsConfig{
		SchemaVersion: config.AgentsSchemaVersion,
		Agents: []config.AgentDef{{
			Kind:  "codex",
			Label: "Codex CLI",
			Homes: []config.AgentHome{{Label: config.DefaultHomeLabel}, {Label: "workspace", Path: "/h/codex"}},
			Bins:  []config.AgentBin{{Label: config.DefaultBinLabelFor("codex")}, {Label: "cx3", Path: "/b/cx3"}},
		}},
	}
	defaults := config.RuntimePreference{Model: "default", Effort: "low", ReplyMode: config.ReplyModeAppend, ConversationMode: config.ConversationModeChat, Agent: "codex", AgentHome: "workspace", AgentBin: "cx3"}
	store, err := config.OpenPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"), defaults, cfg.AllowedModels, agents.Agents...)
	if err != nil {
		t.Fatal(err)
	}
	runner := newFakeRunner()
	runner.results = []AgentRunResult{
		{AgentSessionID: "thread-codex", Tokens: 2, Segments: []card.Segment{{Kind: card.SegmentText, Text: "first"}}},
		{AgentSessionID: "thread-codex", Tokens: 3, Segments: []card.Segment{{Kind: card.SegmentText, Text: "second"}}},
	}
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	svc.Agents = agents
	svc.Preferences = store
	configureTestMedia(svc, &resolutionCacheStub{resolution: media.Resolution{Attachments: []media.Attachment{
		{Path: "/cache/image.png", MIME: "image/png", Size: 1},
		{Path: "/cache/notes.txt", MIME: "text/plain", Size: 1},
	}}})
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "codex-1", ChatID: "chat", Sender: "u", Text: "inspect", Time: now, Attachments: []media.Ref{{FileKey: "image", Kind: "image"}, {FileKey: "notes", Kind: "file"}}}); err != nil {
		t.Fatal(err)
	}
	queued, ok := svc.Sessions.Get(session.Key{Agent: agent.Codex, ChatID: "chat"})
	if !ok || len(queued.Queue) != 1 {
		t.Fatalf("session = %#v, want one Codex attachment input", queued)
	}
	if err := svc.DrainReady(queued.Queue[0].DebounceUntil); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Codex, ChatID: "chat"})
	first := runner.Calls()[0]
	if first.Kind != agent.Codex || first.Bin != "/b/cx3" || first.Home != "/h/codex" || len(first.Images) != 1 || first.Images[0] != "/cache/image.png" {
		t.Fatalf("first Codex call = %#v", first)
	}
	if !strings.Contains(first.Prompt, "/cache/notes.txt") {
		t.Fatalf("prompt = %q", first.Prompt)
	}
	events := renderer.Events()
	if got := events[len(events)-1].Meta.ModelInfo; got != (card.ModelInfo{}) {
		t.Fatalf("Codex model provenance = %#v, want executable-owned empty metadata", got)
	}

	secondAt := now.Add(2 * time.Second)
	if err := svc.HandleMessage(context.Background(), Message{ID: "codex-2", ChatID: "chat", Sender: "u", Text: "continue", Time: secondAt}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(secondAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := runner.Calls()[1].AgentSessionID; got != "thread-codex" {
		t.Fatalf("resumed thread = %q", got)
	}
	status := svc.statusTextWithPreference(agent.Codex, Message{ChatID: "chat"}, store.Get())
	if !containsAll(status, "mode=codex_oneshot", "agent=codex", "agent_session=thread-codex", "model=由 Codex 配置决定") || strings.Contains(status, "claude_session=") {
		t.Fatalf("Codex status = %q", status)
	}
}

func TestNewInTopicResetsStoredClaudeSession(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.results = []AgentRunResult{
		{AgentSessionID: "sess-old", Segments: []card.Segment{{Kind: card.SegmentText, Text: "old"}}},
		{AgentSessionID: "sess-new", Segments: []card.Segment{{Kind: card.SegmentText, Text: "new"}}},
	}
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg1 := Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg1); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(msg1.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForEvents(t, renderer, 2)
	msg2 := Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new second", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg2); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(msg2.Time.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[1].AgentSessionID != "" {
		t.Fatalf("new call session id = %q, want reset", calls[1].AgentSessionID)
	}
}

func TestServiceQueuesSecondInputUntilFirstCompletes(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	sess, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic-a"})
	if !ok || len(sess.Queue) != 1 || sess.Queue[0].ID != "msg-2" {
		t.Fatalf("queued session = %#v", sess)
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic-a"})
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[1].Prompt != "second" {
		t.Fatalf("second runner prompt = %q", calls[1].Prompt)
	}
}

func TestServiceRearmsBusyRichMessageCohort(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())

	if err := svc.HandleMessage(context.Background(), Message{ID: "active", ChatID: "chat", Sender: "u", Text: "active", MessageType: "text", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "rich", ChatID: "chat", Sender: "u", Text: "rich payload", MessageType: "interactive", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	queuedBeforeCompletion, ok := svc.Sessions.Get(session.Key{Agent: agent.Claude, ChatID: "chat"})
	if !ok || len(queuedBeforeCompletion.Queue) != 1 {
		t.Fatalf("queued session = %#v", queuedBeforeCompletion)
	}
	deadlineBeforeCompletion := queuedBeforeCompletion.Queue[0].DebounceUntil
	close(runner.block)
	key := session.Key{Agent: agent.Claude, ChatID: "chat"}
	waitForSessionNoActiveBatch(t, svc, key)

	sess, ok := svc.Sessions.Get(key)
	if !ok || len(sess.Queue) != 1 {
		t.Fatalf("session = %#v, want one rearmed rich input", sess)
	}
	deadline := sess.Queue[0].DebounceUntil
	if !deadline.After(deadlineBeforeCompletion) {
		t.Fatalf("rearmed deadline = %s, want after receipt deadline %s", deadline, deadlineBeforeCompletion)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "follow-up", ChatID: "chat", Sender: "u", Text: "follow-up text", MessageType: "text", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(deadline.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("runner calls before rearmed deadline = %d, want 1", got)
	}
	if err := svc.DrainReady(deadline); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if !containsAll(calls[1].Prompt, "rich payload", "follow-up text") || strings.Index(calls[1].Prompt, "rich payload") > strings.Index(calls[1].Prompt, "follow-up text") {
		t.Fatalf("second runner prompt = %q, want ordered rich cohort", calls[1].Prompt)
	}
}

func TestDifferentTopicsRunInParallel(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: time.Now()}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-b", Sender: "u1", Text: "/new second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	close(runner.block)
}

func TestServiceMissingWorkdirAsksThenRunsAfterCreate(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second, ConversationMode: config.ConversationModeTopic}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg := Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + missing + " hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 1 || events[0].Type != "workdir_confirm" {
		t.Fatalf("events = %#v, want workdir confirm", events)
	}
	if events[0].SessionID != "claude:chat:message:msg-1" {
		t.Fatalf("confirm session id = %q", events[0].SessionID)
	}
	if !events[0].ReplyInThread {
		t.Fatalf("workdir confirmation did not freeze topic reply mode: %#v", events[0])
	}
	svc.Config.ConversationMode = config.ConversationModeChat
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "create_workdir", Actor: "u1"})
	if err != nil {
		t.Fatalf("create action error: %v", err)
	}
	if result.Event == nil || result.Event.Type != "workdir_created" || result.Event.SessionID != "claude:chat:message:msg-1" {
		t.Fatalf("action result = %#v, want workdir_created on confirm card", result.Event)
	}
	if len(result.Event.Actions) != 2 || !result.Event.Actions[0].Disabled || !result.Event.Actions[1].Disabled {
		t.Fatalf("terminal actions = %#v, want disabled", result.Event.Actions)
	}
	if _, err := os.Stat(missing); err != nil {
		t.Fatalf("missing dir was not created: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].WorkDir; got != missing {
		t.Fatalf("runner workdir = %q, want %q", got, missing)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
	events = renderer.Events()
	if events[1].Type != "workdir_created" || events[1].SessionID != "claude:chat:message:msg-1" {
		t.Fatalf("terminal confirm event = %#v", events[1])
	}
	runEvent := events[2]
	if runEvent.Type != "stream" || runEvent.SessionID == events[1].SessionID || runEvent.ReplyToMessageID != "msg-1" || !runEvent.ReplyInThread {
		t.Fatalf("run event = %#v, want separate card replying to original message", runEvent)
	}
}

func TestPlainTextAfterCreatedWorkDirKeepsWorkDir(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + missing, Time: time.Now()}); err != nil {
		t.Fatalf("handle missing workdir error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 1 || events[0].Type != "workdir_confirm" {
		t.Fatalf("events = %#v, want workdir confirm", events)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: events[0].SessionID, ActionID: "create_workdir", Actor: "u1"}); err != nil {
		t.Fatalf("create action error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none for empty ready session", runner.Calls())
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "show pwd", Time: time.Now()}); err != nil {
		t.Fatalf("plain message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].WorkDir; got != missing {
		t.Fatalf("runner workdir = %q, want %q", got, missing)
	}
	waitForEvents(t, renderer, 5)
	events = renderer.Events()
	last := events[len(events)-1]
	if last.Meta.WorkDir != missing {
		t.Fatalf("result workdir = %q, want %q", last.Meta.WorkDir, missing)
	}
}

func TestWorkdirCancelDoesNotRun(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	msg := Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + missing + " hello", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "cancel_workdir", Actor: "u1"})
	if err != nil {
		t.Fatalf("cancel action error: %v", err)
	}
	if result.Event == nil || result.Event.Type != "workdir_cancelled" || result.Event.HeaderTemplate != "" {
		t.Fatalf("cancel result = %#v, want workdir_cancelled default grey card", result.Event)
	}
	if len(result.Event.Actions) != 2 || !result.Event.Actions[0].Disabled || !result.Event.Actions[1].Disabled {
		t.Fatalf("cancel actions = %#v, want disabled", result.Event.Actions)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir stat = %v, want not exist", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls())
	}
	events := renderer.Events()
	if events[len(events)-1].Type != "workdir_cancelled" {
		t.Fatalf("last event = %#v, want workdir_cancelled", events[len(events)-1])
	}
}

func TestConcurrentMissingWorkdirConfirmationsAreIsolatedByMessage(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())

	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + first + " first", Time: now}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + second + " second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}

	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events = %#v, want two confirmations", events)
	}
	if events[0].SessionID == events[1].SessionID {
		t.Fatalf("confirmations share session id %q", events[0].SessionID)
	}
	if !containsAll(events[0].Segments[0].Text, first) || !containsAll(events[1].Segments[0].Text, second) {
		t.Fatalf("confirmation texts = %#v", events)
	}

	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: events[1].SessionID, ActionID: "create_workdir", Actor: "u1"}); err != nil {
		t.Fatalf("second create action error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := runner.Calls()[0].WorkDir; got != second {
		t.Fatalf("first runner workdir = %q, want %q", got, second)
	}

	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: events[0].SessionID, ActionID: "create_workdir", Actor: "u1"}); err != nil {
		t.Fatalf("first create action error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := runner.Calls()[1].WorkDir; got != first {
		t.Fatalf("second runner workdir = %q, want %q", got, first)
	}
}

func TestServiceStopCancelsActiveOneShotRun(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new long", Time: time.Now()}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "stop", Actor: "u1"})
	if err != nil {
		t.Fatalf("stop action error: %v", err)
	}
	if result.Event == nil || result.Event.Type != "stopped" || !result.Event.StopButton.Disabled {
		t.Fatalf("stop action result = %#v, want disabled stopped", result.Event)
	}
	prepared, err := result.PrepareCard(cfg.CardMaxChars)
	if err != nil {
		t.Fatalf("PrepareCard() error: %v", err)
	}
	payload := prepared.PayloadCopy()
	elements := payload["body"].(map[string]any)["elements"].([]any)
	var button map[string]any
	for _, raw := range elements {
		element := raw.(map[string]any)
		if element["element_id"] == "btn_stop" {
			button = element
			break
		}
	}
	if button == nil {
		t.Fatalf("sync stop card elements = %#v, want stop button", elements)
	}
	if button["disabled"] != true {
		t.Fatalf("sync stop button = %#v, want disabled", button)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "stopped" || !last.StopButton.Disabled || last.HeaderTemplate != "grey" {
		t.Fatalf("stop event = %#v", last)
	}
}

func TestServiceStopIsIdempotentForAlreadyStoppedRun(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new long", Time: time.Now()}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	req := ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "stop", Actor: "u1"}
	if _, err := svc.HandleActionResult(context.Background(), req); err != nil {
		t.Fatalf("first stop error: %v", err)
	}
	// A second stop against the same already-stopped run must not error and must
	// still yield a disabled stopped card, so a double click degrades cleanly.
	result, err := svc.HandleActionResult(context.Background(), req)
	if err != nil {
		t.Fatalf("second stop error: %v", err)
	}
	if result.Event == nil || result.Event.Type != "stopped" || !result.Event.StopButton.Disabled {
		t.Fatalf("second stop result = %#v, want disabled stopped", result.Event)
	}
}

func TestServiceStopUnknownSessionDegradesToStoppedCard(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	// No active run exists for this session id (stale/expired action). The stop
	// must degrade to a disabled stopped card rather than error.
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "claude:chat:message:missing", ActionID: "stop", Actor: "u1"})
	if err != nil {
		t.Fatalf("stale stop error: %v", err)
	}
	if result.Event == nil || result.Event.Type != "stopped" || !result.Event.StopButton.Disabled {
		t.Fatalf("stale stop result = %#v, want disabled stopped", result.Event)
	}
}

func TestServiceStopSyncCardDisablesStreamingMode(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new long", Time: time.Now()}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	result, err := svc.HandleActionResult(context.Background(), ActionRequest{SessionID: "claude:chat:message:msg-1", ActionID: "stop", Actor: "u1"})
	if err != nil {
		t.Fatalf("stop action error: %v", err)
	}
	prepared, err := result.PrepareCard(cfg.CardMaxChars)
	if err != nil {
		t.Fatalf("PrepareCard() error: %v", err)
	}
	config := prepared.PayloadCopy()["config"].(map[string]any)
	if config["streaming_mode"] != false {
		t.Fatalf("sync stop card streaming_mode = %#v, want false", config["streaming_mode"])
	}
	if _, hasStreamingConfig := config["streaming_config"]; hasStreamingConfig {
		t.Fatalf("sync stop card carried streaming_config: %#v", config)
	}
}

func TestMessageRecallCancelsActiveRun(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, runner, recorder)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new long", Time: now}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "msg-1", ChatID: "chat", RecallType: "user"}); err != nil {
		t.Fatalf("recall message error: %v", err)
	}
	waitForEvents(t, renderer, 2)
	last := renderer.Events()[len(renderer.Events())-1]
	if last.Type != "stopped" || !last.StopButton.Disabled || last.HeaderTemplate != "grey" {
		t.Fatalf("recall stopped event = %#v", last)
	}
	status := svc.statusText(agent.Claude, Message{ChatID: "chat"})
	if !containsAll(status, "state=idle", "queue=0") {
		t.Fatalf("status after recall = %q, want idle with empty queue", status)
	}
	foundAudit := false
	for _, event := range recorder.Events() {
		if event.Action == "message_recalled_active_cancelled" && event.Detail == "msg-1" {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %#v, want message_recalled_active_cancelled", recorder.Events())
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "/new second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := runner.Calls()[1].Prompt; got != "second" {
		t.Fatalf("second prompt = %q, want second", got)
	}
}

func TestMessageRecallCancelsPendingWorkdirConfirmation(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, runner, recorder)
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + missing + " hello", Time: time.Now()}); err != nil {
		t.Fatalf("handle message error: %v", err)
	}
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "msg-1", ChatID: "chat", RecallType: "user"}); err != nil {
		t.Fatalf("recall message error: %v", err)
	}
	events := renderer.Events()
	if len(events) != 2 {
		t.Fatalf("events = %#v, want confirm and cancelled", events)
	}
	last := events[len(events)-1]
	if last.Type != "workdir_cancelled" || last.SessionID != "claude:chat:message:msg-1" {
		t.Fatalf("last event = %#v, want workdir_cancelled", last)
	}
	if len(last.Actions) != 2 || !last.Actions[0].Disabled || !last.Actions[1].Disabled {
		t.Fatalf("cancelled actions = %#v, want disabled", last.Actions)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir stat = %v, want not exist", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.Calls())
	}
	foundAudit := false
	for _, event := range recorder.Events() {
		if event.Action == "message_recalled_pending_cancelled" && event.Detail == "msg-1" {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %#v, want message_recalled_pending_cancelled", recorder.Events())
	}
}

func TestMessageRecallRemovesQueuedInput(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	recorder := audit.NewRecorder()
	svc := NewService(cfg, renderer, runner, recorder)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "/new first", Time: now}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", ThreadID: "topic-a", Sender: "u1", Text: "second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "msg-2", ChatID: "chat", RecallType: "user"}); err != nil {
		t.Fatalf("recall message error: %v", err)
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic-a"})
	if len(runner.Calls()) != 1 {
		t.Fatalf("runner calls = %#v, want only first call", runner.Calls())
	}
	status := svc.statusText(agent.Claude, Message{ChatID: "chat", ThreadID: "topic-a"})
	if !containsAll(status, "state=idle", "queue=0") {
		t.Fatalf("status after queued recall = %q, want idle with empty queue", status)
	}
	foundAudit := false
	for _, event := range recorder.Events() {
		if event.Action == "message_recalled_queued_cancelled" && event.Detail == "msg-2" {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %#v, want message_recalled_queued_cancelled", recorder.Events())
	}
}

func TestQueuedRunPreservesInputWorkDir(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DefaultAgent: "claude", DefaultWorkDir: root, CardMaxChars: 1000, InteractionTimeout: time.Second}
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())

	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-1", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + first + " first", Time: now}); err != nil {
		t.Fatalf("first message error: %v", err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "msg-2", ChatID: "chat", Sender: "u1", Text: "/new --workdir " + second + " second", Time: time.Now()}); err != nil {
		t.Fatalf("second message error: %v", err)
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
	if err := svc.DrainReady(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	calls := runner.Calls()
	if calls[0].WorkDir != first {
		t.Fatalf("first workdir = %q, want %q", calls[0].WorkDir, first)
	}
	if calls[1].WorkDir != second {
		t.Fatalf("queued workdir = %q, want %q", calls[1].WorkDir, second)
	}
}

func TestServiceSkipsDuplicateRunAndOldDelivery(t *testing.T) {
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	msg := Message{ID: "dup", ChatID: "chat", Sender: "u", Text: "/new hello", Time: now}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "old", ChatID: "chat", Sender: "u", Text: "/new ignored", Time: svc.startedAt.Add(-3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("runner calls = %d, want one", got)
	}
}

func TestServiceRejectsTwentyFirstPendingInput(t *testing.T) {
	cfg := testConfig(t)
	cfg.QueueMaxPending = 20
	renderer := card.NewFakeRenderer()
	svc := NewService(cfg, renderer, newFakeRunner(), audit.NewRecorder())
	now := time.Now()
	for i := 0; i < 20; i++ {
		if err := svc.HandleMessage(context.Background(), Message{ID: fmt.Sprintf("m-%d", i), ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "input", Time: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "m-20", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "overflow", Time: now}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	last := events[len(events)-1]
	if last.Type != "message" || !containsAll(last.Segments[0].Text, "队列已满") {
		t.Fatalf("overflow event = %#v", last)
	}
}

func TestServiceShutdownRejectsNewRunsAndCancelsActiveBatch(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "run", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	shutdownCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Shutdown(shutdownCtx) }()
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before cancellation: %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v, want canceled", err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "new", ChatID: "chat", Sender: "u", Text: "/new rejected", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("runner calls = %d, want one", got)
	}
}

func TestServiceBackgroundReadyTickDrainsWithoutSleep(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "tick", ChatID: "chat", Sender: "u", Text: "/new tick", Time: now}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan time.Time, 1)
	svc.startBackgroundLoopsWithTicks(ctx, nil, ready)
	ready <- now.Add(time.Second)
	waitForCalls(t, runner, 1)
}

func TestServiceSweepsWithLivePathsBeforeResolvingAttachments(t *testing.T) {
	cfg := testConfig(t)
	recorder := audit.NewRecorder()
	svc := NewService(cfg, card.NewFakeRenderer(), newFakeRunner(), recorder)
	livePath := filepath.Join(t.TempDir(), "live.png")
	key := session.Key{Agent: agent.Claude, ChatID: "live-chat"}
	_, _, err := svc.Sessions.EnqueueDurable(key, session.Input{
		ID: "live", State: session.InputQueued,
		Attachments: []media.Attachment{{Path: livePath, MIME: "image/png", Size: 1}},
	}, cfg.DefaultWorkDir, session.BatchLimits{MaxPending: 10})
	if err != nil {
		t.Fatal(err)
	}
	order := []string{}
	sweeper := &mediaSweepStub{err: errors.New("scan failed"), order: &order}
	resolver := &orderedMediaResolver{order: &order, resolution: media.Resolution{Failures: []media.Failure{{Code: "download_failed"}}}}
	svc.MediaGC = sweeper
	configureTestMedia(svc, resolver)

	_, _, release := svc.resolveAttachments(context.Background(), []media.Ref{{MessageID: "m", FileKey: "f", Kind: "file"}})
	release()
	if got := strings.Join(order, ","); got != "sweep,resolve" {
		t.Fatalf("order = %q, want sweep,resolve", got)
	}
	calls := sweeper.callSnapshots()
	if len(calls) != 1 {
		t.Fatalf("sweep calls = %d, want 1", len(calls))
	}
	if _, ok := calls[0][livePath]; !ok {
		t.Fatalf("live snapshot = %#v, missing %q", calls[0], livePath)
	}
	if !auditContainsAction(recorder.Events(), "media_gc_failed") {
		t.Fatalf("audit = %#v, want media_gc_failed", recorder.Events())
	}
}

func TestServiceRetriesStaleMediaSweepWithFreshSnapshotAndBoundsAttempts(t *testing.T) {
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	sweeper := &mediaSweepStub{results: []media.SweepResult{{Stale: true}, {Stale: true}, {Stale: false}}}
	svc.MediaGC = sweeper
	svc.SweepMediaCache()
	if calls := len(sweeper.callSnapshots()); calls != 2 {
		t.Fatalf("sweep calls = %d, want bounded 2", calls)
	}
	if !auditContainsAction(svc.Audit.Events(), "media_gc_stale") {
		t.Fatalf("audit = %#v, want media_gc_stale", svc.Audit.Events())
	}
}

func TestServiceStartupAndBackgroundMediaSweeps(t *testing.T) {
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	sweeper := &mediaSweepStub{}
	svc.MediaGC = sweeper
	svc.SweepMediaCacheStartup()
	if calls := sweeper.startupCallCount(); calls != 1 {
		t.Fatalf("startup sweep calls = %d, want 1", calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	mediaTicks := make(chan time.Time, 1)
	svc.startBackgroundLoopsWithMediaTicks(ctx, nil, nil, mediaTicks)
	mediaTicks <- time.Now()
	deadline := time.Now().Add(time.Second)
	for len(sweeper.callSnapshots()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := len(sweeper.callSnapshots()); calls != 1 {
		t.Fatalf("background sweep calls = %d, want 1", calls)
	}
	cancel()
}

func auditContainsAction(events []audit.Event, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}

func TestServiceRecallDuringStartingCancelsBeforeRunnerSpawn(t *testing.T) {
	cfg := testConfig(t)
	fake := card.NewFakeRenderer()
	renderer := &blockingStreamRenderer{next: fake, entered: make(chan struct{}), release: make(chan struct{})}
	runner := newFakeRunner()
	svc := NewService(cfg, renderer, runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "starting", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- svc.DrainReady(now.Add(time.Second)) }()
	<-renderer.entered
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "starting", ChatID: "chat"}); err != nil {
		t.Fatal(err)
	}
	close(renderer.release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
	events := fake.Events()
	if last := events[len(events)-1]; last.Type != "stopped" || last.Streaming {
		t.Fatalf("early-cancel card = %#v, want stopped terminal", last)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
}

func TestServiceRecallBeforeInitialCardRendersStopped(t *testing.T) {
	cfg := testConfig(t)
	fake := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, fake, runner, audit.NewRecorder())
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.afterStoreActiveRunHook = func() { close(entered); <-release }
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "pre-card", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- svc.DrainReady(now.Add(time.Second)) }()
	<-entered
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "pre-card", ChatID: "chat"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
	events := fake.Events()
	if len(events) < 1 {
		t.Fatalf("events = %#v", events)
	}
	if last := events[len(events)-1]; last.Type != "stopped" || last.Streaming {
		t.Fatalf("early card = %#v, want stopped", last)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
}

func TestServiceShutdownWaitsForFrozenDispatchAndPreventsSpawn(t *testing.T) {
	cfg := testConfig(t)
	fake := card.NewFakeRenderer()
	runner := newFakeRunner()
	svc := NewService(cfg, fake, runner, audit.NewRecorder())
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.afterStoreActiveRunHook = func() { close(entered); <-release }
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "dispatch", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- svc.DrainReady(now.Add(time.Second)) }()
	<-entered
	shutdownCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- svc.Shutdown(shutdownCtx) }()
	waitForNotAccepting(t, svc)
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned while dispatch blocked: %v", err)
	default:
	}
	close(release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
}

func TestServiceShutdownDeadlineCancelsFrozenDispatchBeforeSpawn(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.afterStoreActiveRunHook = func() { close(entered); <-release }
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "deadline", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- svc.DrainReady(now.Add(time.Second)) }()
	<-entered
	shutdownCtx, cancel := context.WithCancel(context.Background())
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- svc.Shutdown(shutdownCtx) }()
	waitForNotAccepting(t, svc)
	cancel()
	close(release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v", err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat"})
}

func TestServiceRestoreDoesNotRunClearedQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := session.Key{Agent: agent.Claude, ChatID: "chat"}
	now := time.Now()
	seed := session.NewManagerWithStore(path)
	if _, _, err := seed.AcceptAndEnqueue(key, session.Input{ID: "pending", ReplyToMessageID: "pending", WorkDir: t.TempDir(), Time: now, DebounceUntil: now, State: session.InputQueued}, now, time.Hour, 10, session.BatchLimits{MaxPending: 20}); err != nil {
		t.Fatal(err)
	}
	restored := session.NewManagerWithStore(path)
	notices, err := restored.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatalf("notices = %#v", notices)
	}
	runner := newFakeRunner()
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), runner, audit.NewRecorder(), restored, notices)
	if err := svc.DrainReady(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
}

func TestNewServiceWithSessionsAuditsRecoveryNoticesWithoutRenderingCards(t *testing.T) {
	notices := []session.RecoveryNotice{
		{SessionID: "cancelled-session", ReplyToMessageID: "cancelled-reply", CardSessionID: "cancelled-card", Status: session.InputCancelled},
		{SessionID: "interrupted-session", ReplyToMessageID: "interrupted-reply", CardSessionID: "interrupted-card", Status: session.InputInterrupted, RenderRef: &session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 3}},
	}
	renderer := card.NewFakeRenderer()
	recorder := audit.NewRecorder()
	svc := NewServiceWithSessions(testConfig(t), renderer, newFakeRunner(), recorder, session.NewManager(), notices)

	events := recorder.Events()
	if len(events) != len(notices) {
		t.Fatalf("audit events = %#v, want one per notice", events)
	}
	for i, notice := range notices {
		if events[i].Actor != "system" || events[i].Action != "session_recovery_"+string(notice.Status) || events[i].SessionID != notice.SessionID {
			t.Fatalf("audit event %d = %#v for notice %#v", i, events[i], notice)
		}
		wantDetail := "reply=" + notice.ReplyToMessageID + " card_session=" + notice.CardSessionID
		if events[i].Detail != wantDetail {
			t.Fatalf("audit detail %d = %q, want %q", i, events[i].Detail, wantDetail)
		}
	}
	if got := renderer.Events(); len(got) != 0 {
		t.Fatalf("recovery rendered cards = %#v, want none", got)
	}

	notices[0].ReplyToMessageID = "mutated"
	notices[1].RenderRef.CardID = "mutated-card"
	if svc.RestoreNotices[0].ReplyToMessageID != "cancelled-reply" || svc.RestoreNotices[1].RenderRef == nil || svc.RestoreNotices[1].RenderRef.CardID != "old-card" {
		t.Fatalf("service restore notices leaked caller mutations: %#v", svc.RestoreNotices)
	}
}

func TestNewServiceWithSessionsKeepsRecoveryAuditWriteErrorsObservable(t *testing.T) {
	recorder := audit.NewRecorderWithWriter(failingAuditWriter{})
	renderer := card.NewFakeRenderer()
	svc := NewServiceWithSessions(testConfig(t), renderer, newFakeRunner(), recorder, session.NewManager(), []session.RecoveryNotice{{
		SessionID: "session", Status: session.InputInterrupted,
	}})

	if svc == nil {
		t.Fatal("service = nil")
	}
	if got := recorder.WriteErrors(); got != 1 {
		t.Fatalf("audit write errors = %d, want 1", got)
	}
	if got := renderer.Events(); len(got) != 0 {
		t.Fatalf("recovery rendered cards = %#v, want none", got)
	}
}

func TestServiceProcessesRestartCardRecoveryOnceWithoutReplacement(t *testing.T) {
	created := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	ref := &session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4, CreatedAt: created}
	notices := []session.RecoveryNotice{
		{SessionID: "claude:chat", ReplyToMessageID: "queued-source", Status: session.InputCancelled},
		{SessionID: "claude:chat", ReplyToMessageID: "running-source", Status: session.InputInterrupted, RenderRef: ref},
		{SessionID: "claude:chat", ReplyToMessageID: "batched-source", Status: session.InputInterrupted, RenderRef: ref},
	}
	runner := newFakeRunner()
	target := &bridgeReplyTarget{}
	recorder := audit.NewRecorder()
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), runner, recorder, session.NewManager(), notices)
	replies, err := reply.OpenStore(filepath.Join(t.TempDir(), "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := replies.SetLatest("claude:chat", ref); err != nil {
		t.Fatal(err)
	}
	svc.Replies = replies
	svc.CardTarget = target
	svc.ProcessRecoveryNotices(context.Background())
	svc.ProcessRecoveryNotices(context.Background())

	target.mu.Lock()
	newCalls, rehydrateCalls := target.newCalls, target.rehydrateCalls
	events := append([]card.Event(nil), target.events...)
	target.mu.Unlock()
	if newCalls != 0 || rehydrateCalls != 1 || len(events) != 1 {
		t.Fatalf("recovery target new/rehydrate/events = %d/%d/%#v", newCalls, rehydrateCalls, events)
	}
	if len(events[0].Segments) != 1 || events[0].Segments[0].Text != "服务重启，已中断，请重新发送" || events[0].Streaming || events[0].Type != "interrupted" || events[0].Actions != nil || events[0].StopButton != (card.StopButton{Visible: true, Disabled: true}) || events[0].Meta.Status != string(session.InputInterrupted) || !events[0].HideAgentPanels {
		t.Fatalf("recovery event = %#v", events[0])
	}
	if target.rehydratedRef.Version != 4 || !target.rehydratedRef.CreatedAt.Equal(created) {
		t.Fatalf("rehydrated ref = %#v", target.rehydratedRef)
	}
	if saved := replies.GetLatest("claude:chat"); saved == nil || saved.Version != 5 || !saved.CreatedAt.Equal(created) {
		t.Fatalf("saved latest = %#v", saved)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("recovery runner calls = %#v", runner.Calls())
	}
	if auditContainsAction(recorder.Events(), "recovery_card_update_failed") {
		t.Fatalf("unexpected recovery failure audit = %#v", recorder.Events())
	}
}

func TestServiceRecoverySkipsSequenceUnknownCardOnce(t *testing.T) {
	ref := &session.RenderRef{CardID: "unknown-card", ReplyMessageID: "old-reply", Version: 4, SequenceUnknown: true, PendingSequence: 9}
	notices := []session.RecoveryNotice{
		{SessionID: "claude:chat", ReplyToMessageID: "source", Status: session.InputInterrupted, RenderRef: ref},
		{SessionID: "claude:chat", ReplyToMessageID: "source-duplicate", Status: session.InputInterrupted, RenderRef: ref},
	}
	target := &bridgeReplyTarget{}
	recorder := audit.NewRecorder()
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), recorder, session.NewManager(), notices)
	replies, err := reply.OpenStore(filepath.Join(t.TempDir(), "replies.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := replies.SetLatest("claude:chat", ref); err != nil {
		t.Fatal(err)
	}
	svc.Replies = replies
	svc.CardTarget = target
	svc.ProcessRecoveryNotices(context.Background())
	svc.ProcessRecoveryNotices(context.Background())
	target.mu.Lock()
	newCalls, rehydrateCalls, events := target.newCalls, target.rehydrateCalls, append([]card.Event(nil), target.events...)
	target.mu.Unlock()
	if newCalls != 0 || rehydrateCalls != 0 || len(events) != 0 {
		t.Fatalf("unknown recovery new/rehydrate/events = %d/%d/%#v", newCalls, rehydrateCalls, events)
	}
	if got := replies.GetLatest("claude:chat"); got == nil || *got != *ref {
		t.Fatalf("unknown latest = %#v, want %#v", got, ref)
	}
	var skipped []audit.Event
	for _, event := range recorder.Events() {
		if event.Action == "recovery_card_update_skipped_sequence_unknown" {
			skipped = append(skipped, event)
		}
	}
	if len(skipped) != 1 || skipped[0].Detail != "pending_sequence=9" {
		t.Fatalf("sequence unknown audits = %#v", skipped)
	}
}

type bridgeAlwaysUnknownResolver struct{}

func (bridgeAlwaysUnknownResolver) RenderRefSequenceUnknown(session.RenderRef) bool { return true }

func TestServiceRecoverySkipsResolverUnknownCard(t *testing.T) {
	ref := &session.RenderRef{CardID: "unknown-card", ReplyMessageID: "old-reply", Version: 4}
	target := &bridgeReplyTarget{}
	recorder := audit.NewRecorder()
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), recorder, session.NewManager(), []session.RecoveryNotice{{
		SessionID: "claude:chat", ReplyToMessageID: "source", Status: session.InputInterrupted, RenderRef: ref,
	}})
	svc.CardTarget = target
	svc.SequenceResolver = bridgeAlwaysUnknownResolver{}
	svc.ProcessRecoveryNotices(context.Background())
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.newCalls != 0 || target.rehydrateCalls != 0 || len(target.events) != 0 {
		t.Fatalf("resolver unknown recovery wrote card: %#v", target)
	}
	if !auditContainsAction(recorder.Events(), "recovery_card_update_skipped_sequence_unknown") {
		t.Fatalf("missing unknown audit: %#v", recorder.Events())
	}
}

func TestServiceRecoveryFailureDoesNotCreateOrMutateLatest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target *bridgeReplyTarget
	}{
		{name: "stale renderer", target: &bridgeReplyTarget{renderErr: feishu.ErrStaleRenderRef}},
		{name: "no card target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := &session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4}
			recorder := audit.NewRecorder()
			svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), recorder, session.NewManager(), []session.RecoveryNotice{{
				SessionID: "claude:chat", ReplyToMessageID: "source", Status: session.InputInterrupted, RenderRef: ref,
			}})
			replies, err := reply.OpenStore(filepath.Join(t.TempDir(), "replies.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := replies.SetLatest("claude:chat", ref); err != nil {
				t.Fatal(err)
			}
			svc.Replies = replies
			if tc.target != nil {
				svc.CardTarget = tc.target
			}
			svc.ProcessRecoveryNotices(context.Background())
			if tc.target != nil {
				tc.target.mu.Lock()
				newCalls := tc.target.newCalls
				tc.target.mu.Unlock()
				if newCalls != 0 {
					t.Fatalf("new calls = %d, want 0", newCalls)
				}
			}
			if got := replies.GetLatest("claude:chat"); got == nil || *got != *ref {
				t.Fatalf("latest = %#v, want %#v", got, ref)
			}
			if !auditContainsAction(recorder.Events(), "recovery_card_update_failed") {
				t.Fatalf("audit = %#v", recorder.Events())
			}
		})
	}
}

func TestServiceRecoveryCardUpdateHonorsCallerDeadline(t *testing.T) {
	ref := session.RenderRef{CardID: "old-card", ReplyMessageID: "old-reply", Version: 4}
	renderer := &contextBlockingRecoveryRenderer{ref: ref}
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder(), session.NewManager(), []session.RecoveryNotice{{
		SessionID: "claude:chat", ReplyToMessageID: "source", Status: session.InputInterrupted, RenderRef: &ref,
	}})
	svc.CardTarget = &contextBlockingRecoveryTarget{renderer: renderer}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	svc.ProcessRecoveryNotices(ctx)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("recovery elapsed = %s, want bounded by caller deadline", elapsed)
	}
	renderer.mu.Lock()
	legacyCalled, contextCalled := renderer.legacyCalled, renderer.contextCalled
	renderer.mu.Unlock()
	if legacyCalled || !contextCalled {
		t.Fatalf("legacy/context render calls = %t/%t, want false/true", legacyCalled, contextCalled)
	}
}

func TestServiceCompletionPersistFailureStillRendersResultAndAudits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	manager := session.NewManagerWithStore(path)
	cfg := testConfig(t)
	renderer := card.NewFakeRenderer()
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	runner.results = []AgentRunResult{{Segments: []card.Segment{{Kind: card.SegmentText, Text: "captured result"}}}}
	recorder := audit.NewRecorder()
	svc := NewServiceWithSessions(cfg, renderer, runner, recorder, manager, nil)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "persist", ChatID: "chat", Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(runner.block)
	waitForEvents(t, renderer, 2)
	last := renderer.Events()[len(renderer.Events())-1]
	if last.Type != "result" || !containsAll(last.Segments[0].Text, "captured result") {
		t.Fatalf("terminal card = %#v", last)
	}
	for _, event := range recorder.Events() {
		if event.Action == "completion_persist_failed" {
			return
		}
	}
	t.Fatalf("audit events = %#v, want completion_persist_failed", recorder.Events())
}

func TestServiceRendersTerminalBeforeReleasingActiveBatch(t *testing.T) {
	manager := session.NewManagerWithStore(filepath.Join(t.TempDir(), "sessions.json"))
	key := session.Key{Agent: agent.Claude, ChatID: "chat"}
	renderer := &terminalBatchOrderRenderer{next: card.NewFakeRenderer(), sessions: manager, key: key}
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewServiceWithSessions(testConfig(t), renderer, runner, audit.NewRecorder(), manager, nil)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "order", ChatID: key.ChatID, Sender: "u", Text: "/new work", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, key)
	renderer.mu.Lock()
	activeSeen := renderer.activeSeen
	renderer.mu.Unlock()
	if !activeSeen {
		t.Fatal("terminal card rendered after active batch was released")
	}
}

func TestServiceRetriesPersistedCompletionBeforeStartingLaterQueue(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions.json")
	manager := session.NewManagerWithStore(path)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	recorder := audit.NewRecorder()
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), runner, recorder, manager, nil)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "later", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "later", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(runner.block)
	waitForEvents(t, svc.Cards.(*card.LimitRenderer).Next.(*card.FakeRenderer), 2)
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("runner calls before repair = %d, want 1", got)
	}
	if got := len(svc.pendingCompletions); got != 1 {
		t.Fatalf("pending completions = %d, want 1", got)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := len(svc.pendingCompletions); got != 0 {
		t.Fatalf("pending completions after retry = %d", got)
	}
}

func TestServiceBacksOffPendingCompletionRetries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions.json")
	manager := session.NewManagerWithStore(path)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewServiceWithSessions(testConfig(t), card.NewFakeRenderer(), runner, audit.NewRecorder(), manager, nil)
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(runner.block)
	waitForEvents(t, svc.Cards.(*card.LimitRenderer).Next.(*card.FakeRenderer), 2)
	svc.mu.Lock()
	var pending pendingCompletion
	for _, entry := range svc.pendingCompletions {
		pending = entry
	}
	svc.mu.Unlock()
	if pending.Attempts != 0 {
		t.Fatalf("initial attempts = %d", pending.Attempts)
	}
	attempts := 0
	svc.beforePendingCompletionRetryHook = func() { attempts++ }
	if err := svc.DrainReady(pending.NextRetryAt.Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("retry before due attempts = %d", attempts)
	}
	if err := svc.DrainReady(pending.NextRetryAt); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("first due retry attempts = %d", attempts)
	}
	svc.mu.Lock()
	pending = svc.pendingCompletions[completionKey(pending.Key, pending.BatchID)]
	svc.mu.Unlock()
	if pending.Attempts != 1 {
		t.Fatalf("attempts after failure = %d", pending.Attempts)
	}
	if err := svc.DrainReady(pending.NextRetryAt.Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("retry before second due attempts = %d", attempts)
	}
	if err := svc.DrainReady(pending.NextRetryAt); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("second due retry attempts = %d", attempts)
	}
}

func TestServiceMergesBusyTopicInputsIntoNextBatch(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	first := Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}
	if err := svc.HandleMessage(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	for _, msg := range []Message{{ID: "two", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "two", Time: now}, {ID: "three", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "three", Time: now}} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	close(runner.block)
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"})
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if prompt := runner.Calls()[1].Prompt; !containsAll(prompt, "two", "three") {
		t.Fatalf("merged prompt = %q", prompt)
	}
}

func TestServiceDeduplicatesCommandAndAcceptsOldBoundary(t *testing.T) {
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	msg := Message{ID: "help", ChatID: "chat", Sender: "u", Text: "/help", Time: time.Now()}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleMessage(context.Background(), Message{ID: "boundary", ChatID: "chat", Sender: "u", Text: "/help", Time: svc.startedAt.Add(-2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	// Two accepted help cards: one receipt duplicate is silent and the -2s boundary is not old.
	if got := len(svc.Cards.(*card.LimitRenderer).Next.(*card.FakeRenderer).Events()); got != 2 {
		t.Fatalf("help cards = %d, want 2", got)
	}
}

func TestServiceRunningRecallCancelsMergedBatch(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	for _, msg := range []Message{{ID: "one", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "one", Time: now}, {ID: "two", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "two", Time: now}} {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessageRecalled(context.Background(), MessageRecall{MessageID: "one", ChatID: "chat"}); err != nil {
		t.Fatal(err)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"})
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("runner calls = %d, want one cancelled batch", got)
	}
}

func TestServiceStopKeepsLaterQueue(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "later", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "later", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleAction(context.Background(), ActionRequest{SessionID: "claude:chat:thread:topic:message:first", ActionID: "stop", Actor: "u"}); err != nil {
		t.Fatal(err)
	}
	waitForSessionNoActiveBatch(t, svc, session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"})
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := runner.Calls()[1].Prompt; got != "later" {
		t.Fatalf("later prompt = %q", got)
	}
}

func TestServiceNewPromptIsBatchBoundaryAndResetsNextContext(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.results = []AgentRunResult{{AgentSessionID: "old"}, {AgentSessionID: "new"}, {AgentSessionID: "new"}}
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	inputs := []Message{{ID: "before", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "before", Time: now}, {ID: "new", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "/new reset", Time: now}, {ID: "after", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "after", Time: now}}
	for _, msg := range inputs {
		if err := svc.HandleMessage(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	key := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 1)
	waitForSessionNoActiveBatch(t, svc, key)
	if runner.Calls()[0].Prompt != "before" {
		t.Fatalf("first prompt = %q", runner.Calls()[0].Prompt)
	}
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	waitForSessionNoActiveBatch(t, svc, key)
	if call := runner.Calls()[1]; call.Prompt != "reset" || call.AgentSessionID != "" {
		t.Fatalf("reset call = %#v", call)
	}
	if err := svc.DrainReady(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 3)
	if call := runner.Calls()[2]; call.Prompt != "after" || call.AgentSessionID != "new" {
		t.Fatalf("after call = %#v", call)
	}
}

func TestServiceRunnerErrorSchedulesLaterQueue(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeRunner()
	runner.block = make(chan struct{})
	runner.errs = []error{errors.New("runner failed")}
	svc := NewService(cfg, card.NewFakeRenderer(), runner, audit.NewRecorder())
	now := time.Now()
	if err := svc.HandleMessage(context.Background(), Message{ID: "first", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "first", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DrainReady(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := svc.HandleMessage(context.Background(), Message{ID: "next", ChatID: "chat", ThreadID: "topic", Sender: "u", Text: "next", Time: now}); err != nil {
		t.Fatal(err)
	}
	close(runner.block)
	key := session.Key{Agent: agent.Claude, ChatID: "chat", Thread: "topic"}
	waitForSessionNoActiveBatch(t, svc, key)
	if err := svc.DrainReady(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, runner, 2)
	if got := runner.Calls()[1].Prompt; got != "next" {
		t.Fatalf("next prompt = %q", got)
	}
}

func TestStreamUpdateParsesToolUseBlockIntoToolSegment(t *testing.T) {
	var got []AgentStreamUpdate
	data := []byte(`{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","id":"tu-1","input":{"command":"ls"}}}`)
	_, err := parseClaudeStream(bytes.NewReader(data), nil, func(update AgentStreamUpdate) {
		got = append(got, update)
	})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	if len(got) != 1 || len(got[0].Segments) != 1 || !got[0].PartialMessage {
		t.Fatalf("updates = %#v", got)
	}
	seg := got[0].Segments[0]
	if seg.Kind != card.SegmentTool {
		t.Fatalf("segment kind = %v, want tool", seg.Kind)
	}
	// The rendered tool segment must carry the tool name, id, and JSON input.
	for _, want := range []string{"Bash", "tu-1", `"command":"ls"`} {
		if !strings.Contains(seg.Text, want) {
			t.Fatalf("tool_use segment %q missing %q", seg.Text, want)
		}
	}
}

func TestStreamUpdateParsesToolResultBlockIntoToolSegment(t *testing.T) {
	var got []AgentStreamUpdate
	data := []byte(`{"type":"content_block_start","content_block":{"type":"tool_result","tool_use_id":"tu-1","content":"exit 0"}}`)
	_, err := parseClaudeStream(bytes.NewReader(data), nil, func(update AgentStreamUpdate) {
		got = append(got, update)
	})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	if len(got) != 1 || len(got[0].Segments) != 1 {
		t.Fatalf("updates = %#v", got)
	}
	seg := got[0].Segments[0]
	if seg.Kind != card.SegmentTool {
		t.Fatalf("segment kind = %v, want tool", seg.Kind)
	}
	for _, want := range []string{"tool_result", "tu-1", "exit 0"} {
		if !strings.Contains(seg.Text, want) {
			t.Fatalf("tool_result segment %q missing %q", seg.Text, want)
		}
	}
}

func TestToolSegmentsAvoidMarkdownAdhesion(t *testing.T) {
	// 回归:工具区 markdown 不能出现代码围栏与列表项粘连
	// (曾出现 "- Bash `id````json ... ```- tool_result" 这类无空行的粘连)。
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"tu-1","input":{"command":"echo hi"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"hi"}]}}`,
	}
	data := []byte(strings.Join(lines, "\n"))
	result, err := parseClaudeStream(bytes.NewReader(data), nil, func(AgentStreamUpdate) {})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	var toolText string
	for _, seg := range result.Segments {
		if seg.Kind == card.SegmentTool {
			toolText = seg.Text
		}
	}
	if toolText == "" {
		t.Fatalf("no tool segment: %#v", result.Segments)
	}
	// 代码围栏前必须有空行(即 "```" 不能紧跟在非空行后)。
	if strings.Contains(toolText, "````") || strings.Contains(toolText, "e```") {
		t.Fatalf("code fence adheres to preceding text: %q", toolText)
	}
	// tool_use 块与 tool_result 块之间必须以空行分隔。
	if !strings.Contains(toolText, "```\n\n- tool_result") {
		t.Fatalf("tool_use and tool_result not separated by blank line: %q", toolText)
	}
	// 代码围栏起始前应为空行。
	if !strings.Contains(toolText, "`\n\n```json") {
		t.Fatalf("json fence not preceded by blank line: %q", toolText)
	}
}

func TestParseClaudeStreamSegmentsAssistantAnswersAndCountsUniqueTools(t *testing.T) {
	// 两个 assistant message:第一段是过程性发言,第二段是最终结论。
	// 一次工具调用被拆成 tool_use + tool_result 两条,同一 tool_use.id 只应计一次。
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"让我先看看"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"tu-1","input":{"command":"ls"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"file.txt"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"最终结论:只有一个文件。"}]}}`,
	}
	data := []byte(strings.Join(lines, "\n"))
	result, err := parseClaudeStream(bytes.NewReader(data), nil, func(AgentStreamUpdate) {})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	if len(result.AnswerSegments) != 2 {
		t.Fatalf("answer segments = %#v, want 2 assistant answers", result.AnswerSegments)
	}
	if last := result.AnswerSegments[len(result.AnswerSegments)-1]; !strings.Contains(last, "最终结论") {
		t.Fatalf("last answer segment = %q", last)
	}
	if result.ToolCallCount != 1 {
		t.Fatalf("tool call count = %d, want 1 (deduped by tool_use.id)", result.ToolCallCount)
	}
}

func TestParseClaudeStreamPreservesOrderedTimeline(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"先检查"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"tu-1","input":{"command":"ls"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"file.txt"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"最终答案"}]}}`,
	}
	result, err := parseClaudeStream(bytes.NewReader([]byte(strings.Join(lines, "\n"))), nil, func(AgentStreamUpdate) {})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	wantKinds := []card.SegmentKind{
		card.SegmentText,
		card.SegmentTool,
		card.SegmentTool,
		card.SegmentText,
	}
	if len(result.OrderedSegments) != len(wantKinds) {
		t.Fatalf("ordered segments = %#v", result.OrderedSegments)
	}
	for i, kind := range wantKinds {
		if result.OrderedSegments[i].Kind != kind {
			t.Fatalf("ordered segment %d = %#v, want %q", i, result.OrderedSegments[i], kind)
		}
	}
	if !strings.Contains(result.OrderedSegments[1].Text, "Bash") || !strings.Contains(result.OrderedSegments[2].Text, "tool_result") {
		t.Fatalf("ordered tool segments = %#v", result.OrderedSegments[1:3])
	}
}

func TestTerminalAnswerFollowsReplyMode(t *testing.T) {
	for _, tc := range []struct {
		mode config.ReplyMode
		want string
	}{
		{mode: config.ReplyModeAppend, want: "让我先看看\n\n最终结论:只有一个文件。"},
		{mode: config.ReplyModeAppendCleanCard, want: "最终结论:只有一个文件。"},
		{mode: config.ReplyModeLatestCard, want: "最终结论:只有一个文件。"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			renderer := card.NewFakeRenderer()
			svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
			input := session.Input{ReplyToMessageID: "src", ReplyMode: tc.mode, Time: time.Now()}
			stream := newAgentCardStreamWithRenderer(svc, "s1", session.Session{ID: "s1"}, input, renderer, nil)
			if err := stream.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			_, err := stream.Finish("completed", card.Meta{}, AgentRunResult{
				Segments:       []card.Segment{{Kind: card.SegmentText, Text: "让我先看看\n\n最终结论:只有一个文件。"}},
				AnswerSegments: []string{"让我先看看", "最终结论:只有一个文件。"},
			})
			if err != nil {
				t.Fatalf("finish: %v", err)
			}
			events := renderer.Events()
			terminal := events[len(events)-1]
			if len(terminal.Segments) == 0 || terminal.Segments[0].Kind != card.SegmentText || terminal.Segments[0].Text != tc.want {
				t.Fatalf("terminal segments = %#v, want %q", terminal.Segments, tc.want)
			}
		})
	}
}

func TestStreamUpdateParsesContentBlockLevelThinking(t *testing.T) {
	var got []AgentStreamUpdate
	// content_block_start with a redacted_thinking block must map to a thought
	// segment, covering the block-level branch (not just thinking_delta).
	data := []byte(`{"type":"content_block_start","content_block":{"type":"redacted_thinking","thinking":"internal reasoning"}}`)
	_, err := parseClaudeStream(bytes.NewReader(data), nil, func(update AgentStreamUpdate) {
		got = append(got, update)
	})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	if len(got) != 1 || len(got[0].Segments) != 1 {
		t.Fatalf("updates = %#v", got)
	}
	seg := got[0].Segments[0]
	if seg.Kind != card.SegmentThought || seg.Text != "internal reasoning" {
		t.Fatalf("thinking block segment = %#v, want thought internal reasoning", seg)
	}
}

func TestParseClaudeStreamOutputDoesNotDuplicateFinalResult(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"type":"assistant","message":{"model":"claude-opus","content":[{"type":"text","text":"E2E_ONESHOT"}],"usage":{"output_tokens":3},"session_id":"sess-1"}}`,
		`{"type":"result","result":"E2E_ONESHOT","usage":{"input_tokens":2}}`,
	}, "\n"))
	result := ParseClaudeStreamOutput(data)
	if result.AgentSessionID != "sess-1" || result.Model != "claude-opus" || result.Tokens != 5 {
		t.Fatalf("metadata = %#v", result)
	}
	if len(result.Segments) != 1 || result.Segments[0].Text != "E2E_ONESHOT" {
		t.Fatalf("segments = %#v, want single E2E_ONESHOT", result.Segments)
	}
}

func TestCLIExecRunnerUsesRequestedWorkDirAndPWD(t *testing.T) {
	fakeBin := t.TempDir()
	fakeClaude := filepath.Join(fakeBin, "claude")
	script := `#!/bin/sh
printf '{"type":"assistant","message":{"model":"fake-claude","content":[{"type":"text","text":"pwd=%s envpwd=%s"}],"usage":{"output_tokens":1},"session_id":"sess-1"}}\n' "$(pwd)" "$PWD"
`
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	workDir := t.TempDir()
	result, err := CLIExecRunner{}.Run(context.Background(), AgentRunRequest{
		Kind:    agent.Claude,
		Prompt:  "check cwd",
		WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("runner error: %v", err)
	}
	if len(result.Segments) != 1 {
		t.Fatalf("segments = %#v, want one answer", result.Segments)
	}
	got := result.Segments[0].Text
	if !containsAll(got, "pwd="+workDir, "envpwd="+workDir) {
		t.Fatalf("runner cwd output = %q, want pwd and envpwd %s", got, workDir)
	}
}

func TestCLIExecRunnerRunsCodexWithStdinImagesAndConfiguredWorkDir(t *testing.T) {
	binDir := t.TempDir()
	fakeCodex := filepath.Join(binDir, "cx3")
	logPath := filepath.Join(t.TempDir(), "codex.log")
	script := `#!/bin/sh
printf 'args=%s\npwd=%s\n' "$*" "$PWD" >"$FAKE_CODEX_LOG"
IFS= read -r prompt || true
printf 'stdin=%s\nhome=%s\n' "$prompt" "${CODEX_HOME:-}" >>"$FAKE_CODEX_LOG"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s\n' '{"type":"item.completed","item":{"id":"msg-1","type":"agent_message","text":"codex answer"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":3}}'
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CODEX_LOG", logPath)
	workDir := t.TempDir()
	result, err := CLIExecRunner{}.Run(context.Background(), AgentRunRequest{
		Kind:           agent.Codex,
		Bin:            fakeCodex,
		Prompt:         "inspect repo",
		WorkDir:        workDir,
		Home:           "/isolated/codex-home",
		Images:         []string{"/cache/a.png", "/cache/b.jpg"},
		AgentSessionID: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentSessionID != "thread-1" || result.Tokens != 5 || len(result.Segments) != 1 || result.Segments[0].Text != "codex answer" {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !containsAll(log, "args=exec --json --image /cache/a.png --image /cache/b.jpg -- -", "pwd="+workDir, "stdin=inspect repo", "home=/isolated/codex-home") {
		t.Fatalf("fake codex log = %q", log)
	}
	for _, forbidden := range []string{"--sandbox", "approval_policy", "--model", "--profile", "--ignore-rules", "--skip-git-repo-check"} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("unexpected %q in log %q", forbidden, log)
		}
	}
}

func TestStreamUpdateParsesClaudeDeltaThinking(t *testing.T) {
	var got []AgentStreamUpdate
	data := []byte(strings.Join([]string{
		`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hidden plan"}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"visible answer"}}`,
	}, "\n"))
	_ = ParseClaudeStreamOutput(data)
	_, err := parseClaudeStream(bytes.NewReader(data), nil, func(update AgentStreamUpdate) {
		got = append(got, update)
	})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("updates = %#v", got)
	}
	if got[0].Segments[0].Kind != card.SegmentThought || got[0].Segments[0].Text != "hidden plan" {
		t.Fatalf("thinking update = %#v", got[0])
	}
	if got[1].Segments[0].Kind != card.SegmentText || got[1].Segments[0].Text != "visible answer" {
		t.Fatalf("text update = %#v", got[1])
	}
}

func TestStreamUpdateUnwrapsClaudePartialStreamEvents(t *testing.T) {
	var got []AgentStreamUpdate
	data := []byte(strings.Join([]string{
		`{"type":"stream_event","session_id":"sess-partial","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hidden partial"}}}`,
		`{"type":"stream_event","session_id":"sess-partial","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"visible partial"}}}`,
	}, "\n"))
	_, err := parseClaudeStream(bytes.NewReader(data), nil, func(update AgentStreamUpdate) {
		got = append(got, update)
	})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("updates = %#v, want two unwrapped partial updates", got)
	}
	if got[0].AgentSessionID != "sess-partial" || !got[0].PartialMessage || got[0].Segments[0].Kind != card.SegmentThought || got[0].Segments[0].Text != "hidden partial" {
		t.Fatalf("thinking update = %#v", got[0])
	}
	if got[1].AgentSessionID != "sess-partial" || !got[1].PartialMessage || got[1].Segments[0].Kind != card.SegmentText || got[1].Segments[0].Text != "visible partial" {
		t.Fatalf("text update = %#v", got[1])
	}
}

func TestStreamUpdatePreservesDeltaWhitespace(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}}`,
		"{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\\n\\n```go\\n\"}}}",
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"fmt.Println(\"ok\")"}}}`,
		"{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\\n```\"}}}",
	}, "\n"))
	var streamed strings.Builder
	_, err := parseClaudeStream(bytes.NewReader(data), nil, func(update AgentStreamUpdate) {
		if len(update.Segments) > 0 && !update.Incremental {
			t.Fatalf("delta update = %#v, want incremental", update)
		}
		for _, segment := range update.Segments {
			if segment.Kind == card.SegmentText {
				streamed.WriteString(segment.Text)
			}
		}
	})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	want := "Hello world\n\n```go\nfmt.Println(\"ok\")\n```"
	if got := streamed.String(); got != want {
		t.Fatalf("streamed text = %q, want %q", got, want)
	}
}

func TestStreamUpdateMarksFullAssistantMessageAsAnswerSnapshot(t *testing.T) {
	data := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hello world"}]}}`)
	var got []AgentStreamUpdate
	_, err := parseClaudeStream(bytes.NewReader(data), nil, func(update AgentStreamUpdate) {
		got = append(got, update)
	})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	if len(got) != 1 || !got[0].AnswerSnapshot || got[0].Incremental {
		t.Fatalf("updates = %#v, want one non-incremental answer snapshot", got)
	}
}

func TestStreamUpdateMarksToolOnlyAssistantMessageAsSnapshot(t *testing.T) {
	data := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"tu-1","input":{"command":"ls"}}]}}`)
	var got []AgentStreamUpdate
	_, err := parseClaudeStream(bytes.NewReader(data), nil, func(update AgentStreamUpdate) {
		got = append(got, update)
	})
	if err != nil {
		t.Fatalf("parse stream error: %v", err)
	}
	if len(got) != 1 || !got[0].AssistantSnapshot || got[0].AnswerSnapshot {
		t.Fatalf("tool-only assistant update = %#v, want assistant boundary without answer snapshot", got)
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{DefaultAgent: "claude", DefaultWorkDir: t.TempDir(), CardMaxChars: 1000, InteractionTimeout: time.Second, ConversationMode: config.ConversationModeTopic}
}

func testPreferenceStore(t *testing.T, defaults config.RuntimePreference, allowedModels []string) (*config.PreferenceStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "preferences.json")
	store, err := config.OpenPreferenceStore(path, defaults, allowedModels)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func waitForEvents(t *testing.T, renderer *card.FakeRenderer, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(renderer.Events()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("events len = %d, want >= %d", len(renderer.Events()), n)
}

func waitForCalls(t *testing.T, runner *fakeRunner, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(runner.Calls()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("calls len = %d, want >= %d", len(runner.Calls()), n)
}

func waitForReactionCounts(t *testing.T, sink *fakeReactionSink, adds, deletes int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		gotAdds, gotDeletes := sink.snapshot()
		if len(gotAdds) >= adds && len(gotDeletes) >= deletes {
			return
		}
		time.Sleep(time.Millisecond)
	}
	gotAdds, gotDeletes := sink.snapshot()
	t.Fatalf("reaction add/delete counts = %d/%d, want >= %d/%d", len(gotAdds), len(gotDeletes), adds, deletes)
}

func waitForAuditAction(t *testing.T, recorder *audit.Recorder, action string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if auditContainsAction(recorder.Events(), action) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("audit events = %#v, want action %q", recorder.Events(), action)
}

func waitForSessionNoActiveBatch(t *testing.T, svc *Service, key session.Key) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if sess, ok := svc.Sessions.Get(key); ok && sess.ActiveBatch == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session %s still has an active batch", key.ID())
}

func waitForNotAccepting(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !svc.isAccepting() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("service remained accepting after Shutdown started")
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
