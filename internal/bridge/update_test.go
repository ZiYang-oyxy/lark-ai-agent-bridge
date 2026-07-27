package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lark-agent-bridge/internal/access"
	"lark-agent-bridge/internal/agent"
	"lark-agent-bridge/internal/audit"
	"lark-agent-bridge/internal/buildinfo"
	"lark-agent-bridge/internal/card"
	"lark-agent-bridge/internal/config"
	"lark-agent-bridge/internal/devmode"
	"lark-agent-bridge/internal/schedule"
	"lark-agent-bridge/internal/session"
	bridgeupdate "lark-agent-bridge/internal/update"
)

type fakeUpdateManager struct {
	checkResult    bridgeupdate.CheckResult
	checkErr       error
	notes          string
	notesErr       error
	prepared       PreparedUpdate
	prepareErr     error
	prepareCall    int
	peekManifest   bridgeupdate.Manifest
	peekAvailable  bool
	refreshOnce    sync.Once
	refreshStarted chan struct{}
	refreshRelease chan struct{}
	refreshChannel []bool
}

func (f *fakeUpdateManager) Check(context.Context, string) (bridgeupdate.CheckResult, error) {
	return f.checkResult, f.checkErr
}
func (f *fakeUpdateManager) Refresh(context.Context, string) (bridgeupdate.CheckResult, error) {
	if f.refreshStarted != nil {
		f.refreshOnce.Do(func() { close(f.refreshStarted) })
	}
	if f.refreshRelease != nil {
		<-f.refreshRelease
	}
	return f.checkResult, f.checkErr
}
func (f *fakeUpdateManager) CheckChannel(context.Context, string, bool) (bridgeupdate.CheckResult, error) {
	return f.checkResult, f.checkErr
}
func (f *fakeUpdateManager) RefreshChannel(ctx context.Context, version string, prerelease bool) (bridgeupdate.CheckResult, error) {
	f.refreshChannel = append(f.refreshChannel, prerelease)
	return f.Refresh(ctx, version)
}
func (f *fakeUpdateManager) ReleaseNotes(context.Context, bridgeupdate.Manifest) (string, error) {
	return f.notes, f.notesErr
}
func (f *fakeUpdateManager) AggregatedReleaseNotes(_ context.Context, _ string, _ bridgeupdate.Manifest, _ func(string, error)) (string, error) {
	return f.notes, f.notesErr
}
func (f *fakeUpdateManager) Prepare(context.Context, bridgeupdate.Asset) (PreparedUpdate, error) {
	f.prepareCall++
	return f.prepared, f.prepareErr
}
func (f *fakeUpdateManager) LatestManifest() (bridgeupdate.Manifest, bool) {
	return f.peekManifest, f.peekAvailable
}

type fakePreparedUpdate struct {
	mu             sync.Mutex
	replaced       int
	restarted      chan struct{}
	restartRelease chan struct{}
	restartEnv     []string
	aborted        int
}

func (f *fakePreparedUpdate) Replace() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaced++
	return nil
}
func (f *fakePreparedUpdate) Restart(_ []string, env []string) error {
	f.mu.Lock()
	f.restartEnv = append([]string(nil), env...)
	f.mu.Unlock()
	if f.restarted != nil {
		close(f.restarted)
	}
	if f.restartRelease != nil {
		<-f.restartRelease
	}
	return nil
}
func (f *fakePreparedUpdate) Abort() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborted++
}

func updateAvailableResult() bridgeupdate.CheckResult {
	asset := bridgeupdate.Asset{URL: "https://updates.example/binary", SHA256: strings.Repeat("a", 64), Size: 3}
	return bridgeupdate.CheckResult{
		Manifest: bridgeupdate.Manifest{Version: "1.2.0", ReleaseNotesURL: "https://updates.example/notes"},
		Asset:    asset, UpdateAvailable: true,
	}
}

func TestHelpShowsCurrentVersionAndUpdateDetailsAction(t *testing.T) {
	setUpdateTestVersion(t)
	renderer := card.NewFakeRenderer()
	svc := NewService(updateTestConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Updates = &fakeUpdateManager{checkResult: updateAvailableResult()}
	if err := svc.HandleMessage(t.Context(), Message{ID: "help", ChatID: "dm", Sender: "user", Text: "/help"}); err != nil {
		t.Fatal(err)
	}
	event := renderer.Events()[0]
	if event.Type != "help" || event.HelpCard == nil {
		t.Fatalf("help must keep the sectioned command card: %#v", event)
	}
	status := event.HelpCard.VersionStatus
	if status == nil {
		t.Fatal("help must include structured version status")
	}
	if status.CurrentVersion != "v1.0.0" || status.LatestVersion != "v1.2.0" || !status.UpdateAvailable {
		t.Fatalf("help version status = %#v", status)
	}
	if status.DetailsAction.ID != "update.details" || status.DetailsAction.Label != "查看更新 →" || status.DetailsAction.Value != "1.2.0" {
		t.Fatalf("help details action = %#v", status.DetailsAction)
	}
	if len(event.Actions) != 0 {
		t.Fatalf("update action must be owned by help version status: %#v", event.Actions)
	}
}

func TestHelpMapsPassiveUpdateStatesWithoutDetailsAction(t *testing.T) {
	setUpdateTestVersion(t)
	tests := []struct {
		name    string
		updates UpdateManager
		status  string
	}{
		{name: "updates disabled", status: ""},
		{name: "latest", updates: &fakeUpdateManager{}, status: "已是最新版本"},
		{name: "unsupported platform", updates: &fakeUpdateManager{checkResult: bridgeupdate.CheckResult{UnsupportedPlatform: true}}, status: "不支持当前平台"},
		{name: "check failed", updates: &fakeUpdateManager{checkErr: errors.New("offline")}, status: "暂时无法检查更新"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
			svc.Updates = tt.updates
			event := svc.helpUpdateEvent(t.Context(), "help", "message", config.ConversationModeChat, HelpCardData())
			status := event.HelpCard.VersionStatus
			if status == nil || status.CurrentVersion != "v1.0.0" || status.Status != tt.status {
				t.Fatalf("version status = %#v, want status %q", status, tt.status)
			}
			if status.UpdateAvailable || status.DetailsAction.ID != "" || len(event.Actions) != 0 {
				t.Fatalf("passive state must not expose update action: status=%#v actions=%#v", status, event.Actions)
			}
		})
	}
}

func TestHelpMapsDevelopmentBuildWithoutDetailsAction(t *testing.T) {
	old := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = old })
	buildinfo.Version = "dev"
	svc := NewService(updateTestConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Updates = &fakeUpdateManager{checkResult: updateAvailableResult()}
	event := svc.helpUpdateEvent(t.Context(), "help", "message", config.ConversationModeChat, HelpCardData())
	status := event.HelpCard.VersionStatus
	if status == nil || status.CurrentVersion != "dev" || status.Status != "开发构建不可自升级" || status.UpdateAvailable || status.DetailsAction.ID != "" {
		t.Fatalf("development version status = %#v", status)
	}
}

func TestUpdateDetailsShowsNotesAndAdminInstall(t *testing.T) {
	setUpdateTestVersion(t)
	manager := &fakeUpdateManager{checkResult: updateAvailableResult(), notes: "# Changes\n\n## Breaking Changes\n\n- 无。\n\n## Features\n\n- safer"}
	svc := NewService(updateTestConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Updates = manager
	result, err := svc.HandleActionResult(t.Context(), ActionRequest{SessionID: "update", ActionID: "update.details", Value: "1.2.0", Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || !strings.Contains(result.Event.Segments[0].Text, "safer") {
		t.Fatalf("details result = %#v", result)
	}
	if strings.Contains(result.Event.Segments[0].Text, "Breaking Changes") || strings.Contains(result.Event.Segments[0].Text, "无。") {
		t.Fatalf("details card must hide empty release-note sections: %#v", result.Event.Segments)
	}
	// Actions: 返回帮助, 立即升级 (admin-only), 发布历史 (external link, all users).
	if len(result.Event.Actions) != 3 || result.Event.Actions[1].ID != "update.install" || result.Event.Actions[1].Confirm == nil {
		t.Fatalf("details actions = %#v", result.Event.Actions)
	}
	history := result.Event.Actions[2]
	if history.ID != "update.history" || history.Label != "发布历史" || history.URL != "https://updates.example/lark-ai-agent-bridge-releases.html" {
		t.Fatalf("release-history action = %#v", history)
	}
}

func TestUpdateDetailsShowsReleaseHistoryForNonAdmin(t *testing.T) {
	setUpdateTestVersion(t)
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(policy *access.Policy) { policy.AllowedUsers = []string{"user"} }); err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("owner")
	manager := &fakeUpdateManager{checkResult: updateAvailableResult(), notes: "# Changes"}
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Access, svc.AccessControls, svc.Updates = store, controls, manager
	result, err := svc.HandleActionResult(t.Context(), ActionRequest{SessionID: "update", ActionID: "update.details", Value: "1.2.0", Actor: "user"})
	if err != nil {
		t.Fatal(err)
	}
	// Non-admin sees 返回帮助 + 发布历史 but no admin-only 立即升级.
	if len(result.Event.Actions) != 2 {
		t.Fatalf("non-admin details actions = %#v", result.Event.Actions)
	}
	for _, a := range result.Event.Actions {
		if a.ID == "update.install" {
			t.Fatalf("non-admin must not see install action: %#v", result.Event.Actions)
		}
	}
	history := result.Event.Actions[1]
	if history.ID != "update.history" || history.Label != "发布历史" || history.URL != "https://updates.example/lark-ai-agent-bridge-releases.html" {
		t.Fatalf("release-history action = %#v", history)
	}
}

func TestUpdateInstallRejectsForgedNonAdminAction(t *testing.T) {
	setUpdateTestVersion(t)
	store, err := access.OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(policy *access.Policy) { policy.AllowedUsers = []string{"user"} }); err != nil {
		t.Fatal(err)
	}
	controls := access.NewRuntimeControls()
	controls.OwnerRefreshSucceeded("owner")
	manager := &fakeUpdateManager{checkResult: updateAvailableResult(), prepared: &fakePreparedUpdate{}}
	svc := NewService(testConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Access, svc.AccessControls, svc.Updates = store, controls, manager
	result, err := svc.HandleActionResult(t.Context(), ActionRequest{SessionID: "update", ActionID: "update.install", Value: "1.2.0", Actor: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || !strings.Contains(result.Event.Segments[0].Text, "仅管理员") || manager.prepareCall != 0 {
		t.Fatalf("result=%#v prepare calls=%d", result, manager.prepareCall)
	}
}

func TestUpdateInstallRejectsQueuedWorkAfterStaging(t *testing.T) {
	setUpdateTestVersion(t)
	prepared := &fakePreparedUpdate{}
	manager := &fakeUpdateManager{checkResult: updateAvailableResult(), prepared: prepared}
	renderer := card.NewFakeRenderer()
	svc := NewService(updateTestConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Updates = manager
	key := session.Key{Agent: agent.Claude, ChatID: "dm"}
	svc.Sessions.Enqueue(key, sessionInputForUpdateTest(), t.TempDir())
	result, err := svc.HandleActionResult(t.Context(), ActionRequest{SessionID: "update", ActionID: "update.install", Value: "1.2.0", Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event != nil {
		t.Fatalf("result=%#v prepared=%#v", result, prepared)
	}
	result.StartDeferred()
	waitForEvents(t, renderer, 2)
	event := renderer.Events()[1]
	if !strings.Contains(event.Segments[0].Text, "当前有任务") || prepared.replaced != 0 || prepared.aborted != 1 {
		t.Fatalf("event=%#v prepared=%#v", event, prepared)
	}
	// busy 分支必须橙色(用户可自救,不是硬错),且附会话清单让用户看到具体是哪些会话挡了升级。
	if event.HeaderTemplate != "orange" {
		t.Fatalf("busy header template = %q, want orange", event.HeaderTemplate)
	}
	// 会话清单以 markdown 表格追加为额外 segment,且包含"占用中的会话"标题与至少一行 `dm` chat 摘要。
	if len(event.Segments) < 2 {
		t.Fatalf("busy card missing blocking sessions list: %#v", event.Segments)
	}
	tableText := event.Segments[1].Text
	if !strings.Contains(tableText, "占用中的会话") || !strings.Contains(tableText, "claude") || !strings.Contains(tableText, "dm") {
		t.Fatalf("blocking sessions list must name the queued session, got:\n%s", tableText)
	}
}

func TestUpdateInstallReplacesAndSchedulesRestart(t *testing.T) {
	setUpdateTestVersion(t)
	prepared := &fakePreparedUpdate{restarted: make(chan struct{})}
	manager := &fakeUpdateManager{checkResult: updateAvailableResult(), prepared: prepared}
	svc := NewService(updateTestConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Updates = manager
	result, err := svc.HandleActionResult(t.Context(), ActionRequest{SessionID: "update", ActionID: "update.install", Value: "1.2.0", Actor: "admin", ChatID: "oc_chat", OpenMessageID: "om_card"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event != nil {
		t.Fatalf("result=%#v", result)
	}
	result.StartDeferred()
	select {
	case <-prepared.restarted:
	case <-time.After(time.Second):
		t.Fatal("restart was not scheduled")
	}
	if prepared.replaced != 1 {
		t.Fatalf("prepared=%#v", prepared)
	}
	for _, item := range prepared.restartEnv {
		if strings.HasPrefix(item, upgradeNotifyEnvVar+"=") {
			t.Fatalf("restart env must not duplicate the persisted handoff: %q", item)
		}
	}
}

func TestUpdateMaintenanceRejectsMessagesSchedulesAndSecondInstall(t *testing.T) {
	setUpdateTestVersion(t)
	prepared := &fakePreparedUpdate{restarted: make(chan struct{}), restartRelease: make(chan struct{})}
	manager := &fakeUpdateManager{checkResult: updateAvailableResult(), prepared: prepared}
	renderer := card.NewFakeRenderer()
	svc := NewService(updateTestConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Updates = manager
	first, err := svc.HandleActionResult(t.Context(), ActionRequest{SessionID: "update", ActionID: "update.install", Value: "1.2.0", Actor: "admin", ChatID: "oc_chat", OpenMessageID: "om_card"})
	if err != nil {
		t.Fatal(err)
	}
	first.StartDeferred()
	select {
	case <-prepared.restarted:
	case <-time.After(time.Second):
		t.Fatal("restart did not enter maintenance")
	}
	if err := svc.HandleMessage(t.Context(), Message{ID: "work", ChatID: "dm", Sender: "user", Text: "run"}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if !strings.Contains(events[len(events)-1].Segments[0].Text, "正在升级") {
		t.Fatalf("maintenance event = %#v", events[len(events)-1])
	}
	if _, err := svc.Enqueue(t.Context(), schedule.Task{}, schedule.Run{}); !errors.Is(err, schedule.ErrQueueFull) {
		t.Fatalf("scheduled enqueue error = %v", err)
	}
	result, err := svc.HandleActionResult(t.Context(), ActionRequest{SessionID: "second", ActionID: "update.install", Value: "1.2.0", Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || !strings.Contains(result.Event.Segments[0].Text, "正在进行") {
		t.Fatalf("second install = %#v", result)
	}
	close(prepared.restartRelease)
}

func TestUpdateInstallAcknowledgesBeforeRefreshCompletes(t *testing.T) {
	setUpdateTestVersion(t)
	started := make(chan struct{})
	release := make(chan struct{})
	prepared := &fakePreparedUpdate{restarted: make(chan struct{})}
	manager := &fakeUpdateManager{checkResult: updateAvailableResult(), prepared: prepared, refreshStarted: started, refreshRelease: release}
	svc := NewService(updateTestConfig(t), card.NewFakeRenderer(), newFakeRunner(), audit.NewRecorder())
	svc.Updates = manager

	begin := time.Now()
	result, err := svc.HandleActionResult(t.Context(), ActionRequest{SessionID: "update", ActionID: "update.install", Value: "1.2.0", Actor: "admin", ChatID: "oc_chat", OpenMessageID: "om_card"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed > 100*time.Millisecond {
		t.Fatalf("callback ACK took %s while Refresh was blocked", elapsed)
	}
	if result.Event != nil {
		t.Fatalf("result=%#v", result)
	}
	result.StartDeferred()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background Refresh did not start")
	}
	close(release)
	select {
	case <-prepared.restarted:
	case <-time.After(time.Second):
		t.Fatal("background update did not finish")
	}
}

func TestUpgradeCommandUsesSelectedChannelAndRejectsBusySession(t *testing.T) {
	setUpdateTestVersion(t)
	prepared := &fakePreparedUpdate{restarted: make(chan struct{})}
	manager := &fakeUpdateManager{checkResult: updateAvailableResult(), prepared: prepared}
	renderer := card.NewFakeRenderer()
	svc := NewService(updateTestConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Updates = manager

	devMode, err := devmode.OpenStore(filepath.Join(t.TempDir(), "devmode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := devMode.SetPrerelease(true); err != nil {
		t.Fatal(err)
	}
	svc.DevMode = devMode
	if err := svc.HandleMessage(t.Context(), Message{ID: "upgrade-stable", ChatID: "dm", Sender: "admin", Text: "/upgrade stable"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-prepared.restarted:
	case <-time.After(time.Second):
		t.Fatal("/upgrade stable did not start the existing upgrade flow")
	}
	if len(manager.refreshChannel) != 1 || manager.refreshChannel[0] {
		t.Fatalf("refresh channels = %#v, want [false]", manager.refreshChannel)
	}

	busyRenderer := card.NewFakeRenderer()
	busy := NewService(updateTestConfig(t), busyRenderer, newFakeRunner(), audit.NewRecorder())
	busy.Updates = &fakeUpdateManager{checkResult: updateAvailableResult(), prepared: &fakePreparedUpdate{}}
	key := session.Key{Agent: agent.Claude, ChatID: "dm"}
	busy.Sessions.Enqueue(key, sessionInputForUpdateTest(), t.TempDir())
	if err := busy.HandleMessage(t.Context(), Message{ID: "upgrade-busy", ChatID: "dm", Sender: "admin", Text: "/upgrade"}); err != nil {
		t.Fatal(err)
	}
	events := busyRenderer.Events()
	if len(events) != 1 || !strings.Contains(events[0].Segments[0].Text, "会话正在运行或排队") {
		t.Fatalf("busy /upgrade event = %#v", events)
	}
}

func TestUpgradeCommandRejectsRCOutsideDeveloperMode(t *testing.T) {
	setUpdateTestVersion(t)
	renderer := card.NewFakeRenderer()
	svc := NewService(updateTestConfig(t), renderer, newFakeRunner(), audit.NewRecorder())
	svc.Updates = &fakeUpdateManager{checkResult: updateAvailableResult(), prepared: &fakePreparedUpdate{}}
	if err := svc.HandleMessage(t.Context(), Message{ID: "upgrade-rc", ChatID: "dm", Sender: "admin", Text: "/upgrade rc"}); err != nil {
		t.Fatal(err)
	}
	events := renderer.Events()
	if len(events) != 1 || !strings.Contains(events[0].Segments[0].Text, "仅在开发者模式") {
		t.Fatalf("rc outside developer mode = %#v", events)
	}
}

func sessionInputForUpdateTest() session.Input {
	return session.Input{ID: "queued", Sender: "user", Text: "work", Time: time.Now()}
}

func setUpdateTestVersion(t *testing.T) {
	t.Helper()
	old := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = old })
	buildinfo.Version = "1.0.0"
}

func TestStatusbarUpdateAvailableUsesSemverPrecedence(t *testing.T) {
	tests := []struct {
		name            string
		latest, current string
		want            bool
	}{
		{name: "older stable than current rc", latest: "0.1.12", current: "0.1.13-rc.6"},
		{name: "older rc", latest: "0.1.13-rc.5", current: "0.1.13-rc.6"},
		{name: "same rc", latest: "0.1.13-rc.6", current: "0.1.13-rc.6"},
		{name: "newer rc", latest: "0.1.13-rc.7", current: "0.1.13-rc.6", want: true},
		{name: "matching stable release", latest: "0.1.13", current: "0.1.13-rc.6", want: true},
		{name: "newer stable core", latest: "0.1.14", current: "0.1.13-rc.6", want: true},
		{name: "rc below matching stable", latest: "0.1.13-rc.7", current: "0.1.13"},
		{name: "invalid manifest version", latest: "latest", current: "0.1.13-rc.6"},
		{name: "invalid current version", latest: "0.1.14", current: "dev"},
		{name: "trim whitespace", latest: " 0.1.13-rc.7 ", current: " 0.1.13-rc.6 ", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusbarUpdateAvailable(tt.latest, tt.current); got != tt.want {
				t.Fatalf("statusbarUpdateAvailable(%q, %q) = %t, want %t", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}

func updateTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.SessionStorePath = filepath.Join(cfg.DefaultWorkDir, ".lark-agent-bridge", "sessions.json")
	return cfg
}
