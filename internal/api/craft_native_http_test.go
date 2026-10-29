package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func craftNativeHTTPFixture(t *testing.T) (*pagesTransferRig, *craftNativeHTTP, context.Context) {
	t.Helper()
	return craftNativeHTTPFixtureForApplication(t, "wordcraft")
}
func craftNativeHTTPFixtureForApplication(t *testing.T, application string) (*pagesTransferRig, *craftNativeHTTP, context.Context) {
	t.Helper()
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := pagesTransferSetupWithDB(t, db)
	f.seedPage(t, "native-page", "native-page", "Native", f.userID, "")
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('native-page',?,?,?,?,?)`, f.wsID, application, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('native-source',?,?)`, f.userID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	u := &AuthUser{ID: f.userID, SessionID: "native-source", CredentialExpiresAt: time.Now().Add(time.Hour)}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	ctx := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
	return f, newCraftNativeHTTP(NewPageHandler(f.db, nil, newTestLogger()), true, &collaborationAdmissionTestClock{boot: "native-http-boot", now: time.Second}), ctx
}
func craftNativeSessionCall(m *craftNativeHTTP, ctx context.Context) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/native-page/craft/sessions", strings.NewReader(`{}`)).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	w := httptest.NewRecorder()
	m.sessions(w, r)
	return w
}
func TestCraftNativeHTTPHumanSessionBindsOpaquePublicConnection(t *testing.T) {
	_, m, ctx := craftNativeHTTPFixture(t)
	first, second := craftNativeSessionCall(m, ctx), craftNativeSessionCall(m, ctx)
	if first.Code != 201 || second.Code != 201 {
		t.Fatalf("session creation %d %s; %d %s", first.Code, first.Body, second.Code, second.Body)
	}
	var a, b map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if a["session_token"] == "" || a["session_id"] == "" || a["session_token"] == a["session_id"] || a["session_token"] == b["session_token"] || a["session_id"] == b["session_id"] {
		t.Fatalf("nonunique or leaked session authority: %#v %#v", a, b)
	}
}
func TestCraftNativeHTTPDefaultOffAndUntrustedSourceDenied(t *testing.T) {
	_, m, ctx := craftNativeHTTPFixture(t)
	m.enabled = false
	if w := craftNativeSessionCall(m, ctx); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
	m.enabled = true
	if w := craftNativeSessionCall(m, context.WithValue(ctx, ctxUser, &AuthUser{ID: UserFromContext(ctx).ID})); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
}

func craftNativeIssued(t *testing.T, m *craftNativeHTTP, ctx context.Context) (string, string) {
	t.Helper()
	w := craftNativeSessionCall(m, ctx)
	if w.Code != 201 {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
	var result struct {
		Token string `json:"session_token"`
		ID    string `json:"session_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Token) != 64 || len(result.ID) != 64 || result.Token == result.ID {
		t.Fatal("invalid opaque authority")
	}
	return result.Token, result.ID
}
func craftNativeLeaseCall(m *craftNativeHTTP, ctx context.Context, token, slug, operation, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/"+slug+"/craft/lease/"+operation, strings.NewReader(body)).WithContext(ctx)
	r.SetPathValue("slug", slug)
	r.Header.Set("X-Craft-Session", token)
	w := httptest.NewRecorder()
	switch operation {
	case "read":
		m.lease(w, r)
	case "acquire":
		m.acquire(w, r)
	case "renew":
		m.renew(w, r)
	case "release":
		m.release(w, r)
	case "request":
		m.requestHandoff(w, r)
	case "handoff":
		m.handoff(w, r)
	}
	return w
}
func TestCraftNativeHTTPPerTabCustodyAndRequestedHandoff(t *testing.T) {
	_, m, ctx := craftNativeHTTPFixture(t)
	a, _ := craftNativeIssued(t, m, ctx)
	b, _ := craftNativeIssued(t, m, ctx)
	if w := craftNativeLeaseCall(m, ctx, a, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := craftNativeLeaseCall(m, ctx, b, "native-page", "acquire", `{}`); w.Code != 409 {
		t.Fatal("second tab acquired", w.Code, w.Body)
	}
	if w := craftNativeLeaseCall(m, ctx, b, "native-page", "renew", `{"generation":"1"}`); w.Code != 409 {
		t.Fatal("non-holder renewed", w.Code, w.Body)
	}
	request := craftNativeLeaseCall(m, ctx, b, "native-page", "request", `{}`)
	if request.Code != 201 {
		t.Fatal(request.Code, request.Body)
	}
	var data struct {
		ID string `json:"request_id"`
	}
	if err := json.Unmarshal(request.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if w := craftNativeLeaseCall(m, ctx, a, "native-page", "handoff", `{"generation":"1","request_id":"`+data.ID+`"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := craftNativeLeaseCall(m, ctx, a, "native-page", "renew", `{"generation":"1"}`); w.Code != 409 {
		t.Fatal("old holder revived", w.Code, w.Body)
	}
	if w := craftNativeLeaseCall(m, ctx, b, "native-page", "renew", `{"generation":"2"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestCraftNativeHTTPBindingRejectsScopeAndPublicIDAsBearer(t *testing.T) {
	f, m, ctx := craftNativeHTTPFixture(t)
	f.seedPage(t, "other-native", "other-native", "Other", f.userID, "")
	token, id := craftNativeIssued(t, m, ctx)
	for _, test := range []struct {
		token, slug string
		ctx         context.Context
	}{{id, "native-page", ctx}, {token, "other-native", ctx}, {token, "native-page", context.WithValue(ctx, ctxWorkspaceID, "other-workspace")}} {
		if w := craftNativeLeaseCall(m, test.ctx, test.token, test.slug, "acquire", `{}`); w.Code != 403 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := craftNativeLeaseCall(m, ctx, token, "native-page", "acquire", `{"actor_id":"other-human"}`); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestCraftNativeHTTPCredentialAndPolicyABACannotReviveSession(t *testing.T) {
	for _, subject := range []string{"credential", "workspace epoch"} {
		t.Run(subject, func(t *testing.T) {
			f, m, ctx := craftNativeHTTPFixture(t)
			token, _ := craftNativeIssued(t, m, ctx)
			if subject == "credential" {
				mustExec(t, f.db, `UPDATE user_sessions SET revoked_at='2026-10-27T00:00:00Z' WHERE id='native-source'`)
				mustExec(t, f.db, `UPDATE user_sessions SET revoked_at=NULL WHERE id='native-source'`)
			} else {
				mustExec(t, f.db, `UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=?`, f.wsID)
			}
			if w := craftNativeLeaseCall(m, ctx, token, "native-page", "acquire", `{}`); w.Code != 403 {
				t.Fatal("old epoch regained authority", w.Code, w.Body)
			}
		})
	}
}
func TestCraftNativeHTTPPositiveInt64GenerationWire(t *testing.T) {
	for _, raw := range []string{"0", "01", "-1", "1.0", "9223372036854775808"} {
		if _, err := craftNativeGeneration(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"1", "9007199254740993", "9223372036854775807"} {
		value, err := craftNativeGeneration(raw)
		if err != nil || strconv.FormatInt(value, 10) != raw {
			t.Fatal(raw, value, err)
		}
	}
}

func craftNativeFreshContext(t *testing.T, f *pagesTransferRig, ctx context.Context, horizon time.Duration) context.Context {
	t.Helper()
	user := *UserFromContext(ctx)
	user.CredentialExpiresAt = time.Now().Add(horizon)
	source, err := captureOriginalHumanCredential(ctx, f.db, &user, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	user.OriginalCredential = source
	return auth.WithOriginalCredential(context.WithValue(ctx, ctxUser, &user), *source)
}
func craftNativeReauthorize(m *craftNativeHTTP, ctx context.Context, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/native-page/craft/sessions/reauthorize", strings.NewReader(`{}`)).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", token)
	w := httptest.NewRecorder()
	m.reauthorize(w, r)
	return w
}
func TestCraftNativeHTTPFreshCredentialExtendsLiveAdmissionOnly(t *testing.T) {
	f, m, ctx := craftNativeHTTPFixture(t)
	old := craftNativeFreshContext(t, f, ctx, 15*time.Minute)
	token, _ := craftNativeIssued(t, m, old)
	clock := m.clock.(*collaborationAdmissionTestClock)
	clock.now += 14 * time.Minute
	fresh := craftNativeFreshContext(t, f, ctx, 59*time.Minute)
	if w := craftNativeReauthorize(m, fresh, token); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	clock.now += 2 * time.Minute
	if w := craftNativeLeaseCall(m, fresh, token, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal("fresh authenticated renewal did not preserve live editor", w.Code, w.Body)
	}
}
func TestCraftNativeHTTPOldJWTNeverSlidesCapAndExpiredAdmissionCannotRevive(t *testing.T) {
	for _, freshRenewal := range []bool{false, true} {
		t.Run(strconv.FormatBool(freshRenewal), func(t *testing.T) {
			f, m, ctx := craftNativeHTTPFixture(t)
			old := craftNativeFreshContext(t, f, ctx, 15*time.Minute)
			token, _ := craftNativeIssued(t, m, old)
			clock := m.clock.(*collaborationAdmissionTestClock)
			clock.now += 14 * time.Minute
			if !freshRenewal {
				if w := craftNativeReauthorize(m, old, token); w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
			}
			clock.now += 2 * time.Minute
			fresh := craftNativeFreshContext(t, f, ctx, 59*time.Minute)
			if w := craftNativeReauthorize(m, fresh, token); w.Code != 403 {
				t.Fatal("expired admission revived", w.Code, w.Body)
			}
		})
	}
}
func TestCraftNativeHTTPLeaseDTOAndRequestDisplay(t *testing.T) {
	f, m, ctx := craftNativeHTTPFixture(t)
	a, _ := craftNativeIssued(t, m, ctx)
	b, _ := craftNativeIssued(t, m, ctx)
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		var data map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	if free := decode(craftNativeLeaseCall(m, ctx, a, "native-page", "read", `{}`)); free["holder"] != nil {
		t.Fatal("free holder must be null", free)
	}
	held := decode(craftNativeLeaseCall(m, ctx, a, "native-page", "acquire", `{}`))
	holder, ok := held["holder"].(map[string]any)
	if !ok || holder["actor_id"] != "user:"+f.userID || holder["kind"] != "human" {
		t.Fatal(held)
	}
	if w := craftNativeLeaseCall(m, ctx, b, "native-page", "request", `{}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	requested := decode(craftNativeLeaseCall(m, ctx, a, "native-page", "read", `{}`))
	requests, ok := requested["handoff_requested_by"].([]any)
	if !ok || len(requests) != 1 {
		t.Fatal(requested)
	}
	request, ok := requests[0].(map[string]any)
	if !ok || request["actor_id"] != "user:"+f.userID || request["display_name"] == nil || request["request_id"] == nil {
		t.Fatal(requested)
	}
}

func TestCraftNativeHTTPLeaseUnknownAcquiredTimeAndHandoffKind(t *testing.T) {
	_, m, ctx := craftNativeHTTPFixture(t)
	a, _ := craftNativeIssued(t, m, ctx)
	b, _ := craftNativeIssued(t, m, ctx)
	read := func() map[string]any {
		t.Helper()
		w := craftNativeLeaseCall(m, ctx, a, "native-page", "read", `{}`)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		var state map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	state := read()
	if value, present := state["acquired_at"]; !present || value != nil {
		t.Fatal("unknown acquisition time must be an explicit null", state)
	}
	if w := craftNativeLeaseCall(m, ctx, a, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := craftNativeLeaseCall(m, ctx, b, "native-page", "request", `{}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if state := read(); state["handoff_requested_kind"] != "human" {
		t.Fatal(state)
	}
	if w := craftNativeLeaseCall(m, ctx, a, "native-page", "release", `{"generation":"1"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if state := read(); state["holder"] != nil || state["can_write"] != false {
		t.Fatal(state)
	}
}
