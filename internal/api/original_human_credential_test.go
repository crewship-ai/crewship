package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chataudience"
)

func originalSessionFixture(t *testing.T) (*artifactPrincipalFixture, *auth.OriginalCredential) {
	t.Helper()
	f := newArtifactPrincipalFixture(t)
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, f.rig.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('original-session',?,?)`, f.a.HumanUserID, expiry.Format(time.RFC3339Nano))
	source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: f.a.HumanUserID, SessionID: "original-session", CredentialExpiresAt: expiry.Add(-time.Minute)}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	return f, source
}

func TestOriginalCredentialSourceThroughReceiptAndImmutablePrincipal(t *testing.T) {
	f, source := originalSessionFixture(t)
	h := NewInternalHandler(f.rig.db, "synthetic-master", newTestLogger())
	receipt, err := h.originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, nil, source)
	if err != nil || receipt == nil || receipt.OriginalCredential() != *source {
		t.Fatal("original source lost", receipt, err)
	}
	f.receipt = receipt
	f.admit(t)
	stored, err := f.pages.artifactPrincipalOriginalCredential(t.Context(), &f.a)
	if err != nil || stored != *source {
		t.Fatal("stored source changed", stored, err)
	}
	if err := f.pages.checkOriginalUserArtifactRunPrincipal(t.Context(), &f.a); err != nil {
		t.Fatal("strict original admission", err)
	}
	horizon, err := f.pages.validateArtifactPrincipalOriginalCredential(t.Context(), &f.a, "pages:read")
	expected, _ := source.Expiry()
	if err != nil || !horizon.Equal(expected) {
		t.Fatal("original expiry extended", horizon, expected, err)
	}
	// Exact retry keeps the original horizon. A new receipt with the same user
	// counter but a different original horizon cannot replace that admission.
	if err := f.capture(httptest.NewRequest(http.MethodPost, "/", nil), f.a.MessageID, receipt); err != nil {
		t.Fatal(err)
	}
	changed := *source
	changed.ExpiresAt = time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339Nano)
	renewed, err := h.originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, nil, &changed)
	if err != nil || renewed == nil {
		t.Fatal(err)
	}
	if err := f.capture(httptest.NewRequest(http.MethodPost, "/", nil), f.a.MessageID, renewed); err == nil {
		t.Fatal("retry replaced original credential horizon")
	}
	if _, err := f.rig.db.Exec(`UPDATE artifact_run_principals SET source_credential_expires_at=? WHERE run_id=?`, changed.ExpiresAt, f.a.RunID); err == nil {
		t.Fatal("principal source mutable through SQL")
	}
	// A queued run cannot become current again when the credential is restored.
	mustExec(t, f.rig.db, `UPDATE user_sessions SET revoked_at='2026-10-23T00:00:00Z' WHERE id='original-session'`)
	mustExec(t, f.rig.db, `UPDATE user_sessions SET revoked_at=NULL WHERE id='original-session'`)
	if _, err := validateOriginalHumanCredential(t.Context(), f.rig.db, *source, scopeSelf); err == nil {
		t.Fatal("source revived after revoke/restore ABA")
	}
	if got, err := h.originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, receipt, nil); err != nil || got != nil {
		t.Fatal("queued receipt recaptured fresh fence", got, err)
	}
	if err := f.pages.checkOriginalUserArtifactRunPrincipal(t.Context(), &f.a); err == nil {
		t.Fatal("retained principal reopened after credential ABA")
	}
}

func TestOriginalCredentialStreamSnapshotCannotUpgradeLegacyOrReplayABA(t *testing.T) {
	f, source := originalSessionFixture(t)
	v, err := auth.NewJWTValidator("original-source-test-secret-at-least-32-chars")
	if err != nil {
		t.Fatal(err)
	}
	m := NewAuthMiddleware(v, sessions.NewDBStore(f.rig.db), f.rig.db, newTestLogger())
	ticket, err := v.IssueOriginalCredentialWSTicket(source.UserID, source.ID, "", "", "", *source)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := v.ValidateWS(ticket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := m.authenticateStream(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	if got := auth.OriginalCredentialFromContext(ctx); got == nil || *got != *source {
		t.Fatal("stream lost signed original", got)
	}
	release()
	legacy := *claims
	legacy.OriginalCredential = nil
	ctx, release, err = m.authenticateStream(t.Context(), &legacy)
	if err != nil {
		t.Fatal(err)
	}
	if auth.OriginalCredentialFromContext(ctx) != nil {
		t.Fatal("legacy stream recaptured current original")
	}
	release()
	mustExec(t, f.rig.db, `UPDATE users SET access_revoked_at='2026-10-23T00:00:00Z' WHERE id=?`, source.UserID)
	mustExec(t, f.rig.db, `UPDATE users SET access_revoked_at=NULL WHERE id=?`, source.UserID)
	if _, release, err := m.authenticateStream(t.Context(), claims); err == nil {
		release()
		t.Fatal("old signed source upgraded after user ABA")
	}
}

func TestOriginalCredentialRealHTTPAuthenticationToSignedTicket(t *testing.T) {
	f, _ := originalSessionFixture(t)
	v, err := auth.NewJWTValidator("original-http-source-secret-at-least-32-chars")
	if err != nil {
		t.Fatal(err)
	}
	store := sessions.NewDBStore(f.rig.db)
	m := NewAuthMiddleware(v, store, f.rig.db, newTestLogger())
	h := NewAuthHandler(f.rig.db, newTestLogger(), v, store, false)
	access, err := v.IssueAccessToken(f.a.HumanUserID, "original-session", "Human", "")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/ws-token", nil)
	request.Header.Set("Authorization", "Bearer "+access)
	response := httptest.NewRecorder()
	m.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		source := auth.OriginalCredentialFromContext(r.Context())
		if user == nil || source == nil || user.OriginalCredential == nil || *source != *user.OriginalCredential {
			t.Fatal("HTTP authentication omitted host original")
		}
		h.WsToken(w, r)
	})).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal("original ticket exchange", response.Code, response.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	claims, err := v.ValidateWS(body.Token)
	if err != nil || claims.OriginalCredential == nil {
		t.Fatal("HTTP source not signed into ticket", err)
	}
	ctx, release, err := m.authenticateStream(t.Context(), claims)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	original := auth.OriginalCredentialFromContext(ctx)
	if original == nil || *original != *claims.OriginalCredential {
		t.Fatal("upgrade refreshed source", original)
	}
	internal := NewInternalHandler(f.rig.db, "synthetic-master", newTestLogger())
	receipt, err := internal.originalHumanChatReceipt(ctx, f.a.ChatID, f.a.HumanUserID, nil, original)
	if err != nil || receipt == nil || receipt.OriginalCredential() != *original {
		t.Fatal("original source lost before receipt", receipt, err)
	}
}

func TestOriginalCredentialCLIScopeBindingExpiryAndPartialProvenance(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	token := "crewship_cli_original-source-synthetic"
	mustExec(t, f.rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('original-cli',?,'fixture',?,'STANDARD','["pages:read"]')`, f.a.HumanUserID, hashStandard(token))
	source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: f.a.HumanUserID, TokenID: "original-cli"}, AuthKindCLIToken, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateOriginalHumanCredential(t.Context(), f.rig.db, *source, "pages:read"); err != nil {
		t.Fatal(err)
	}
	if _, err := validateOriginalHumanCredential(t.Context(), f.rig.db, *source, "pages:write"); err == nil {
		t.Fatal("scope widened")
	}
	expired := *source
	expired.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if _, err := validateOriginalHumanCredential(t.Context(), f.rig.db, expired, "pages:read"); err == nil {
		t.Fatal("expired original refreshed")
	}
	wrong := *source
	wrong.Binding = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := validateOriginalHumanCredential(t.Context(), f.rig.db, wrong, "pages:read"); err == nil {
		t.Fatal("wrong original binding accepted")
	}
	// Original user revision alone cannot admit the new Mooring path.
	if err := captureOriginalUserArtifactPrincipalQuery(httptest.NewRequest(http.MethodPost, "/", nil), f.rig.db, f.a.RunID, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, f.receipt); err == nil {
		t.Fatal("missing credential became Mooring admission")
	}
	partial := *f.receipt
	partial.SourceCredentialID = source.ID
	if got, err := chataudience.CaptureTrusted(t.Context(), f.rig.db, f.a.ChatID, f.a.HumanUserID, &partial); err != nil || got != nil {
		t.Fatal("partial source accepted", got, err)
	}
	mustExec(t, f.rig.db, `UPDATE cli_tokens SET token_hash=? WHERE id='original-cli'`, hashStandard(token+"replacement"))
	if _, err := validateOriginalHumanCredential(t.Context(), f.rig.db, *source, "pages:read"); err == nil {
		t.Fatal("row replacement accepted")
	}
}

func TestOriginalCredentialResolveHumanRejectsUntrustedOrMalformedSource(t *testing.T) {
	f, source := originalSessionFixture(t)
	h := NewInternalHandler(f.rig.db, "synthetic-master", newTestLogger())
	raw, _ := json.Marshal(source)
	expired := *source
	expired.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	expiredRaw, _ := json.Marshal(expired)
	wrong := *source
	wrong.UserID = f.rig.adminID
	wrongRaw, _ := json.Marshal(wrong)
	for _, body := range []string{`null`, string(raw) + `{}`, strings.TrimSuffix(string(raw), "}") + `,"unrecognized":true}`, `{"user_authority_revision":9007199254740993}`, string(expiredRaw), string(wrongRaw)} {
		request := httptest.NewRequest(http.MethodGet, "/resolve-human?user_id="+url.QueryEscape(source.UserID)+"&source_credential="+base64.RawURLEncoding.EncodeToString([]byte(body)), nil)
		request.SetPathValue("chatId", f.a.ChatID)
		response := httptest.NewRecorder()
		h.ResolveHumanChat(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatal("invalid source reached resolver", response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/resolve-human?user_id="+url.QueryEscape(source.UserID)+"&source_credential="+base64.RawURLEncoding.EncodeToString(raw), nil)
	request.SetPathValue("chatId", f.a.ChatID)
	request = request.WithContext(context.WithValue(request.Context(), ctxInternalTokenWS, f.a.WorkspaceID))
	response := httptest.NewRecorder()
	h.ResolveHumanChat(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("workspace token asserted original human source", response.Code)
	}
}

func TestOriginalCredentialAdminKeyRotationRevokesOriginalSource(t *testing.T) {
	t.Setenv(adminTokenHMACKeyEnv, strings.Repeat("ab", 32))
	f := newArtifactPrincipalFixture(t)
	key, err := adminHMACKey()
	if err != nil {
		t.Fatal(err)
	}
	token := cliTokenAdminPrefix + "synthetic-original-admission"
	mustExec(t, f.rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('original-admin',?,'fixture',?,'ADMIN')`, f.a.HumanUserID, hashAdmin(token, key))
	source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: f.a.HumanUserID, TokenID: "original-admin"}, AuthKindCLIToken, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateOriginalHumanCredential(t.Context(), f.rig.db, *source, "pages:read"); err != nil {
		t.Fatal(err)
	}
	// Canonical bearer auth changes on key rotation even though the exact
	// credential row and user counter have not changed.
	t.Setenv(adminTokenHMACKeyEnv, strings.Repeat("cd", 32))
	if _, err := ValidateCLITokenFull(t.Context(), f.rig.db, token, ValidateAuditContext{}); err == nil {
		t.Fatal("canonical auth unexpectedly accepts rotated key")
	}
	if _, err := validateOriginalHumanCredential(t.Context(), f.rig.db, *source, "pages:read"); err == nil {
		t.Fatal("original credential survived canonical ADMIN key revocation")
	}
}

func TestOriginalCredentialScopedReferenceRejectsMalformedPolicy(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	token := cliTokenStandardPrefix + "synthetic-strict-scope"
	mustExec(t, f.rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('strict-scope-cli',?,'fixture',?,'STANDARD')`, f.a.HumanUserID, hashStandard(token))
	for _, tc := range []struct {
		name    string
		value   any
		allowed bool
	}{
		{"sql-null", nil, true}, {"empty", "", true}, {"whitespace", "  ", true}, {"empty-array", "[]", true},
		{"read", `["pages:read"]`, true}, {"resource-wildcard", `["pages:*"]`, true}, {"all-wildcard", `["*"]`, true},
		{"write-only", `["pages:write"]`, false}, {"malformed", "{", false}, {"object", "{}", false}, {"json-null", "null", false},
		{"scalar", `"pages:read"`, false}, {"null-member", `[null]`, false}, {"numeric-member", `[1]`, false}, {"empty-member", `[""]`, false},
		{"unknown-member", `["pages:read","unrecognized:scope"]`, false}, {"mixed-null", `["pages:read",null]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustExec(t, f.rig.db, `UPDATE cli_tokens SET scopes=? WHERE id='strict-scope-cli'`, tc.value)
			source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: f.a.HumanUserID, TokenID: "strict-scope-cli"}, AuthKindCLIToken, token)
			if err != nil {
				t.Fatal("capture provenance", err)
			}
			if _, err := validateOriginalHumanCredential(t.Context(), f.rig.db, *source, scopeSelf); err != nil {
				t.Fatal("generic provenance policy changed", err)
			}
			_, err = validateOriginalHumanCredential(t.Context(), f.rig.db, *source, "pages:read")
			if (err == nil) != tc.allowed {
				t.Errorf("scoped admission allowed=%t want=%t", err == nil, tc.allowed)
			}
		})
	}
}

func TestOriginalCredentialExpiredJWTIsStrictMooringOnly(t *testing.T) {
	f, _ := originalSessionFixture(t)
	v, err := auth.NewJWTValidator("synthetic-short-original-jwt-secret-at-least-32-chars")
	if err != nil {
		t.Fatal(err)
	}
	// Mint an actual signed access JWT near the end of its accepted lifetime.
	// The backing session remains live for an hour. ValidateAccess's accepted
	// integer-second horizon ends at exp+6, giving this request 2-3 seconds.
	v.SetClock(func() time.Time { return time.Now().Add(-auth.AccessTokenTTL - 3*time.Second) })
	access, err := v.IssueAccessToken(f.a.HumanUserID, "original-session", "Human", "")
	if err != nil {
		t.Fatal(err)
	}
	v.SetClock(nil)
	h := NewInternalHandler(f.rig.db, "synthetic-master", newTestLogger())
	m := NewAuthMiddleware(v, sessions.NewDBStore(f.rig.db), f.rig.db, newTestLogger())
	var source *auth.OriginalCredential
	var receipt *chataudience.Receipt
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer "+access)
	response := httptest.NewRecorder()
	m.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source = auth.OriginalCredentialFromContext(r.Context())
		if source == nil {
			t.Fatal("verified HTTP auth omitted original source")
		}
		receipt, err = h.originalHumanChatReceipt(r.Context(), f.a.ChatID, f.a.HumanUserID, nil, source)
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatal("near-expiry original HTTP authentication", response.Code)
	}
	if err != nil || receipt == nil {
		t.Fatal("initial verified source", receipt, err)
	}
	f.receipt = receipt
	expiry, _ := source.Expiry()
	time.Sleep(time.Until(expiry) + time.Millisecond)
	resumed, err := h.originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, receipt, nil)
	if err != nil || resumed == nil || *resumed != *receipt {
		t.Errorf("generic resume lost original provenance after JWT horizon: %v %v", resumed, err)
	}
	if fresh, err := h.originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, nil, source); err != nil || fresh != nil {
		t.Errorf("expired original source became fresh admission: %v %v", fresh, err)
	}
	request = httptest.NewRequest(http.MethodPost, "/", nil)
	if err := f.capture(request, f.a.MessageID, receipt); err != nil {
		t.Errorf("generic retention unexpectedly cut off by original JWT: %v", err)
		return
	}
	mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('principal-start',?,'principal-crew','principal-agent','run.started','system','test','principal-run')`, f.rig.wsID)
	if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err != nil {
		t.Error("generic principal lifetime changed", err)
	}
	stored, err := f.pages.artifactPrincipalOriginalCredential(t.Context(), &f.a)
	if err != nil || stored != *source {
		t.Error("generic retention refreshed or lost immutable source", stored, err)
	}
	if err := f.pages.checkOriginalUserArtifactRunPrincipal(t.Context(), &f.a); err == nil {
		t.Error("strict Mooring principal survived original JWT horizon")
	}
	if _, err := f.pages.validateArtifactPrincipalOriginalCredential(t.Context(), &f.a, "pages:read"); err == nil {
		t.Error("strict Mooring horizon refreshed")
	}
	if err := captureOriginalUserArtifactPrincipalQuery(request, f.rig.db, "new-strict-run", f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, receipt); err == nil {
		t.Error("strict Mooring captured expired original")
	}
	var admitted int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_run_principals WHERE run_id='new-strict-run'`).Scan(&admitted); err != nil || admitted != 0 {
		t.Error("denied strict admission wrote principal", admitted, err)
	}
	mustExec(t, f.rig.db, `UPDATE users SET access_revoked_at='2026-10-23T00:00:00Z' WHERE id=?`, source.UserID)
	if got, err := h.originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, receipt, nil); err != nil || got != nil {
		t.Error("generic resume ignored actual user revocation", got, err)
	}
	if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err == nil {
		t.Error("generic principal ignored original global revocation")
	}
}
