package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	signupPath      = "/api/v1/auth/signup"
	invitationsPath = "/api/v1/workspaces/" + covWS + "/invitations"
	membersPath     = "/api/v1/workspaces/" + covWS + "/members"
	adminUsers      = "/api/v1/admin/users"
)

// fixtureRoster is what GET /admin/users reports once signup has
// redeemed the invitations the seed just created.
func fixtureRoster() []map[string]any {
	roster := make([]map[string]any, 0, len(demoUsers))
	for i, u := range demoUsers {
		roster = append(roster, map[string]any{"id": fmt.Sprintf("u%d", i), "email": u.Email, "role": u.Role})
	}
	return roster
}

func TestSeedRBACUsers_RequiresWorkspace(t *testing.T) {
	client := cli.NewClient("http://127.0.0.1:1", "tok", "")
	err := seedRBACUsers(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "workspace_id not set") {
		t.Errorf("got %v", err)
	}
}

func TestSeedRBACUsers_Canceled(t *testing.T) {
	client := cli.NewClient("http://127.0.0.1:1", "tok", covWS)
	if err := seedRBACUsers(canceledCtx(), client); err != context.Canceled {
		t.Errorf("got %v", err)
	}
}

// The seed must never reach POST /members with an email: there is no
// endpoint that maps an arbitrary address to a user id, on purpose
// (#1254 — it would be the enumeration oracle behind an admin gate).
// Placement goes invitation → signup → roster check.
func TestSeedRBACUsers_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(invitationsPath, clitest.JSONResponse(201, map[string]string{"id": "inv1"}))
	// Signup answers the de-enumerated 202 with no account id — the
	// seed must not need one.
	stub.OnPost(signupPath, clitest.JSONResponse(202, map[string]any{"ok": true}))
	stub.OnGet(adminUsers, clitest.JSONResponse(200, fixtureRoster()))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("seedRBACUsers: %v", err)
		}
	})

	invites := stub.CallsFor("POST", invitationsPath)
	if len(invites) != len(demoUsers) {
		t.Fatalf("invitations = %d, want %d", len(invites), len(demoUsers))
	}
	// The invitation is what carries the fixture role.
	var first map[string]any
	clitest.MustDecodeJSONBody(invites[0].Body, &first)
	if first["email"] != demoUsers[0].Email || first["role"] != demoUsers[0].Role {
		t.Errorf("invitation body = %v", first)
	}
	if n := len(stub.CallsFor("POST", signupPath)); n != len(demoUsers) {
		t.Fatalf("signups = %d, want %d", n, len(demoUsers))
	}
	if n := len(stub.CallsFor("POST", membersPath)); n != 0 {
		t.Errorf("seed hit POST /members %d times — placement must go through the invitation", n)
	}
	// Credential table printed for every minted user.
	for _, u := range demoUsers {
		if !strings.Contains(out, u.Email) || !strings.Contains(out, u.Password) {
			t.Errorf("credential table missing %s:\n%s", u.Email, out)
		}
	}
}

// Re-seeding an instance that already has the fixture: the invitation
// POST 409s ("already a member"), signup answers the same generic 202,
// and the roster confirms everyone is where the fixture wants them.
func TestSeedRBACUsers_InviteConflictIsIdempotent(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(invitationsPath, clitest.ErrorResponse(409, "User is already a member of this workspace"))
	stub.OnPost(signupPath, clitest.JSONResponse(202, map[string]any{"ok": true}))
	stub.OnGet(adminUsers, clitest.JSONResponse(200, fixtureRoster()))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("seedRBACUsers: %v", err)
		}
	})
	if !strings.Contains(out, "already in workspace with role "+demoUsers[0].Role) {
		t.Errorf("expected idempotent re-seed line:\n%s", out)
	}
	if !strings.Contains(out, "RBAC fixture credentials") {
		t.Errorf("expected credential table:\n%s", out)
	}
}

// A role that drifted from the fixture is a warning — there is no
// role-update endpoint to fix it with.
func TestSeedRBACUsers_RoleDrift(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(invitationsPath, clitest.ErrorResponse(409, "member exists"))
	stub.OnPost(signupPath, clitest.JSONResponse(202, map[string]any{"ok": true}))
	drifted := "VIEWER"
	if demoUsers[0].Role == drifted {
		drifted = "MEMBER"
	}
	stub.OnGet(adminUsers, clitest.JSONResponse(200, []map[string]any{
		{"id": "u1", "email": demoUsers[0].Email, "role": drifted},
	}))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("seedRBACUsers: %v", err)
		}
	})
	if !strings.Contains(out, "≠ fixture") {
		t.Errorf("expected role-drift warning:\n%s", out)
	}
	if !strings.Contains(out, "RBAC fixture credentials") {
		t.Errorf("expected credential table:\n%s", out)
	}
}

// An address that already had an account before the seed ran: signup is
// a no-op there (it must not redeem invitations for an account whose
// owner never turned up), so the roster still doesn't know them.
func TestSeedRBACUsers_PreExistingAccountIsReported(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(invitationsPath, clitest.JSONResponse(201, map[string]string{"id": "inv1"}))
	stub.OnPost(signupPath, clitest.JSONResponse(202, map[string]any{"ok": true}))
	stub.OnGet(adminUsers, clitest.JSONResponse(200, []map[string]any{}))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("seedRBACUsers: %v", err)
		}
	})
	if !strings.Contains(out, "account predates this seed") {
		t.Errorf("expected pre-existing-account line:\n%s", out)
	}
	if !strings.Contains(out, "no users seeded") {
		t.Errorf("expected empty-mint summary:\n%s", out)
	}
}

func TestSeedRBACUsers_SignupFailuresAreNonFatal(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(invitationsPath, clitest.JSONResponse(201, map[string]string{"id": "inv1"}))
	stub.OnPost(signupPath, clitest.ErrorResponse(500, "signup broken"))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("hard failure must not abort the seed: %v", err)
		}
	})
	if !strings.Contains(out, "signup HTTP 500") {
		t.Errorf("expected per-user failure lines:\n%s", out)
	}
	if !strings.Contains(out, "no users seeded") {
		t.Errorf("expected empty-mint summary:\n%s", out)
	}
}

func TestSeedRBACUsers_InviteErrorSkipsUser(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(invitationsPath, clitest.ErrorResponse(403, "not allowed"))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("seedRBACUsers: %v", err)
		}
	})
	if !strings.Contains(out, "not allowed") {
		t.Errorf("expected invite failure lines:\n%s", out)
	}
	// Signup never runs for a user we couldn't invite — otherwise we'd
	// mint an account that lands nowhere.
	if n := len(stub.CallsFor("POST", signupPath)); n != 0 {
		t.Errorf("signups = %d after invite failure, want 0", n)
	}
	if !strings.Contains(out, "no users seeded") {
		t.Errorf("nobody minted → summary expected:\n%s", out)
	}
}

func TestSeedRBACUsers_RosterLookupFailureIsNonFatal(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(invitationsPath, clitest.JSONResponse(201, map[string]string{"id": "inv1"}))
	stub.OnPost(signupPath, clitest.JSONResponse(202, map[string]any{"ok": true}))
	stub.OnGet(adminUsers, clitest.ErrorResponse(500, "roster broken"))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("lookup failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "roster lookup failed") {
		t.Errorf("expected lookup failure lines:\n%s", out)
	}
}

func TestSeedRBACUsers_TransportErrorsAreNonFatal(t *testing.T) {
	// Dead server: every invitation POST fails at the transport layer.
	client := cli.NewClient("http://127.0.0.1:1", "tok", covWS)
	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), client); err != nil {
			t.Errorf("transport failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "invite request failed") {
		t.Errorf("expected per-user transport failure lines:\n%s", out)
	}
	if !strings.Contains(out, "no users seeded") {
		t.Errorf("expected empty summary:\n%s", out)
	}
}

func TestSeedRBACUsers_SignupTransportErrorSkipsUser(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(invitationsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"inv1"}`))
	})
	mux.HandleFunc(signupPath, func(w http.ResponseWriter, _ *http.Request) { killConn(w) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), cli.NewClient(srv.URL, "tok", covWS)); err != nil {
			t.Errorf("signup transport failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "signup request failed") {
		t.Errorf("expected signup failure lines:\n%s", out)
	}
	if !strings.Contains(out, "no users seeded") {
		t.Errorf("expected empty summary:\n%s", out)
	}
}

func TestSeedRBACUsers_MidLoopCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc(invitationsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"inv1"}`))
	})
	mux.HandleFunc(signupPath, func(w http.ResponseWriter, _ *http.Request) {
		cancel() // first signup lands; second loop iteration sees ctx.Err()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc(adminUsers, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_ = captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(ctx, cli.NewClient(srv.URL, "tok", covWS)); err != context.Canceled {
			t.Errorf("got %v, want context.Canceled", err)
		}
	})
}

// ─── findWorkspaceMemberByEmail ─────────────────────────────────────

func TestFindWorkspaceMemberByEmail(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	role := "ADMIN"
	stub.OnGet(adminUsers, clitest.JSONResponse(200, []map[string]any{
		{"id": "u1", "email": "Karel@Crewship.Local", "role": role},
		{"id": "u2", "email": "norole@crewship.local", "role": nil},
	}))
	client := newSeedClient(stub)

	// Case-insensitive hit with role.
	id, gotRole, err := findWorkspaceMemberByEmail(client, covWS, "karel@crewship.local")
	if err != nil || id != "u1" || gotRole != "ADMIN" {
		t.Errorf("got id=%q role=%q err=%v", id, gotRole, err)
	}
	// Nil role coalesces to "".
	id, gotRole, err = findWorkspaceMemberByEmail(client, covWS, "norole@crewship.local")
	if err != nil || id != "u2" || gotRole != "" {
		t.Errorf("nil role: id=%q role=%q err=%v", id, gotRole, err)
	}
	// Miss → empty result, no error.
	id, gotRole, err = findWorkspaceMemberByEmail(client, covWS, "ghost@crewship.local")
	if err != nil || id != "" || gotRole != "" {
		t.Errorf("miss: id=%q role=%q err=%v", id, gotRole, err)
	}
	// Query carries the workspace id.
	calls := stub.CallsFor("GET", adminUsers)
	if len(calls) == 0 || !strings.Contains(calls[0].Query, "workspace_id="+covWS) {
		t.Errorf("workspace_id missing from query: %+v", calls)
	}

	// API error propagates.
	stub.OnGet(adminUsers, clitest.ErrorResponse(403, "owner only"))
	if _, _, err := findWorkspaceMemberByEmail(client, covWS, "x@y.z"); err == nil ||
		!strings.Contains(err.Error(), "owner only") {
		t.Errorf("API error: got %v", err)
	}

	// Transport error propagates.
	dead := cli.NewClient("http://127.0.0.1:1", "tok", covWS)
	if _, _, err := findWorkspaceMemberByEmail(dead, covWS, "x@y.z"); err == nil {
		t.Error("expected transport error")
	}
}

func TestFindWorkspaceMemberByEmail_ParseError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet(adminUsers, clitest.TextResponse(200, "{not json"))
	if _, _, err := findWorkspaceMemberByEmail(newSeedClient(stub), covWS, "x@y.z"); err == nil {
		t.Error("expected parse error")
	}
}
