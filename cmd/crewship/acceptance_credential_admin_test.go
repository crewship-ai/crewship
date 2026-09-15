package main

// Acceptance for the provider-account administration commands added in
// #2379 / #2439 / #2450 / #2453, driven through the BUILT BINARY against the
// REAL api router over a migrated SQLite (the acceptance_page_access_test.go
// shape). The handlers' own contracts are proved in internal/api; this file
// proves what only the binary can — that each command reaches its route with
// the headers and body the handler reads, that the server's answer comes back
// through the formatter, and that a refusal exits non-zero with the server's
// sentence:
//
//   - `credential pool create|list|get|update|delete` — the ETag round trip:
//     the revision `pool get` prints is what `--revision` sends as If-Match,
//     a stale revision is a 412, and a retired pool is gone from list/get.
//   - `escalation supply` — the value read from stdin activates the REQUESTED
//     row, grants it to the asking agent handle-only and resolves the
//     escalation, all as one transaction.
//   - `credential refresh` — a subscription login is renewed through the
//     provider refresher (a fake here; nothing dials OpenAI), a login with no
//     refresh flow is a 400, and a MEMBER cannot see the row at all.
//   - `credential list --kind provider_login` — only login rows come back,
//     and a MEMBER does not see provider accounts (#2439).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/providerlogin"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const credAdminWorkspaceID = "ccredadminws000000001"

// credAdminRig is one real server with an OWNER and a MEMBER, each holding a
// CLI token, and a config file per (token, format) pair for the binary.
type credAdminRig struct {
	t      *testing.T
	db     *database.DB
	router *api.Router
	srv    *httptest.Server
	binary string
	cfgDir string
}

func startCredAdminServer(t *testing.T) *credAdminRig {
	t.Helper()
	// The vault encrypts on create/supply and fails closed without a key; the
	// server runs in-process, so t.Setenv reaches it.
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Cred Admin', 'cred-admin')`, credAdminWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('cadm-owner', 'owner@cadm.invalid', 'Owner')`)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('cadm-member', 'member@cadm.invalid', 'Member')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('cadm-m-owner', ?, 'cadm-owner', 'OWNER')`, credAdminWorkspaceID)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('cadm-m-member', ?, 'cadm-member', 'MEMBER')`, credAdminWorkspaceID)
	for _, tok := range []struct{ id, user, token string }{
		{"cadm-tok-owner", "cadm-owner", credAdminOwnerToken},
		{"cadm-tok-member", "cadm-member", credAdminMemberToken},
	} {
		mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, 't', ?, datetime('now'))`,
			tok.id, tok.user, sha256HexToken(tok.token))
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &credAdminRig{t: t, db: dbh, router: router, srv: srv, binary: buildCrewshipBinary(t), cfgDir: t.TempDir()}
}

const (
	credAdminOwnerToken  = "crewship_cli_cadmowner000000000000000000"
	credAdminMemberToken = "crewship_cli_cadmmember00000000000000000"
)

func (r *credAdminRig) config(token, format string) string {
	r.t.Helper()
	who := "member"
	if token == credAdminOwnerToken {
		who = "owner"
	}
	cfg := filepath.Join(r.cfgDir, who+"-"+format+".yaml")
	if err := os.WriteFile(cfg, []byte("server: "+r.srv.URL+"\nworkspace: "+credAdminWorkspaceID+"\ntoken: "+token+"\nformat: "+format+"\n"), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return cfg
}

// exec runs the binary with cfg and returns stdout on its own and stdout+stderr
// together: the machine format goes to stdout alone (#2086), warnings and
// receipts to stderr. stdin, when non-empty, is piped the way `escalation
// supply` reads a secret.
func (r *credAdminRig) exec(cfg, stdin string, args ...string) (stdout, combined string, err error) {
	r.t.Helper()
	cmd := exec.Command(r.binary, args...)
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), outBuf.String() + errBuf.String(), err
}

// run is exec for the cases that assert on the whole conversation.
func (r *credAdminRig) run(cfg, stdin string, args ...string) (string, error) {
	r.t.Helper()
	_, combined, err := r.exec(cfg, stdin, args...)
	return combined, err
}

func (r *credAdminRig) must(cfg string, args ...string) string {
	r.t.Helper()
	out, err := r.run(cfg, "", args...)
	if err != nil {
		r.t.Fatalf("CLI %v: %v\n%s", args, err, out)
	}
	return out
}

// mustJSON runs a command that must succeed and decodes its stdout into v.
func (r *credAdminRig) mustJSON(cfg string, v any, args ...string) {
	r.t.Helper()
	stdout, combined, err := r.exec(cfg, "", args...)
	if err != nil {
		r.t.Fatalf("CLI %v: %v\n%s", args, err, combined)
	}
	if err := json.Unmarshal([]byte(stdout), v); err != nil {
		r.t.Fatalf("CLI %v did not print JSON on stdout: %v\n%s", args, err, combined)
	}
}

// createLogin stores a provider account through the CLI and returns its id.
// Every login here is an ANTHROPIC setup-token seat: a subscription login is
// stored on its shape alone, so no probe reaches a provider from the test.
func (r *credAdminRig) createLogin(cfgJSON, name string) string {
	r.t.Helper()
	var cred struct {
		ID string `json:"id"`
	}
	r.mustJSON(cfgJSON, &cred, "credential", "create", "--name", name, "--type", "PROVIDER_LOGIN", "--provider", "ANTHROPIC", "--mode", "subscription", "--value", "sk-ant-oat01-"+name)
	if cred.ID == "" {
		r.t.Fatalf("credential create printed no id for %s", name)
	}
	return cred.ID
}

// ── credential pool: ETag round trip ─────────────────────────────────────────

func TestAcceptance_CredentialPool_ETagRoundTrip(t *testing.T) {
	rig := startCredAdminServer(t)
	ownerJSON := rig.config(credAdminOwnerToken, "json")
	ownerTable := rig.config(credAdminOwnerToken, "table")
	memberTable := rig.config(credAdminMemberToken, "table")

	first := rig.createLogin(ownerJSON, "claude-seat-1")
	second := rig.createLogin(ownerJSON, "claude-seat-2")

	// create → revision 1, members echoed with their priorities.
	var created struct {
		Revision int64  `json:"revision"`
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Members  []struct {
			CredentialID string `json:"credential_id"`
			Priority     int    `json:"priority"`
		} `json:"members"`
	}
	rig.mustJSON(ownerJSON, &created, "credential", "pool", "create", "--name", "Team Claude", "--provider", "anthropic", "--mode", "subscription",
		"--member", first+"=10", "--member", second)
	if created.ID == "" || created.Revision != 1 || created.Provider != "ANTHROPIC" || len(created.Members) != 2 || created.Members[0].Priority != 10 {
		t.Fatalf("pool create = %+v, want revision 1, provider ANTHROPIC (canonicalised by the CLI) and both members", created)
	}

	// list → the table names the pool with its member count.
	out := rig.must(ownerTable, "credential", "pool", "list")
	if !strings.Contains(out, created.ID) || !strings.Contains(out, "Team Claude") {
		t.Errorf("pool list lacks the new pool:\n%s", out)
	}

	// get → the ETag the server sent is the revision the detail prints.
	out = rig.must(ownerTable, "credential", "pool", "get", created.ID)
	for _, want := range []string{"Revision", "1", first, second, "priority 10"} {
		if !strings.Contains(out, want) {
			t.Errorf("pool get lacks %q:\n%s", want, out)
		}
	}

	// update with that revision → 204, the CLI reports revision 2.
	out = rig.must(ownerTable, "credential", "pool", "update", created.ID, "--revision", "1", "--name", "Team Claude (EU)", "--member", second+"=5")
	if !strings.Contains(out, "2") || !strings.Contains(out, "updated") {
		t.Errorf("pool update should report revision 2:\n%s", out)
	}
	var after struct {
		Revision int64  `json:"revision"`
		Name     string `json:"name"`
		Members  []struct {
			CredentialID string `json:"credential_id"`
		} `json:"members"`
	}
	rig.mustJSON(ownerJSON, &after, "credential", "pool", "get", created.ID)
	if after.Revision != 2 || after.Name != "Team Claude (EU)" || len(after.Members) != 1 || after.Members[0].CredentialID != second {
		t.Errorf("pool after update = %+v, want revision 2, the new name and the replacement membership", after)
	}

	// the same stale revision again → 412 carried out as a non-zero exit.
	out, err := rig.run(ownerTable, "", "credential", "pool", "update", created.ID, "--revision", "1", "--name", "Stale", "--member", first)
	if err == nil || !strings.Contains(out, "reload") {
		t.Errorf("a stale --revision should fail with the server's 412 sentence; err=%v\n%s", err, out)
	}

	// delete needs the current revision; the retired pool is gone.
	out, err = rig.run(ownerTable, "", "credential", "pool", "delete", created.ID, "--revision", "1")
	if err == nil {
		t.Errorf("delete with a stale revision should be refused:\n%s", out)
	}
	rig.must(ownerTable, "credential", "pool", "delete", created.ID, "--revision", "2")
	if out, err = rig.run(ownerTable, "", "credential", "pool", "get", created.ID); err == nil || !strings.Contains(out, "not found") {
		t.Errorf("a retired pool should be a 404; err=%v\n%s", err, out)
	}
	if out = rig.must(ownerTable, "credential", "pool", "list"); strings.Contains(out, created.ID) {
		t.Errorf("a retired pool should not be listed:\n%s", out)
	}

	// Pool definitions are OWNER/ADMIN-only even to read.
	if out, err = rig.run(memberTable, "", "credential", "pool", "list"); err == nil {
		t.Errorf("a MEMBER should not list pools:\n%s", out)
	}
}

// ── escalation supply: stdin value → vault, grant, resolved ──────────────────

func TestAcceptance_EscalationSupply_StdinValueGrantsAgent(t *testing.T) {
	rig := startCredAdminServer(t)
	ownerTable := rig.config(credAdminOwnerToken, "table")
	db := rig.db.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode) VALUES ('cadm-crew', ?, 'Ops', 'ops', 'free')`, credAdminWorkspaceID)
	// The agent is owned by the MEMBER so the OWNER supplying is never the
	// initiator, whatever the four-eyes default becomes.
	mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, created_by_user_id) VALUES ('cadm-agent', 'cadm-crew', ?, 'Reporter', 'reporter', 'cadm-member')`, credAdminWorkspaceID)
	mustExec(`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('cadm-chat', 'cadm-agent', ?, 'CHAT', 'ACTIVE')`, credAdminWorkspaceID)
	// The staged row an ask leaves behind: REQUESTED, handle-only, no value.
	mustExec(`INSERT INTO credentials (id, workspace_id, name, description, encrypted_value, type, provider, scope, security_level, status,
			created_by, created_at, updated_at, created_by_actor_type, created_by_actor_id, handle_only)
		VALUES ('cadm-cred', ?, 'PG_PASSWORD', 'read the orders table', 'v1:placeholder', 'SECRET', 'NONE', 'WORKSPACE', 2, 'REQUESTED',
			'cadm-member', ?, ?, 'agent', 'cadm-agent', 1)`, credAdminWorkspaceID, now, now)
	mustExec(`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, metadata, credential_id, status, created_at)
		VALUES ('cadm-esc', ?, 'cadm-crew', 'cadm-chat', 'cadm-agent', 'need the postgres password', 'CREDENTIAL',
			'{"name":"PG_PASSWORD","type":"SECRET","requested":true}', 'cadm-cred', 'PENDING', ?)`, credAdminWorkspaceID, now)

	// No value on stdin → refused before any request.
	out, err := rig.run(ownerTable, "", "escalation", "supply", "cadm-esc")
	if err == nil || !strings.Contains(out, "stdin") {
		t.Fatalf("supply without a value should fail naming stdin; err=%v\n%s", err, out)
	}

	out, err = rig.run(ownerTable, "s3cret-pg-pass\n", "escalation", "supply", "cadm-esc", "--security-level", "3")
	if err != nil {
		t.Fatalf("escalation supply: %v\n%s", err, out)
	}
	for _, want := range []string{"cadm-esc", "PG_PASSWORD", "handle-only", "L3"} {
		if !strings.Contains(out, want) {
			t.Errorf("receipt lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cret") {
		t.Errorf("the value must never be echoed:\n%s", out)
	}

	var status string
	var handleOnly, level int
	if err := db.QueryRow(`SELECT status, handle_only, security_level FROM credentials WHERE id = 'cadm-cred'`).Scan(&status, &handleOnly, &level); err != nil {
		t.Fatal(err)
	}
	if status != "ACTIVE" || handleOnly != 1 || level != 3 {
		t.Errorf("credential after supply = %s/handle_only=%d/L%d, want ACTIVE, handle-only, L3 (the --security-level override)", status, handleOnly, level)
	}
	var grants int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = 'cadm-agent' AND credential_id = 'cadm-cred'`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 1 {
		t.Errorf("grants to the asking agent = %d, want 1", grants)
	}
	var escStatus, action string
	if err := db.QueryRow(`SELECT status, COALESCE(action, '') FROM escalations WHERE id = 'cadm-esc'`).Scan(&escStatus, &action); err != nil {
		t.Fatal(err)
	}
	if escStatus != "RESOLVED" || action != "approve" {
		t.Errorf("escalation = %s/%s, want RESOLVED/approve", escStatus, action)
	}

	// A second answer finds nothing pending: 409 out as a non-zero exit.
	if out, err = rig.run(ownerTable, "again\n", "escalation", "supply", "cadm-esc"); err == nil {
		t.Errorf("supplying a resolved escalation should be refused:\n%s", out)
	}
}

// ── credential refresh ────────────────────────────────────────────────────────

// codexAccessJWT is an unsigned ChatGPT access token carrying the claims the
// login reader looks at (plan, account, expiry) — the server never verifies
// the signature, only reads the payload.
func codexAccessJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{
		"exp":                         float64(exp.Unix()),
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-1"},
	})
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

// codexAuthJSON is the ~/.codex/auth.json shape `codex login` writes.
func codexAuthJSON(access string) string {
	return `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id.tok.en","access_token":"` + access +
		`","refresh_token":"rt.REAL-SECRET","account_id":"acct-1"},"last_refresh":"2026-08-29T20:31:08Z"}`
}

// acceptanceTokens is the provider's token endpoint, faked: it hands back a
// rotated pair and records what it was given.
type acceptanceTokens struct {
	gotOld string
	next   providerlogin.RefreshResult
}

func (f *acceptanceTokens) Refresh(_ context.Context, refreshToken string) (providerlogin.RefreshResult, error) {
	f.gotOld = refreshToken
	return f.next, nil
}

func TestAcceptance_CredentialRefresh_RenewsThroughProvider(t *testing.T) {
	rig := startCredAdminServer(t)
	ownerJSON := rig.config(credAdminOwnerToken, "json")
	ownerTable := rig.config(credAdminOwnerToken, "table")
	memberTable := rig.config(credAdminMemberToken, "table")

	access := codexAccessJWT(t, time.Now().Add(2*time.Hour))
	tokens := &acceptanceTokens{next: providerlogin.RefreshResult{
		AccessToken: codexAccessJWT(t, time.Now().Add(24*time.Hour)), RefreshToken: "rt.ROTATED", IDToken: "id.new",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}}
	rig.router.LoginRefresher().SetTokenRefresher("OPENAI", tokens)

	var seatRow struct {
		ID string `json:"id"`
	}
	rig.mustJSON(ownerJSON, &seatRow, "credential", "create", "--name", "chatgpt-seat", "--type", "PROVIDER_LOGIN", "--provider", "OPENAI", "--mode", "subscription", "--value", codexAuthJSON(access))
	seat := seatRow.ID
	// A Claude setup-token is a subscription seat with no refresh flow.
	setupToken := rig.createLogin(ownerJSON, "claude-seat")

	out := rig.must(ownerTable, "credential", "refresh", "chatgpt-seat")
	if !strings.Contains(out, "Login refreshed: "+seat) || !strings.Contains(out, "Refresh:") {
		t.Errorf("refresh should report the renewed login:\n%s", out)
	}
	if tokens.gotOld != "rt.REAL-SECRET" {
		t.Errorf("the provider was handed %q, want the stored refresh token", tokens.gotOld)
	}
	var refreshStatus string
	if err := rig.db.DB.QueryRow(`SELECT status FROM provider_login_refresh WHERE credential_id = ?`, seat).Scan(&refreshStatus); err != nil || refreshStatus != "ok" {
		t.Errorf("refresh state after the forced refresh = %q (%v), want ok", refreshStatus, err)
	}

	// A setup-token has no refresh flow: the server's 400 comes out verbatim.
	out, err := rig.run(ownerTable, "", "credential", "refresh", setupToken)
	if err == nil || !strings.Contains(out, "no refresh flow") {
		t.Errorf("refreshing a setup-token login should fail with the server's sentence; err=%v\n%s", err, out)
	}

	// A MEMBER cannot see a provider account at all (#2439): 404, not 403.
	out, err = rig.run(memberTable, "", "credential", "refresh", seat)
	if err == nil || !strings.Contains(out, "not found") {
		t.Errorf("a MEMBER should get 'not found' for a provider account; err=%v\n%s", err, out)
	}
}

// ── credential list --kind provider_login ────────────────────────────────────

func TestAcceptance_CredentialList_KindProviderLogin(t *testing.T) {
	rig := startCredAdminServer(t)
	ownerJSON := rig.config(credAdminOwnerToken, "json")
	ownerTable := rig.config(credAdminOwnerToken, "table")
	memberJSON := rig.config(credAdminMemberToken, "json")

	rig.must(ownerJSON, "credential", "create", "--name", "GH_TOKEN", "--type", "SECRET", "--value", "ghp_plain")
	seat := rig.createLogin(ownerJSON, "claude-seat")

	ids := func(cfg string, args ...string) []string {
		t.Helper()
		var rows []struct {
			ID string `json:"id"`
		}
		rig.mustJSON(cfg, &rows, args...)
		var got []string
		for _, r := range rows {
			got = append(got, r.ID)
		}
		return got
	}

	if got := ids(ownerJSON, "credential", "list", "--kind", "provider_login"); len(got) != 1 || got[0] != seat {
		t.Errorf("--kind provider_login listed %v, want only the seat %s", got, seat)
	}
	if got := ids(ownerJSON, "credential", "list"); len(got) != 2 {
		t.Errorf("an unfiltered list should carry both rows, got %v", got)
	}
	out := rig.must(ownerTable, "credential", "list", "--kind", "provider_login")
	for _, want := range []string{"PROVIDER", "MODE", "subscription", "claude-seat"} {
		if !strings.Contains(out, want) {
			t.Errorf("the provider-login table lacks %q:\n%s", want, out)
		}
	}

	// Any other kind is refused by the CLI's own validation.
	if out, err := rig.run(ownerTable, "", "credential", "list", "--kind", "secret"); err == nil || !strings.Contains(out, "provider_login") {
		t.Errorf("--kind secret should be a validation error; err=%v\n%s", err, out)
	}

	// Provider accounts are invisible below OWNER/ADMIN (#2439): the MEMBER's
	// list has the secret and not the seat, filtered or not.
	if got := ids(memberJSON, "credential", "list", "--kind", "provider_login"); len(got) != 0 {
		t.Errorf("a MEMBER should see no provider logins, got %v", got)
	}
	if got := ids(memberJSON, "credential", "list"); len(got) != 1 || got[0] == seat {
		t.Errorf("a MEMBER's list = %v, want the workspace secret only", got)
	}
}
