package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	signupPath  = "/api/v1/auth/signup"
	membersPath = "/api/v1/workspaces/" + covWS + "/members"
	adminUsers  = "/api/v1/admin/users"
)

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

func TestSeedRBACUsers_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(signupPath, func(_ *http.Request, body []byte) (int, []byte, string) {
		var req map[string]string
		clitest.MustDecodeJSONBody(body, &req)
		return 201, []byte(`{"id":"u-` + req["email"] + `","email":"` + req["email"] + `"}`), "application/json"
	})
	stub.OnPost(membersPath, clitest.JSONResponse(201, map[string]string{"id": "m1"}))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("seedRBACUsers: %v", err)
		}
	})

	signups := stub.CallsFor("POST", signupPath)
	if len(signups) != len(demoUsers) {
		t.Fatalf("signups = %d, want %d", len(signups), len(demoUsers))
	}
	members := stub.CallsFor("POST", membersPath)
	if len(members) != len(demoUsers) {
		t.Fatalf("member POSTs = %d, want %d", len(members), len(demoUsers))
	}
	// Role pinning carries the user id from signup plus the fixture role.
	var first map[string]any
	clitest.MustDecodeJSONBody(members[0].Body, &first)
	if first["user_id"] != "u-"+demoUsers[0].Email || first["role"] != demoUsers[0].Role {
		t.Errorf("member body = %v", first)
	}
	// Credential table printed for every minted user.
	for _, u := range demoUsers {
		if !strings.Contains(out, u.Email) || !strings.Contains(out, u.Password) {
			t.Errorf("credential table missing %s:\n%s", u.Email, out)
		}
	}
}

func TestSeedRBACUsers_SignupConflictReusesExistingMember(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(signupPath, clitest.ErrorResponse(409, "already exists"))
	// Admin roster: first fixture user exists with the matching role,
	// second with a drifted role, the rest are invisible (cross-workspace).
	role0 := demoUsers[0].Role
	drifted := "VIEWER"
	if demoUsers[1].Role == drifted {
		drifted = "MEMBER"
	}
	stub.OnGet(adminUsers, clitest.JSONResponse(200, []map[string]any{
		{"id": "u1", "email": strings.ToUpper(demoUsers[0].Email), "role": role0}, // EqualFold match
		{"id": "u2", "email": demoUsers[1].Email, "role": drifted},
	}))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("seedRBACUsers: %v", err)
		}
	})

	if !strings.Contains(out, "already in workspace with role "+role0) {
		t.Errorf("expected idempotent-skip line:\n%s", out)
	}
	if !strings.Contains(out, "≠ fixture") {
		t.Errorf("expected role-drift warning:\n%s", out)
	}
	if !strings.Contains(out, "lookup endpoint can't see them") {
		t.Errorf("expected invisible-user skip line:\n%s", out)
	}
	// No member POST goes out on the 409 recovery path here (matching
	// role skips, drifted role warns, invisible user skips).
	if got := len(stub.CallsFor("POST", membersPath)); got != 0 {
		t.Errorf("member POSTs = %d, want 0", got)
	}
}

func TestSeedRBACUsers_FailuresAreNonFatal(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
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

func TestSeedRBACUsers_MemberConflictIsIdempotent(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(signupPath, clitest.JSONResponse(201, map[string]string{"id": "u1", "email": "x"}))
	stub.OnPost(membersPath, clitest.ErrorResponse(409, "member exists"))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("seedRBACUsers: %v", err)
		}
	})
	if !strings.Contains(out, "already in workspace; assumed role pinned earlier") {
		t.Errorf("expected member-conflict line:\n%s", out)
	}
	// All users still land in the credential table.
	if !strings.Contains(out, "RBAC fixture credentials") {
		t.Errorf("expected credential table:\n%s", out)
	}
}

func TestSeedRBACUsers_MemberAssignErrorSkipsUser(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(signupPath, clitest.JSONResponse(201, map[string]string{"id": "u1", "email": "x"}))
	stub.OnPost(membersPath, clitest.ErrorResponse(403, "not allowed"))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("seedRBACUsers: %v", err)
		}
	})
	if !strings.Contains(out, "not allowed") {
		t.Errorf("expected assign failure lines:\n%s", out)
	}
	if !strings.Contains(out, "no users seeded") {
		t.Errorf("nobody minted → summary expected:\n%s", out)
	}
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

func TestSeedRBACUsers_TransportErrorsAreNonFatal(t *testing.T) {
	// Dead server: every signup POST fails at the transport layer.
	client := cli.NewClient("http://127.0.0.1:1", "tok", covWS)
	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), client); err != nil {
			t.Errorf("transport failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "signup request failed") {
		t.Errorf("expected per-user transport failure lines:\n%s", out)
	}
	if !strings.Contains(out, "no users seeded") {
		t.Errorf("expected empty summary:\n%s", out)
	}
}

func TestSeedRBACUsers_LookupAfter409Fails(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(signupPath, clitest.ErrorResponse(409, "exists"))
	stub.OnGet(adminUsers, clitest.ErrorResponse(500, "roster broken"))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("lookup failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "lookup after 409 failed") {
		t.Errorf("expected lookup failure lines:\n%s", out)
	}
}

func TestSeedRBACUsers_SignupParseError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(signupPath, clitest.TextResponse(201, "not json"))

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), newSeedClient(stub)); err != nil {
			t.Errorf("parse failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "signup parse") {
		t.Errorf("expected parse failure lines:\n%s", out)
	}
}

func TestSeedRBACUsers_MidLoopCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc(signupPath, func(w http.ResponseWriter, _ *http.Request) {
		cancel() // first signup lands; second loop iteration sees ctx.Err()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"u1","email":"x"}`))
	})
	mux.HandleFunc(membersPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_ = captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(ctx, cli.NewClient(srv.URL, "tok", covWS)); err != context.Canceled {
			t.Errorf("got %v, want context.Canceled", err)
		}
	})
}

func TestSeedRBACUsers_MemberTransportErrorSkipsUser(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(signupPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"u1","email":"x"}`))
	})
	mux.HandleFunc(membersPath, func(w http.ResponseWriter, _ *http.Request) { killConn(w) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := captureStdoutCovCli2(t, func() {
		if err := seedRBACUsers(context.Background(), cli.NewClient(srv.URL, "tok", covWS)); err != nil {
			t.Errorf("member transport failures must not abort: %v", err)
		}
	})
	if !strings.Contains(out, "assign") {
		t.Errorf("expected assign failure lines:\n%s", out)
	}
	if !strings.Contains(out, "no users seeded") {
		t.Errorf("expected empty summary:\n%s", out)
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
