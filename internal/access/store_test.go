package access

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreDefaultsClosedAndPersistsAtomicMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); len(got.AllowedUsers)+len(got.AllowedChats)+len(got.Admins) != 0 {
		t.Fatalf("default policy = %#v, want empty", got)
	}
	if err := store.Update(func(p *Policy) { p.AllowedUsers = append(p.AllowedUsers, "ou_user") }); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get().AllowedUsers; len(got) != 1 || got[0] != "ou_user" {
		t.Fatalf("allowed users = %#v", got)
	}
}

func TestStoreSerializesConcurrentMutations(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range []string{"ou_a", "ou_b", "ou_c", "ou_d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Update(func(p *Policy) { p.AllowedUsers = append(p.AllowedUsers, id) }); err != nil {
				t.Errorf("update %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	if got := store.Get().AllowedUsers; len(got) != 4 {
		t.Fatalf("allowed users = %#v, want four", got)
	}
}

func TestStoreRejectsCorruptAndUnsupportedSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name string
		data any
	}{
		{"corrupt", []byte("{")},
		{"schema", map[string]any{"schema_version": 99, "revision": 1, "access": map[string]any{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "access.json")
			var data []byte
			if raw, ok := tc.data.([]byte); ok {
				data = raw
			} else {
				data, _ = json.Marshal(tc.data)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenStore(path); err == nil {
				t.Fatal("OpenStore succeeded, want error")
			}
		})
	}
}

func TestStoreLoadsLegacyAllowedChatsAsAllMembersAndPersistsSelectedMembers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	legacy := []byte(`{"schema_version":1,"revision":7,"access":{"allowed_chats":["oc_legacy"]}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := GroupPolicyFor(store.Get(), "oc_legacy"); got.Mode != GroupModeAllMembers || store.Revision() != 7 {
		t.Fatalf("legacy policy/revision = %#v / %d", got, store.Revision())
	}
	if err := store.Update(func(p *Policy) {
		p.GroupPolicies["oc_legacy"] = GroupPolicy{Mode: GroupModeSelectedMembers, AllowedMembers: []string{"ou_b", "ou_b", "ou_a"}}
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	group := GroupPolicyFor(reopened.Get(), "oc_legacy")
	if group.Mode != GroupModeSelectedMembers || len(group.AllowedMembers) != 2 || reopened.Revision() != 8 {
		t.Fatalf("selected policy/revision = %#v / %d", group, reopened.Revision())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted snapshot
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", persisted.SchemaVersion, SchemaVersion)
	}
}
