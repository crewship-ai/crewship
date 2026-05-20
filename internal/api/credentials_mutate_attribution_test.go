package api

import (
	"net/http"
	"testing"
)

// resolveCreateAttribution is the slice of credential-create logic
// that polices who can attribute a row to whom. Each test pins one
// of the (actor_type, actor_id, role) shapes from the comment above
// the call site in Create.

func sptr(s string) *string { return &s }

func TestResolveCreateAttribution_DefaultsToUserSelf(t *testing.T) {
	req := createCredentialRequest{} // no attribution fields
	user := &AuthUser{ID: "user_a"}
	gotType, gotID, err := resolveCreateAttribution(req, user, "MEMBER")
	if err != nil {
		t.Fatalf("expected success, got %+v", err)
	}
	if gotType != "user" {
		t.Errorf("type = %q, want user", gotType)
	}
	if gotID == nil || *gotID != "user_a" {
		t.Errorf("id = %v, want user_a", gotID)
	}
}

func TestResolveCreateAttribution_RejectsBadType(t *testing.T) {
	req := createCredentialRequest{CreatedByActorType: sptr("hacker")}
	user := &AuthUser{ID: "user_a"}
	_, _, err := resolveCreateAttribution(req, user, "OWNER")
	if err == nil {
		t.Fatal("expected rejection for unknown actor_type")
	}
	if err.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", err.status)
	}
}

func TestResolveCreateAttribution_NonUserRequiresPrivileged(t *testing.T) {
	cases := []struct {
		role string
		want bool // expect error
	}{
		{"VIEWER", true},
		{"MEMBER", true},
		{"MANAGER", true},
		{"ADMIN", false},
		{"OWNER", false},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			req := createCredentialRequest{
				CreatedByActorType: sptr("agent"),
				CreatedByActorID:   sptr("agent_x"),
			}
			user := &AuthUser{ID: "user_a"}
			_, _, err := resolveCreateAttribution(req, user, tc.role)
			if (err != nil) != tc.want {
				t.Errorf("role=%q: want err? %v, got err=%+v", tc.role, tc.want, err)
			}
		})
	}
}

func TestResolveCreateAttribution_UserCannotSpoofAnotherUserID(t *testing.T) {
	// MEMBER providing a different user.id is treated as a spoof.
	req := createCredentialRequest{
		CreatedByActorType: sptr("user"),
		CreatedByActorID:   ptr("user_b"),
	}
	user := &AuthUser{ID: "user_a"}
	_, _, err := resolveCreateAttribution(req, user, "MEMBER")
	if err == nil {
		t.Fatal("expected forbidden when a MEMBER attributes to another user")
	}
	if err.status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", err.status)
	}
}

func TestResolveCreateAttribution_OwnerCanReattributeToOtherUser(t *testing.T) {
	// Audit / ownership-migration use case — admin assigns a
	// credential to a different user.
	req := createCredentialRequest{
		CreatedByActorType: sptr("user"),
		CreatedByActorID:   ptr("user_b"),
	}
	user := &AuthUser{ID: "user_a"}
	gotType, gotID, err := resolveCreateAttribution(req, user, "OWNER")
	if err != nil {
		t.Fatalf("OWNER reattribution rejected: %+v", err)
	}
	if gotType != "user" || gotID == nil || *gotID != "user_b" {
		t.Errorf("type=%q id=%v; want user/user_b", gotType, gotID)
	}
}

func TestResolveCreateAttribution_AgentRequiresExplicitID(t *testing.T) {
	// nil agent id → reject (no silent self-fallback).
	req := createCredentialRequest{
		CreatedByActorType: sptr("agent"),
		// CreatedByActorID intentionally absent
	}
	user := &AuthUser{ID: "user_a"}
	_, _, err := resolveCreateAttribution(req, user, "OWNER")
	if err == nil {
		t.Fatal("expected rejection for actor_type=agent with no actor_id")
	}
	if err.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", err.status)
	}
}

func TestResolveCreateAttribution_AgentHappyPath(t *testing.T) {
	req := createCredentialRequest{
		CreatedByActorType: sptr("agent"),
		CreatedByActorID:   ptr("agent_trapper"),
	}
	user := &AuthUser{ID: "user_a"}
	gotType, gotID, err := resolveCreateAttribution(req, user, "OWNER")
	if err != nil {
		t.Fatalf("happy path rejected: %+v", err)
	}
	if gotType != "agent" || gotID == nil || *gotID != "agent_trapper" {
		t.Errorf("type=%q id=%v; want agent/agent_trapper", gotType, gotID)
	}
}

func TestResolveCreateAttribution_SystemRejectsExplicitID(t *testing.T) {
	// System actor must NOT carry an id — querying "system rows"
	// should stay a simple type filter, not a polymorphic match.
	req := createCredentialRequest{
		CreatedByActorType: sptr("system"),
		CreatedByActorID:   sptr("something"),
	}
	user := &AuthUser{ID: "user_a"}
	_, _, err := resolveCreateAttribution(req, user, "OWNER")
	if err == nil {
		t.Fatal("expected rejection for system actor with explicit id")
	}
	// Status is part of the contract — pin it so a future helper
	// refactor can't silently flip the rejection from 400 to 5xx.
	if err.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", err.status)
	}
}

func TestResolveCreateAttribution_SystemNilID(t *testing.T) {
	req := createCredentialRequest{CreatedByActorType: sptr("system")}
	user := &AuthUser{ID: "user_a"}
	gotType, gotID, err := resolveCreateAttribution(req, user, "OWNER")
	if err != nil {
		t.Fatalf("system happy path rejected: %+v", err)
	}
	if gotType != "system" || gotID != nil {
		t.Errorf("type=%q id=%v; want system/nil", gotType, gotID)
	}
}
