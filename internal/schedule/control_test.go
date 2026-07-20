package schedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProposalContextIsSingleUse(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	registry := NewContextRegistry(time.Minute)
	token, err := registry.Issue(fixtureProposalContext(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Consume(token, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Consume(token, now.Add(2*time.Second)); !errors.Is(err, ErrContextUsed) {
		t.Fatalf("second consume error = %v", err)
	}
}

func TestProposalContextExpiresAndCanBeRevoked(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	registry := NewContextRegistry(time.Minute)
	expired, _ := registry.Issue(fixtureProposalContext(), now)
	if _, err := registry.Consume(expired, now.Add(2*time.Minute)); !errors.Is(err, ErrContextExpired) {
		t.Fatalf("expired error = %v", err)
	}
	revoked, _ := registry.Issue(fixtureProposalContext(), now)
	registry.Revoke(revoked)
	if _, err := registry.Consume(revoked, now); !errors.Is(err, ErrContextUnknown) {
		t.Fatalf("revoked error = %v", err)
	}
}

func TestControlSocketCreatesDraftFromBoundContext(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	store, err := NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := NewContextRegistry(time.Minute)
	bound := fixtureProposalContext()
	token, err := registry.Issue(bound, now)
	if err != nil {
		t.Fatal(err)
	}
	socketPath := shortSocketPath(t)
	server := NewControlServer(socketPath, store, registry, ControlConfig{Now: func() time.Time { return now }, DraftTTL: 10 * time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client := ProposeClient{SocketPath: socketPath, Timeout: time.Second}
	response, err := client.Propose(context.Background(), ProposeRequest{Token: token, Proposal: Proposal{
		Kind:        KindCron,
		CronExpr:    "0 9 * * 1-5",
		Timezone:    "Asia/Shanghai",
		Description: "工作日总结",
		Prompt:      "总结昨天的项目进展",
	}})
	if err != nil {
		t.Fatal(err)
	}
	draft, ok := store.Draft(response.DraftID)
	if !ok {
		t.Fatal("draft not persisted")
	}
	if draft.Creator != bound.Creator || draft.Target != bound.Target || draft.Execution != bound.Execution {
		t.Fatalf("draft escaped bound context: %#v", draft)
	}
	if draft.OriginRunID != bound.OriginRunID {
		t.Fatalf("origin run = %q", draft.OriginRunID)
	}
	if len(draft.Next) != 3 || response.Description == "" {
		t.Fatalf("response = %#v draft = %#v", response, draft)
	}
	if info, err := os.Stat(socketPath); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o", got)
	}
}

func TestControlSocketRejectsForcedKindMismatchWithoutDraft(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	store, _ := NewStore(filepath.Join(t.TempDir(), "schedules.json"))
	registry := NewContextRegistry(time.Minute)
	bound := fixtureProposalContext()
	bound.ForcedKind = KindTimer
	token, _ := registry.Issue(bound, now)
	socketPath := shortSocketPath(t)
	server := NewControlServer(socketPath, store, registry, ControlConfig{Now: func() time.Time { return now }})
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client := ProposeClient{SocketPath: socketPath, Timeout: time.Second}
	_, err := client.Propose(context.Background(), ProposeRequest{Token: token, Proposal: Proposal{Kind: KindCron, CronExpr: "0 9 * * *", Timezone: "UTC", Prompt: "bad"}})
	if err == nil {
		t.Fatal("expected forced-kind error")
	}
	if got := len(store.Drafts()); got != 0 {
		t.Fatalf("draft count = %d", got)
	}
}

func fixtureProposalContext() ProposalContext {
	return ProposalContext{
		OriginRunID: "claude:oc_chat:thread:omt_topic:message:om_msg",
		Creator:     "ou_creator",
		Target:      Target{ChatID: "oc_chat", ThreadID: "omt_topic", ReplyToMessageID: "om_msg", IsGroup: true},
		Execution: FrozenExecution{
			Agent: "claude", Model: "sonnet", Effort: "low", AgentHome: "default",
			AgentBin: "workspace", WorkDir: "/tmp/project", ReplyMode: "append", ConversationMode: "topic",
		},
	}
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lab-schedule-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "control.sock")
}
