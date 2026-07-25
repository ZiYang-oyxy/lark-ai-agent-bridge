package actiongrant

import (
	"path/filepath"
	"testing"
	"time"
)

func TestGrantBindsActorScopeValuePolicyAndRejectsReplay(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store, err := OpenStore(filepath.Join(t.TempDir(), "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.Issue(Spec{Actor: "ou_owner", ChatID: "oc_chat", SessionID: "run-1", ActionID: "stop", Value: "", PolicyRevision: 7, AllowAdmin: true, ExpiresAt: now.Add(time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	base := Request{GrantID: grant.ID, Actor: "ou_owner", ChatID: "oc_chat", SessionID: "run-1", ActionID: "stop", PolicyRevision: 7, Now: now.Add(time.Minute)}
	for _, tc := range []struct {
		name   string
		mutate func(*Request)
		reason Reason
	}{
		{"actor", func(r *Request) { r.Actor = "ou_other" }, ReasonActorMismatch},
		{"chat", func(r *Request) { r.ChatID = "oc_other" }, ReasonScopeMismatch},
		{"session", func(r *Request) { r.SessionID = "run-2" }, ReasonScopeMismatch},
		{"action", func(r *Request) { r.ActionID = "create_workdir" }, ReasonScopeMismatch},
		{"value", func(r *Request) { r.Value = "/tmp/changed" }, ReasonValueMismatch},
		{"policy", func(r *Request) { r.PolicyRevision = 8 }, ReasonPolicyChanged},
		{"expired", func(r *Request) { r.Now = now.Add(2 * time.Hour) }, ReasonExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			decision, err := store.Consume(req)
			if err != nil || decision.OK || decision.Reason != tc.reason {
				t.Fatalf("decision/error = %#v / %v", decision, err)
			}
		})
	}
	decision, err := store.Consume(base)
	if err != nil || !decision.OK {
		t.Fatalf("consume = %#v / %v", decision, err)
	}
	decision, err = store.Consume(base)
	if err != nil || decision.OK || decision.Reason != ReasonReplayed {
		t.Fatalf("replay = %#v / %v", decision, err)
	}
}

func TestGrantPersistsAndAllowsAdminOverrideOnlyWhenArmed(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "grants.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.Issue(Spec{Actor: "ou_user", ChatID: "oc_chat", SessionID: "s", ActionID: "resume.select", Value: "target", PolicyRevision: 2, AllowAdmin: true, ExpiresAt: now.Add(time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := reopened.Consume(Request{GrantID: grant.ID, Actor: "ou_admin", ActorIsAdmin: true, ChatID: "oc_chat", SessionID: "s", ActionID: "resume.select", Value: "target", PolicyRevision: 2, Now: now.Add(time.Minute)})
	if err != nil || !decision.OK {
		t.Fatalf("admin consume = %#v / %v", decision, err)
	}
}
