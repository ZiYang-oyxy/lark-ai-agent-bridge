package reply

import (
	"os"
	"path/filepath"
	"testing"

	"lark-agent-bridge/internal/session"
)

func TestStorePersistsLatestRenderRefAndReturnsClones(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "replies.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := &session.RenderRef{CardID: "card-1", ReplyMessageID: "reply-1", Version: 3}
	if err := store.SetLatest("claude:chat", want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("reply store mode = %o, want 600", info.Mode().Perm())
	}
	want.CardID = "caller-mutated"
	got := store.GetLatest("claude:chat")
	if got == nil || got.CardID != "card-1" || got.ReplyMessageID != "reply-1" || got.Version != 3 {
		t.Fatalf("latest ref = %#v", got)
	}
	got.CardID = "returned-mutated"
	if again := store.GetLatest("claude:chat"); again == nil || again.CardID != "card-1" {
		t.Fatalf("store state was mutated through GetLatest: %#v", again)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted := reopened.GetLatest("claude:chat"); persisted == nil || persisted.CardID != "card-1" || persisted.Version != 3 {
		t.Fatalf("reopened latest ref = %#v", persisted)
	}
	if err := reopened.SetLatest("claude:chat", nil); err != nil {
		t.Fatal(err)
	}
	if reopened.GetLatest("claude:chat") != nil {
		t.Fatal("cleared scope still has a latest ref")
	}
}

func TestStoreRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replies.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":99,"revision":1,"latest_by_scope":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("OpenStore() error = nil, want unknown schema failure")
	}
}

func TestStoreFailedReplacementDoesNotPublishCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replies.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetLatest("scope", &session.RenderRef{CardID: "stable", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLatest("scope", &session.RenderRef{CardID: "candidate", Version: 2}); err == nil {
		t.Fatal("SetLatest() error = nil, want replacement failure")
	}
	if got := store.GetLatest("scope"); got == nil || got.CardID != "stable" || got.Version != 1 {
		t.Fatalf("latest ref after failed write = %#v", got)
	}
}
