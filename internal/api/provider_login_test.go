package api

// Provider logins — docs/prd/provider-logins.md §10 (#2428). The type, the
// login object on credential rows, the refresh state machine and the
// pays_with derivation. No test here dials a provider: the token endpoint is
// a fake behind providerlogin.TokenRefresher.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

func plFakeJWT(t *testing.T, plan string, exp time.Time) string {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{
		"exp": float64(exp.Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":  plan,
			"chatgpt_account_id": "acct-1",
		},
	})
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

func plCodexAuthJSON(t *testing.T, access string) string {
	t.Helper()
	return `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id.tok.en","access_token":"` + access +
		`","refresh_token":"rt.REAL-SECRET","account_id":"acct-1"},"last_refresh":"2026-08-29T20:31:08Z"}`
}

func plRequest(t *testing.T, method, path, body, userID, wsID, role string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	return req.WithContext(ctx)
}

func plCreate(t *testing.T, h *CredentialHandler, userID, wsID string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	h.Create(rr, plRequest(t, "POST", "/api/v1/credentials", string(raw), userID, wsID, "OWNER"))
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func plDecryptColumn(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var enc string
	if err := db.QueryRow(query, args...).Scan(&enc); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	dec, err := encryption.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	return dec
}

func TestProviderLogin_Create_SplitsCodexAuthJSON(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	exp := time.Now().Add(200 * time.Hour).UTC().Truncate(time.Second)
	access := plFakeJWT(t, "plus", exp)

	code, out := plCreate(t, h, userID, wsID, map[string]any{
		"name": "ChatGPT Plus · jana", "type": "PROVIDER_LOGIN", "provider": "OPENAI",
		"mode": "subscription", "value": plCodexAuthJSON(t, access),
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	credID, _ := out["id"].(string)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "rt.REAL-SECRET") || strings.Contains(string(raw), access) {
		t.Fatalf("create response leaks login material: %s", raw)
	}

	// The value column holds the bare access token, not the file.
	if got := plDecryptColumn(t, db, `SELECT encrypted_value FROM credentials WHERE id = ?`, credID); got != access {
		t.Errorf("encrypted_value = %q, want the access token", got)
	}
	// The parts.
	type part struct {
		secret bool
		value  string
	}
	parts := map[string]part{}
	rows, err := db.Query(`SELECT key, is_secret, COALESCE(value,''), COALESCE(encrypted_value,'') FROM credential_fields WHERE credential_id = ?`, credID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key, val, enc string
		var secret int
		if err := rows.Scan(&key, &secret, &val, &enc); err != nil {
			t.Fatal(err)
		}
		p := part{secret: secret == 1, value: val}
		if p.secret {
			dec, err := encryption.Decrypt(enc)
			if err != nil {
				t.Fatalf("decrypt part %s: %v", key, err)
			}
			p.value = dec
		}
		parts[key] = p
	}
	rows.Close()
	want := map[string]part{
		"refresh_token": {true, "rt.REAL-SECRET"},
		"id_token":      {true, "id.tok.en"},
		"account_id":    {false, "acct-1"},
		"plan":          {false, "plus"},
		"expires_at":    {false, exp.Format(time.RFC3339)},
		"mode":          {false, "subscription"},
	}
	for k, w := range want {
		if got, ok := parts[k]; !ok || got != w {
			t.Errorf("part %s = %+v (present=%v), want %+v", k, got, ok, w)
		}
	}
	if len(parts) != len(want) {
		t.Errorf("parts = %v, want exactly %d", parts, len(want))
	}

	// The row is SEALED (a refresh token lives on it) and its expiry column
	// mirrors the access token's so the EXPIRING badge keeps working.
	var sensitivity, tokenExp string
	if err := db.QueryRow(`SELECT sensitivity, COALESCE(token_expires_at,'') FROM credentials WHERE id = ?`, credID).Scan(&sensitivity, &tokenExp); err != nil {
		t.Fatal(err)
	}
	if sensitivity != SensitivitySealed {
		t.Errorf("sensitivity = %q, want SEALED", sensitivity)
	}
	if tokenExp != exp.Format(time.RFC3339) {
		t.Errorf("token_expires_at = %q, want %s", tokenExp, exp.Format(time.RFC3339))
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM provider_login_refresh WHERE credential_id = ?`, credID).Scan(&status); err != nil || status != "ok" {
		t.Errorf("refresh state = %q, %v; want ok", status, err)
	}

	// The fields surface never returns a secret part's value.
	fh := NewCredentialFieldHandler(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	req := plRequest(t, "GET", "/api/v1/credentials/"+credID+"/fields", "", userID, wsID, "OWNER")
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	fh.List(rr, req)
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "rt.REAL-SECRET") || strings.Contains(rr.Body.String(), "id.tok.en") {
		t.Errorf("fields list leaks a secret part: %d %s", rr.Code, rr.Body.String())
	}

	// The login object on GET.
	getReq := plRequest(t, "GET", "/api/v1/credentials/"+credID, "", userID, wsID, "OWNER")
	getReq.SetPathValue("credentialId", credID)
	rr = httptest.NewRecorder()
	h.Get(rr, getReq)
	var got struct {
		Login *loginView `json:"login"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got.Login == nil {
		t.Fatalf("GET login = %v (%d %s)", err, rr.Code, rr.Body.String())
	}
	l := got.Login
	if l.Mode != "subscription" || l.Provider != "OPENAI" || l.Plan == nil || *l.Plan != "plus" || l.PlanLabel == nil || *l.PlanLabel != "ChatGPT Plus" {
		t.Errorf("login = %+v", l)
	}
	if l.OwnerUserID == nil || *l.OwnerUserID != userID || l.OwnerEmail == nil || *l.OwnerEmail != "test@example.com" {
		t.Errorf("owner = %v / %v", l.OwnerUserID, l.OwnerEmail)
	}
	if l.ExpiresAt == nil || *l.ExpiresAt != exp.Format(time.RFC3339) {
		t.Errorf("expires_at = %v", l.ExpiresAt)
	}
	if !l.Refresh.Supported || l.Refresh.Status != "ok" || l.Refresh.NextAt == nil {
		t.Errorf("refresh = %+v", l.Refresh)
	}
	if l.Delivery.Kind != "file" || l.Delivery.Target != ".codex/auth.json" {
		t.Errorf("delivery = %+v", l.Delivery)
	}
	if l.PaysFor.Agents != 0 || l.PaysFor.Crews != 0 {
		t.Errorf("pays_for = %+v, want zeros", l.PaysFor)
	}
}

func TestProviderLogin_Create_AnthropicSetupToken(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	code, out := plCreate(t, h, userID, wsID, map[string]any{
		"name": "Claude Max · pavel", "type": "PROVIDER_LOGIN", "provider": "anthropic", "value": "sk-ant-oat01-abc",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	credID := out["id"].(string)
	var sensitivity string
	var mode string
	if err := db.QueryRow(`SELECT c.sensitivity, f.value FROM credentials c JOIN credential_fields f ON f.credential_id = c.id AND f.key = 'mode' WHERE c.id = ?`, credID).Scan(&sensitivity, &mode); err != nil {
		t.Fatal(err)
	}
	if sensitivity != SensitivityStandard || mode != "subscription" {
		t.Errorf("sensitivity=%q mode=%q; a setup-token has no refresh token to seal and infers subscription", sensitivity, mode)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM provider_login_refresh WHERE credential_id = ?`, credID).Scan(&n)
	if n != 0 {
		t.Errorf("a login with no refresh flow must not get refresh state")
	}
	login, ok := out["login"].(map[string]any)
	if !ok {
		t.Fatalf("create response has no login object: %v", out)
	}
	refresh := login["refresh"].(map[string]any)
	if login["mode"] != "subscription" || refresh["supported"] != false || refresh["status"] != "none" {
		t.Errorf("login = %v", login)
	}
	if d := login["delivery"].(map[string]any); d["kind"] != "env" || d["target"] != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("delivery = %v", d)
	}
}

func TestProviderLogin_Create_Validation(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"not a model provider", map[string]any{"name": "a", "type": "PROVIDER_LOGIN", "provider": "GITHUB", "value": "ghp_x"}, "not a model provider"},
		{"openai subscription needs the file", map[string]any{"name": "b", "type": "PROVIDER_LOGIN", "provider": "OPENAI", "mode": "subscription", "value": "sk-proj-x"}, "auth.json"},
		{"bad mode", map[string]any{"name": "c", "type": "PROVIDER_LOGIN", "provider": "OPENAI", "mode": "seat", "value": "sk-proj-x"}, "mode must be"},
		{"google subscription unsupported", map[string]any{"name": "d", "type": "PROVIDER_LOGIN", "provider": "GOOGLE", "mode": "subscription", "value": "{}"}, "not supported"},
	}
	for _, c := range cases {
		code, out := plCreate(t, h, userID, wsID, c.body)
		msg, _ := out["error"].(string)
		if code != http.StatusBadRequest || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %d %q, want 400 containing %q", c.name, code, msg, c.want)
		}
	}
}

// seedLegacyLogin writes an AI_CLI_TOKEN / API_KEY row the way the install
// base has them: no parts, no refresh state.
func seedLegacyLogin(t *testing.T, db *sql.DB, wsID, userID, credID, name, credType, provider, plain string) {
	t.Helper()
	seedCredentialEnc(t, db, wsID, userID, credID, name, plain)
	execOrFatal(t, db, `UPDATE credentials SET type = ?, provider = ? WHERE id = ?`, credType, provider, credID)
}

func TestProviderLogin_List_KindFilterAndDerivedLogin(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	access := plFakeJWT(t, "pro", time.Now().Add(100*time.Hour))
	code, out := plCreate(t, h, userID, wsID, map[string]any{
		"name": "codex-login", "type": "PROVIDER_LOGIN", "provider": "OPENAI", "value": plCodexAuthJSON(t, access),
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	loginID := out["id"].(string)
	seedLegacyLogin(t, db, wsID, userID, "leg-claude", "claude-token", "AI_CLI_TOKEN", "ANTHROPIC", "sk-ant-oat01-legacy")
	seedLegacyLogin(t, db, wsID, userID, "leg-openai", "openai-key", "API_KEY", "OPENAI", "sk-proj-legacy")
	seedLegacyLogin(t, db, wsID, userID, "leg-codex", "codex-blob", "AI_CLI_TOKEN", "OPENAI", plCodexAuthJSON(t, plFakeJWT(t, "team", time.Now().Add(50*time.Hour))))
	seedCredentialEnc(t, db, wsID, userID, "gh", "github", "ghp_x") // SECRET/GITHUB: no login

	// Who the codex login pays for: one explicit grant, one crew binding to
	// a crew of two agents, one workspace binding on the legacy key.
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, cli_adapter) VALUES ('ag-1', ?, 'crew-1', 'A', 'a', 'CODEX_CLI')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, cli_adapter) VALUES ('ag-2', ?, 'crew-1', 'B', 'b', 'CODEX_CLI')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, name, slug, cli_adapter) VALUES ('ag-3', ?, 'C', 'c', 'CLAUDE_CODE')`, wsID)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name) VALUES ('ac-1', 'ag-3', ?, 'OPENAI_API_KEY')`, loginID)
	execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, crew_id, agent_id, slot) VALUES ('b-1', ?, ?, 'CREW', 'crew-1', NULL, 'OPENAI_API_KEY')`, wsID, loginID)
	execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, crew_id, agent_id, slot) VALUES ('b-2', ?, 'leg-openai', 'WORKSPACE', NULL, NULL, 'OPENAI_KEY')`, wsID)

	list := func(query string) []map[string]any {
		t.Helper()
		rr := httptest.NewRecorder()
		h.List(rr, plRequest(t, "GET", "/api/v1/credentials"+query, "", userID, wsID, "OWNER"))
		if rr.Code != 200 {
			t.Fatalf("list %s: %d %s", query, rr.Code, rr.Body.String())
		}
		var rows []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	all := list("")
	byID := map[string]map[string]any{}
	for _, r := range all {
		byID[r["id"].(string)] = r
	}
	if len(all) != 5 {
		t.Fatalf("plain list = %d rows, want 5", len(all))
	}
	if _, has := byID["gh"]["login"]; has {
		t.Errorf("a GitHub secret must carry no login object: %v", byID["gh"])
	}
	loginOf := func(id string) map[string]any {
		t.Helper()
		l, ok := byID[id]["login"].(map[string]any)
		if !ok {
			t.Fatalf("row %s has no login: %v", id, byID[id])
		}
		return l
	}
	l := loginOf(loginID)
	pf := l["pays_for"].(map[string]any)
	if pf["agents"].(float64) != 3 || pf["crews"].(float64) != 1 {
		t.Errorf("pays_for = %v, want agents=3 (grant + two crew members) crews=1", pf)
	}
	if l["plan"] != "pro" || l["plan_label"] != "ChatGPT Pro" || l["refresh"].(map[string]any)["supported"] != true {
		t.Errorf("login = %v", l)
	}
	if l := loginOf("leg-claude"); l["mode"] != "subscription" || l["provider"] != "ANTHROPIC" ||
		l["refresh"].(map[string]any)["supported"] != false || l["refresh"].(map[string]any)["status"] != "none" ||
		l["delivery"].(map[string]any)["target"] != "CLAUDE_CODE_OAUTH_TOKEN" || l["plan"] != nil {
		t.Errorf("legacy claude login = %v", l)
	}
	if l := loginOf("leg-openai"); l["mode"] != "api_key" || l["pays_for"].(map[string]any)["agents"].(float64) != 3 {
		t.Errorf("legacy openai key = %v (a WORKSPACE binding pays for every agent)", l)
	}
	if l := loginOf("leg-codex"); l["mode"] != "subscription" || l["plan"] != "team" || l["expires_at"] == nil ||
		l["delivery"].(map[string]any)["kind"] != "file" || l["refresh"].(map[string]any)["supported"] != false {
		t.Errorf("legacy codex blob = %v (plan and expiry read from the token, no refresh)", l)
	}

	only := list("?kind=provider_login")
	if len(only) != 4 {
		t.Errorf("kind=provider_login = %d rows, want 4", len(only))
	}
	for _, r := range only {
		if _, has := r["login"]; !has {
			t.Errorf("row %s in the login list has no login object", r["id"])
		}
	}
	// The cursor envelope carries the same filter and the same objects.
	rr := httptest.NewRecorder()
	h.List(rr, plRequest(t, "GET", "/api/v1/credentials?kind=provider_login&paginate=true", "", userID, wsID, "OWNER"))
	var page struct {
		Credentials []map[string]any `json:"credentials"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil || len(page.Credentials) != 4 {
		t.Errorf("paginated login list: err=%v rows=%d (%d %s)", err, len(page.Credentials), rr.Code, rr.Body.String())
	}
	// An unknown kind is refused rather than answered with everything.
	rr = httptest.NewRecorder()
	h.List(rr, plRequest(t, "GET", "/api/v1/credentials?kind=secret", "", userID, wsID, "OWNER"))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("kind=secret: %d, want 400", rr.Code)
	}
}

// fakeTokens is the TokenRefresher the state-machine tests drive.
type fakeTokens struct {
	mu      sync.Mutex
	calls   int
	gotOld  []string
	next    providerlogin.RefreshResult
	err     error
	block   chan struct{} // when non-nil, Refresh waits on it
	started chan struct{}
}

func (f *fakeTokens) Refresh(ctx context.Context, refreshToken string) (providerlogin.RefreshResult, error) {
	f.mu.Lock()
	f.calls++
	f.gotOld = append(f.gotOld, refreshToken)
	block, started := f.block, f.started
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		<-block
	}
	if f.err != nil {
		return providerlogin.RefreshResult{}, f.err
	}
	return f.next, nil
}

type plRig struct {
	h      *CredentialHandler
	db     *sql.DB
	userID string
	wsID   string
	tokens *fakeTokens
	rf     *ProviderLoginRefresher
	execs  []provider.ExecConfig
}

func newPLRig(t *testing.T) *plRig {
	t.Helper()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	r := &plRig{h: h, db: db, userID: userID, wsID: wsID, tokens: &fakeTokens{}}
	ctr := newRecordingCtr(&r.execs, nil)
	r.rf = NewProviderLoginRefresher(db, h.logger, ctr)
	r.rf.SetTokenRefresher("OPENAI", r.tokens)
	h.SetLoginRefresher(r.rf)
	h.SetContainer(ctr)
	return r
}

func (r *plRig) seedCodexLogin(t *testing.T, name string, expiresIn time.Duration) string {
	t.Helper()
	access := plFakeJWT(t, "plus", time.Now().Add(expiresIn))
	code, out := plCreate(t, r.h, r.userID, r.wsID, map[string]any{
		"name": name, "type": "PROVIDER_LOGIN", "provider": "OPENAI", "value": plCodexAuthJSON(t, access),
	})
	if code != http.StatusCreated {
		t.Fatalf("seed login: %d %v", code, out)
	}
	return out["id"].(string)
}

func (r *plRig) refreshState(t *testing.T, credID string) (status string, failures int, errMsg string) {
	t.Helper()
	var e sql.NullString
	if err := r.db.QueryRow(`SELECT status, failures, error FROM provider_login_refresh WHERE credential_id = ?`, credID).Scan(&status, &failures, &e); err != nil {
		t.Fatalf("refresh state: %v", err)
	}
	return status, failures, e.String
}

func TestProviderLogin_RefreshEndpoint_RotatesTheLogin(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	credID := r.seedCodexLogin(t, "codex", 200*time.Hour)
	newExp := time.Now().Add(240 * time.Hour).UTC().Truncate(time.Second)
	newAccess := plFakeJWT(t, "plus", newExp)
	r.tokens.next = providerlogin.RefreshResult{AccessToken: newAccess, RefreshToken: "rt.NEW", IDToken: "id.new", ExpiresAt: newExp}

	req := plRequest(t, "POST", "/api/v1/credentials/"+credID+"/refresh", "", r.userID, r.wsID, "OWNER")
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	r.h.Refresh(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "rt.NEW") || strings.Contains(rr.Body.String(), newAccess) {
		t.Fatalf("refresh response leaks material: %s", rr.Body.String())
	}
	var out struct {
		Login loginView `json:"login"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Login.Refresh.Status != "ok" || out.Login.Refresh.LastAt == nil || out.Login.ExpiresAt == nil || *out.Login.ExpiresAt != newExp.Format(time.RFC3339) {
		t.Errorf("login after refresh = %+v", out.Login)
	}
	if got := r.tokens.gotOld; len(got) != 1 || got[0] != "rt.REAL-SECRET" {
		t.Errorf("token endpoint was given %v, want the stored refresh token once", got)
	}
	if got := plDecryptColumn(t, r.db, `SELECT encrypted_value FROM credentials WHERE id = ?`, credID); got != newAccess {
		t.Errorf("access token not rotated")
	}
	if got := plDecryptColumn(t, r.db, `SELECT encrypted_value FROM credential_fields WHERE credential_id = ? AND key = 'refresh_token'`, credID); got != "rt.NEW" {
		t.Errorf("refresh token not rotated: %q", got)
	}
	if got := plDecryptColumn(t, r.db, `SELECT encrypted_value FROM credential_fields WHERE credential_id = ? AND key = 'id_token'`, credID); got != "id.new" {
		t.Errorf("id token not rotated: %q", got)
	}
	var expPart, tokenExp string
	_ = r.db.QueryRow(`SELECT value FROM credential_fields WHERE credential_id = ? AND key = 'expires_at'`, credID).Scan(&expPart)
	_ = r.db.QueryRow(`SELECT token_expires_at FROM credentials WHERE id = ?`, credID).Scan(&tokenExp)
	if expPart != newExp.Format(time.RFC3339) || tokenExp != newExp.Format(time.RFC3339) {
		t.Errorf("expiry not updated: part=%q column=%q", expPart, tokenExp)
	}
	var audits int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id = ? AND event_type = 'REFRESH'`, credID).Scan(&audits)
	if audits != 1 {
		t.Errorf("audit REFRESH rows = %d, want 1", audits)
	}
}

func TestProviderLogin_RefreshEndpoint_ConflictWhileInFlight(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	credID := r.seedCodexLogin(t, "codex", 200*time.Hour)
	// Another process holds the single-flight claim.
	execOrFatal(t, r.db, `UPDATE provider_login_refresh SET in_progress_until = ? WHERE credential_id = ?`,
		time.Now().Add(time.Minute).UTC().Format(time.RFC3339), credID)
	req := plRequest(t, "POST", "/api/v1/credentials/"+credID+"/refresh", "", r.userID, r.wsID, "OWNER")
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	r.h.Refresh(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("refresh while in flight: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if r.tokens.calls != 0 {
		t.Errorf("token endpoint called %d times during another's refresh", r.tokens.calls)
	}
}

func TestProviderLogin_RefreshEndpoint_UnsupportedAndRoles(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	code, out := plCreate(t, r.h, r.userID, r.wsID, map[string]any{
		"name": "claude", "type": "PROVIDER_LOGIN", "provider": "ANTHROPIC", "value": "sk-ant-oat01-x",
	})
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	credID := out["id"].(string)
	req := plRequest(t, "POST", "/api/v1/credentials/"+credID+"/refresh", "", r.userID, r.wsID, "OWNER")
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	r.h.Refresh(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("refresh of a setup-token: %d, want 400 (no refresh flow)", rr.Code)
	}
	req = plRequest(t, "POST", "/api/v1/credentials/"+credID+"/refresh", "", r.userID, r.wsID, "VIEWER")
	req.SetPathValue("credentialId", credID)
	rr = httptest.NewRecorder()
	r.h.Refresh(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer refresh: %d, want 403", rr.Code)
	}
}

func TestProviderLoginRefresher_FailuresEscalateToNeedsRelogin(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	credID := r.seedCodexLogin(t, "codex", 10*time.Hour)
	r.tokens.err = errors.New("token endpoint returned 503")

	for i := 1; i <= providerlogin.MaxFailures; i++ {
		if _, err := r.rf.Refresh(context.Background(), credID, true); err == nil {
			t.Fatalf("attempt %d: expected an error", i)
		}
		status, failures, msg := r.refreshState(t, credID)
		wantStatus := "failed"
		if i == providerlogin.MaxFailures {
			wantStatus = "needs_relogin"
		}
		if status != wantStatus || failures != i || !strings.Contains(msg, "503") {
			t.Errorf("after failure %d: status=%q failures=%d error=%q", i, status, failures, msg)
		}
	}
	// The owner was told, once, through the inbox.
	var n int
	var target, kind string
	if err := r.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(target_user_id),''), COALESCE(MAX(kind),'') FROM inbox_items WHERE source_id = ?`,
		"provider-login-relogin:"+credID).Scan(&n, &target, &kind); err != nil {
		t.Fatal(err)
	}
	if n != 1 || target != r.userID || kind != "message" {
		t.Errorf("inbox notification: n=%d target=%q kind=%q", n, target, kind)
	}
	// needs_relogin is terminal for the scheduler: the due scan leaves it.
	before := r.tokens.calls
	r.rf.RefreshDue(context.Background())
	if r.tokens.calls != before {
		t.Errorf("RefreshDue retried a needs_relogin login")
	}
	// The credential row itself stays ACTIVE and visible; only the login's
	// refresh status carries the verdict.
	var status string
	_ = r.db.QueryRow(`SELECT status FROM credentials WHERE id = ?`, credID).Scan(&status)
	if status != "ACTIVE" {
		t.Errorf("credential status = %q, want ACTIVE", status)
	}
}

func TestProviderLoginRefresher_PermanentErrorNeedsReloginAtOnce(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	credID := r.seedCodexLogin(t, "codex", 10*time.Hour)
	rf := providerlogin.NewOpenAIRefresher(nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	rf.TokenURL = srv.URL
	rf.Client = srv.Client()
	r.rf.SetTokenRefresher("OPENAI", rf)
	if _, err := r.rf.Refresh(context.Background(), credID, true); err == nil {
		t.Fatal("expected an error")
	}
	if status, _, msg := r.refreshState(t, credID); status != "needs_relogin" || !strings.Contains(msg, "invalid_grant") {
		t.Errorf("status=%q error=%q; an invalid_grant needs a re-login now, not after three tries", status, msg)
	}
}

func TestProviderLoginRefresher_BackoffAndDueScan(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	soon := r.seedCodexLogin(t, "soon", 10*time.Hour)    // inside the 24 h lead
	later := r.seedCodexLogin(t, "later", 200*time.Hour) // not due
	r.tokens.next = providerlogin.RefreshResult{AccessToken: plFakeJWT(t, "plus", time.Now().Add(240*time.Hour)), RefreshToken: "rt.NEW"}

	r.rf.RefreshDue(context.Background())
	if r.tokens.calls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1 (only the due login)", r.tokens.calls)
	}
	if status, _, _ := r.refreshState(t, soon); status != "ok" {
		t.Errorf("soon: status = %q", status)
	}
	if status, _, _ := r.refreshState(t, later); status != "ok" {
		t.Errorf("later: status = %q", status)
	}

	// A failure sets the 5-minute backoff: the next due scan skips it, a
	// forced refresh does not.
	third := r.seedCodexLogin(t, "third", 5*time.Hour)
	r.tokens.err = errors.New("token endpoint returned 502")
	r.rf.RefreshDue(context.Background())
	if status, failures, _ := r.refreshState(t, third); status != "failed" || failures != 1 {
		t.Fatalf("third after first failure: %q/%d", status, failures)
	}
	calls := r.tokens.calls
	r.rf.RefreshDue(context.Background())
	if r.tokens.calls != calls {
		t.Errorf("due scan ignored the failure backoff")
	}
	var nextAt string
	_ = r.db.QueryRow(`SELECT next_at FROM provider_login_refresh WHERE credential_id = ?`, third).Scan(&nextAt)
	if next, err := time.Parse(time.RFC3339, nextAt); err != nil || time.Until(next) < 4*time.Minute {
		t.Errorf("next_at = %q, want ~5 min out", nextAt)
	}
}

func TestProviderLoginRefresher_SingleFlight(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	credID := r.seedCodexLogin(t, "codex", 10*time.Hour)
	r.tokens.block = make(chan struct{})
	r.tokens.started = make(chan struct{}, 1)
	r.tokens.next = providerlogin.RefreshResult{AccessToken: plFakeJWT(t, "plus", time.Now().Add(240*time.Hour)), RefreshToken: "rt.NEW"}

	done := make(chan error, 1)
	go func() {
		_, err := r.rf.Refresh(context.Background(), credID, true)
		done <- err
	}()
	<-r.tokens.started
	// While the first refresh holds the claim, a second is refused without
	// touching the endpoint.
	if _, err := r.rf.Refresh(context.Background(), credID, true); !errors.Is(err, ErrRefreshInFlight) {
		t.Errorf("second refresh err = %v, want ErrRefreshInFlight", err)
	}
	close(r.tokens.block)
	if err := <-done; err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if r.tokens.calls != 1 {
		t.Errorf("token endpoint calls = %d, want 1", r.tokens.calls)
	}
}

// Before a run starts, a login with < 48 h left is refreshed and the
// delivered set carries the NEW access token.
func TestProviderLogin_RunStartRefreshesTheDeliveredValue(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	credID := r.seedCodexLogin(t, "codex", 40*time.Hour)
	newAccess := plFakeJWT(t, "plus", time.Now().Add(240*time.Hour))
	r.tokens.next = providerlogin.RefreshResult{AccessToken: newAccess, RefreshToken: "rt.NEW"}
	execOrFatal(t, r.db, `INSERT INTO agents (id, workspace_id, name, slug, cli_adapter) VALUES ('ag-1', ?, 'A', 'a', 'CODEX_CLI')`, r.wsID)
	execOrFatal(t, r.db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name) VALUES ('ac-1', 'ag-1', ?, 'OPENAI_API_KEY')`, credID)

	restore := SetRunStartLoginRefresherForTesting(r.rf)
	defer restore()
	delivered, _, err := loadDeliveredCredentials(context.Background(), r.db, "ag-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 {
		t.Fatalf("delivered = %d", len(delivered))
	}
	dec, err := encryption.Decrypt(delivered[0].EncryptedValue)
	if err != nil || dec != newAccess {
		t.Errorf("delivered value is not the refreshed token (err=%v)", err)
	}
	keys := map[string]bool{}
	for _, f := range delivered[0].Fields {
		keys[f.Key] = true
	}
	for _, k := range []string{"refresh_token", "id_token", "account_id", "mode"} {
		if !keys[k] {
			t.Errorf("delivered parts lack %s: %v", k, keys)
		}
	}
	// Far from expiry: no call.
	calls := r.tokens.calls
	if _, _, err := loadDeliveredCredentials(context.Background(), r.db, "ag-1"); err != nil {
		t.Fatal(err)
	}
	if r.tokens.calls != calls {
		t.Errorf("a fresh login was refreshed again at run start")
	}
}

// After a successful refresh the file is re-rendered into the running
// container of every Codex agent the login pays for — without the refresh
// token.
func TestProviderLoginRefresher_ReRendersIntoRunningContainers(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	credID := r.seedCodexLogin(t, "codex", 10*time.Hour)
	newAccess := plFakeJWT(t, "plus", time.Now().Add(240*time.Hour))
	r.tokens.next = providerlogin.RefreshResult{AccessToken: newAccess, RefreshToken: "rt.NEW", IDToken: "id.new"}
	execOrFatal(t, r.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', ?, 'Eng', 'eng')`, r.wsID)
	execOrFatal(t, r.db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, cli_adapter) VALUES ('ag-1', ?, 'crew-1', 'A', 'reviewer', 'CODEX_CLI')`, r.wsID)
	execOrFatal(t, r.db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, cli_adapter) VALUES ('ag-2', ?, 'crew-1', 'B', 'lead', 'CLAUDE_CODE')`, r.wsID)
	execOrFatal(t, r.db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, crew_id, agent_id, slot) VALUES ('b-1', ?, ?, 'CREW', 'crew-1', NULL, 'OPENAI_API_KEY')`, r.wsID, credID)

	if _, err := r.rf.Refresh(context.Background(), credID, true); err != nil {
		t.Fatal(err)
	}
	var writes []provider.ExecConfig
	for _, e := range r.execs {
		script := strings.Join(e.Cmd, " ")
		if strings.Contains(script, ".codex/auth.json") {
			writes = append(writes, e)
		}
	}
	if len(writes) != 1 {
		t.Fatalf("auth.json writes = %d, want 1 (the Codex agent, not the Claude one): %v", len(writes), r.execs)
	}
	w := writes[0]
	if w.ContainerID != "crewship-team-eng" || w.User != "1001:1001" || w.WorkingDir != "/crew/agents/reviewer" {
		t.Errorf("write target = %+v", w)
	}
	script := strings.Join(w.Cmd, " ")
	var body string
	for i, f := range strings.Fields(script) {
		if f == "echo" && i+1 < len(strings.Fields(script)) {
			if b, err := base64.StdEncoding.DecodeString(strings.Fields(script)[i+1]); err == nil {
				body = string(b)
			}
		}
	}
	if !strings.Contains(body, newAccess) || !strings.Contains(body, `"id_token": "id.new"`) || strings.Contains(body, "rt.NEW") || strings.Contains(body, "rt.REAL-SECRET") {
		t.Errorf("re-rendered file wrong or leaks:\n%s", body)
	}
}

func TestAgentGet_PaysWith(t *testing.T) {
	t.Parallel()
	r := newPLRig(t)
	credID := r.seedCodexLogin(t, "codex", 100*time.Hour)
	execOrFatal(t, r.db, `INSERT INTO agents (id, workspace_id, name, slug, cli_adapter) VALUES ('ag-1', ?, 'A', 'a', 'CODEX_CLI')`, r.wsID)
	execOrFatal(t, r.db, `INSERT INTO agents (id, workspace_id, name, slug, cli_adapter) VALUES ('ag-2', ?, 'B', 'b', 'CLAUDE_CODE')`, r.wsID)
	execOrFatal(t, r.db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name) VALUES ('ac-1', 'ag-1', ?, 'OPENAI_API_KEY')`, credID)
	execOrFatal(t, r.db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name) VALUES ('ac-2', 'ag-2', ?, 'OPENAI_API_KEY')`, credID)
	ah := NewAgentHandler(r.db, r.h.logger)

	get := func(agentID string) map[string]any {
		t.Helper()
		req := plRequest(t, "GET", "/api/v1/agents/"+agentID, "", r.userID, r.wsID, "OWNER")
		req.SetPathValue("agentId", agentID)
		rr := httptest.NewRecorder()
		ah.Get(rr, req)
		if rr.Code != 200 {
			t.Fatalf("get agent: %d %s", rr.Code, rr.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}
	a := get("ag-1")
	pw, ok := a["pays_with"].(map[string]any)
	if !ok {
		t.Fatalf("codex agent has no pays_with: %v", a["pays_with"])
	}
	if pw["credential_id"] != credID || pw["name"] != "codex" || pw["login"].(map[string]any)["provider"] != "OPENAI" {
		t.Errorf("pays_with = %v", pw)
	}
	// A Claude Code agent holding only an OpenAI login pays with nothing.
	if b := get("ag-2"); b["pays_with"] != nil {
		t.Errorf("claude agent pays_with = %v, want null", b["pays_with"])
	}
}

func TestCredSecretPaths_ProviderLogin(t *testing.T) {
	t.Parallel()
	if got := credSecretPaths("writer", "OPENAI_API_KEY", "PROVIDER_LOGIN", "OPENAI", "subscription", []string{"refresh_token", "id_token"}); len(got) != 1 || got[0] != "/crew/agents/writer/.codex/auth.json" {
		t.Errorf("codex login paths = %v", got)
	}
	if got := credSecretPaths("writer", "OPENAI_API_KEY", "PROVIDER_LOGIN", "OPENAI", "api_key", nil); got != nil {
		t.Errorf("api_key login paths = %v, want none", got)
	}
	if got := credSecretPaths("writer", "CLAUDE_CODE_OAUTH_TOKEN", "PROVIDER_LOGIN", "ANTHROPIC", "subscription", nil); got != nil {
		t.Errorf("claude login paths = %v, want none", got)
	}
}
