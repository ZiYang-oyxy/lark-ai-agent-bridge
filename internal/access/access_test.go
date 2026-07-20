package access

import "testing"

func TestPolicyMatchesLCABFailClosedSemantics(t *testing.T) {
	owner := NewRuntimeControls()
	owner.OwnerRefreshSucceeded("ou_owner")
	policy := Policy{
		AllowedUsers: []string{"ou_user"},
		AllowedChats: []string{"oc_allowed"},
		Admins:       []string{"ou_admin"},
	}

	tests := []struct {
		name   string
		got    Decision
		ok     bool
		reason Reason
	}{
		{"owner dm", CanUseDM(policy, owner, "ou_owner"), true, ReasonOwner},
		{"allowed dm", CanUseDM(policy, owner, "ou_user"), true, ReasonAllowedUser},
		{"admin dm", CanUseDM(policy, owner, "ou_admin"), true, ReasonAllowedAdmin},
		{"denied dm", CanUseDM(policy, owner, "ou_other"), false, ReasonDeniedUser},
		{"owner group", CanUseGroup(policy, owner, "oc_other", "ou_owner"), true, ReasonOwner},
		{"admin group", CanUseGroup(policy, owner, "oc_other", "ou_admin"), true, ReasonAllowedAdmin},
		{"allowed group", CanUseGroup(policy, owner, "oc_allowed", "ou_other"), true, ReasonAllowedChat},
		{"denied group", CanUseGroup(policy, owner, "oc_other", "ou_other"), false, ReasonDeniedChat},
		{"admin command", CanRunAdminCommand(policy, owner, "ou_admin"), true, ReasonAllowedAdmin},
		{"denied command", CanRunAdminCommand(policy, owner, "ou_other"), false, ReasonDeniedAdmin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got.OK != tt.ok || tt.got.Reason != tt.reason {
				t.Fatalf("decision = %#v, want ok=%v reason=%q", tt.got, tt.ok, tt.reason)
			}
		})
	}
}

func TestOwnerRefreshFailureKeepsCachedOwnerButUnknownDoesNotGrant(t *testing.T) {
	controls := NewRuntimeControls()
	controls.OwnerRefreshSucceeded("ou_owner")
	controls.OwnerRefreshFailed(assertErr("permission denied"))
	if got := CanRunAdminCommand(Policy{}, controls, "ou_owner"); !got.OK || got.Reason != ReasonOwner {
		t.Fatalf("cached owner decision = %#v", got)
	}

	unknown := NewRuntimeControls()
	unknown.SetCachedOwnerForTest("ou_owner")
	if got := CanRunAdminCommand(Policy{}, unknown, "ou_owner"); got.OK {
		t.Fatalf("unknown refresh state granted owner: %#v", got)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
