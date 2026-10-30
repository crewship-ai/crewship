package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func craftNativeProposalRoute(d *craftNativeDocumentsHTTP, ctx context.Context, token, method, suffix, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/v1/pages/native-page/craft/proposals"+suffix, strings.NewReader(body)).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	r.SetPathValue("proposalID", id)
	r.Header.Set("X-Craft-Session", token)
	w := httptest.NewRecorder()
	switch {
	case suffix == "":
		d.listProposals(w, r)
	case strings.HasSuffix(suffix, "/accept"):
		d.acceptProposal(w, r)
	case strings.HasSuffix(suffix, "/reject"):
		d.rejectProposal(w, r)
	default:
		d.proposalDocument(w, r)
	}
	return w
}

// craftNativeProposalPerson signs in another human of workspace ws and, when
// level is set, grants that level on native-page.
func craftNativeProposalPerson(t *testing.T, f *pagesTransferRig, user, ws, level string) context.Context {
	t.Helper()
	mustExec(t, f.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, user, user+"@example.test", user)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, user)
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES(?,?,?)`, user+"-session", user, expiry.Format(time.RFC3339Nano))
	if level != "" {
		mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user',?,?,?)`, user, level, f.userID)
	}
	u := &AuthUser{ID: user, SessionID: user + "-session", CredentialExpiresAt: expiry}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	return auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, ws), *source)
}

func craftNativeProposalSeed(t *testing.T, f *pagesTransferRig, d *craftNativeDocumentsHTTP, page, id string) {
	t.Helper()
	content := []byte(`{"v":"` + id + `"}`)
	pin := mooring.NewNativeStore(nil, d.applications["wordcraft"].Store).EnginePin()
	mustExec(t, f.db, `INSERT INTO craft_native_proposals(id,page_id,workspace_id,application_id,engine_pin,author_actor_id,request_id,base_version,summary,content_hash,content) VALUES(?,?,?,'wordcraft',?,'agent:proposal-agent',?,1,'',?,?)`, id, page, f.wsID, pin, id, fmt.Sprintf("sha256:%x", sha256.Sum256(content)), content)
}

func craftNativeProposalState(t *testing.T, f *pagesTransferRig, id string) string {
	t.Helper()
	var state string
	if err := f.db.QueryRow(`SELECT state FROM craft_native_proposals WHERE id=?`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func craftNativeProposalAdmit(t *testing.T, h *craftNativeHTTP, ctx context.Context) (token, id string, code int) {
	t.Helper()
	w := craftNativeSessionCall(h, ctx)
	if w.Code != 201 {
		return "", "", w.Code
	}
	var out struct {
		Token string `json:"session_token"`
		ID    string `json:"session_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Token, out.ID, w.Code
}

// #4500: reading proposals follows the session-bound read gate, accepting
// needs this admission's lease and write rights, rejecting needs write rights
// only. Neither another workspace nor another page's proposal id gets through.
func TestCraftNativeProposalRoutesRespectPageRights(t *testing.T) {
	f, h, owner := craftNativeHTTPFixture(t)
	// Every person exists before the owner is admitted: membership changes
	// conservatively retire established admissions.
	reader := craftNativeProposalPerson(t, f, "proposal-reader", f.wsID, "read")
	commenter := craftNativeProposalPerson(t, f, "proposal-commenter", f.wsID, "comment")
	coeditor := craftNativeProposalPerson(t, f, "proposal-coeditor", f.wsID, "write")
	mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('proposal-foreign-ws','Foreign','proposal-foreign')`)
	outsider := craftNativeProposalPerson(t, f, "proposal-outsider", "proposal-foreign-ws", "")
	f.seedPage(t, "other-native-page", "other-native-page", "Other", f.userID, "")
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) SELECT 'other-native-page',workspace_id,application_id,engine_revision,engine_sha256,editor_bundle FROM craft_native_documents WHERE page_id='native-page'`)
	cfg := mooring.NativeStoreConfig{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"wordcraft": {Store: cfg}})
	versions := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	if _, err := versions.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"v":1}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
		t.Fatal(err)
	}
	craftNativeProposalSeed(t, f, d, "native-page", "ncp_open_a")
	craftNativeProposalSeed(t, f, d, "native-page", "ncp_open_b")
	craftNativeProposalSeed(t, f, d, "native-page", "ncp_open_c")
	craftNativeProposalSeed(t, f, d, "other-native-page", "ncp_other_page")
	ownerToken, ownerID := craftNativeIssued(t, h, owner)
	if w := craftNativeLeaseCall(h, owner, ownerToken, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal("owner acquire", w.Code, w.Body)
	}
	accept := func(session string) string {
		return fmt.Sprintf(`{"sessionId":%q,"generation":"1"}`, session)
	}

	t.Run("another page's proposal id is not found here", func(t *testing.T) {
		for _, got := range []*httptest.ResponseRecorder{
			craftNativeProposalRoute(d, owner, ownerToken, "GET", "/ncp_other_page", "ncp_other_page", ""),
			craftNativeProposalRoute(d, owner, ownerToken, "POST", "/ncp_other_page/reject", "ncp_other_page", `{}`),
			craftNativeProposalRoute(d, owner, ownerToken, "POST", "/ncp_other_page/accept", "ncp_other_page", accept(ownerID)),
		} {
			if got.Code != 404 || strings.Contains(got.Body.String(), "ncp_other_page") || strings.Contains(got.Body.String(), "bytes") {
				t.Fatal("cross-page proposal reached", got.Code, got.Body)
			}
		}
		if list := craftNativeProposalRoute(d, owner, ownerToken, "GET", "", "", ""); list.Code != 200 || strings.Contains(list.Body.String(), "ncp_other_page") {
			t.Fatal("cross-page proposal listed", list.Code, list.Body)
		}
		if state := craftNativeProposalState(t, f, "ncp_other_page"); state != "open" {
			t.Fatal("cross-page proposal decided", state)
		}
	})

	t.Run("co-editor rejects without the lease but cannot accept", func(t *testing.T) {
		ctx := coeditor
		token, id, code := craftNativeProposalAdmit(t, h, ctx)
		if code != 201 {
			t.Fatal("co-editor admission", code)
		}
		if got := craftNativeProposalRoute(d, ctx, token, "POST", "/ncp_open_b/accept", "ncp_open_b", accept(id)); got.Code != 409 {
			t.Fatal("accept without this admission's lease", got.Code, got.Body)
		}
		if state := craftNativeProposalState(t, f, "ncp_open_b"); state != "open" {
			t.Fatal("refused accept decided the proposal", state)
		}
		got := craftNativeProposalRoute(d, ctx, token, "POST", "/ncp_open_b/reject", "ncp_open_b", `{}`)
		if got.Code != 200 || !strings.Contains(got.Body.String(), `"state":"rejected"`) {
			t.Fatal("co-editor reject", got.Code, got.Body)
		}
		// The lease holder, with write rights, accepts.
		if got = craftNativeProposalRoute(d, owner, ownerToken, "POST", "/ncp_open_c/accept", "ncp_open_c", accept(ownerID)); got.Code != 200 {
			t.Fatal("holder accept", got.Code, got.Body)
		}
	})

	t.Run("read-only reviews but cannot decide", func(t *testing.T) {
		ctx := reader
		token, id, code := craftNativeProposalAdmit(t, h, ctx)
		if code != 201 {
			t.Fatal("reader admission", code)
		}
		list := craftNativeProposalRoute(d, ctx, token, "GET", "", "", "")
		if list.Code != 200 || !strings.Contains(list.Body.String(), "ncp_open_a") {
			t.Fatal("reader list", list.Code, list.Body)
		}
		if doc := craftNativeProposalRoute(d, ctx, token, "GET", "/ncp_open_a", "ncp_open_a", ""); doc.Code != 200 {
			t.Fatal("reader candidate", doc.Code, doc.Body)
		}
		if got := craftNativeProposalRoute(d, ctx, token, "POST", "/ncp_open_a/reject", "ncp_open_a", `{}`); got.Code != 403 {
			t.Fatal("reader rejected", got.Code, got.Body)
		}
		if got := craftNativeProposalRoute(d, ctx, token, "POST", "/ncp_open_a/accept", "ncp_open_a", accept(id)); got.Code != 403 {
			t.Fatal("reader accepted", got.Code, got.Body)
		}
		if state := craftNativeProposalState(t, f, "ncp_open_a"); state != "open" {
			t.Fatal("reader decided a proposal", state)
		}
	})

	t.Run("foreign workspace is refused", func(t *testing.T) {
		ctx := outsider
		if _, _, code := craftNativeProposalAdmit(t, h, ctx); code != 403 {
			t.Fatal("foreign workspace admitted", code)
		}
		// The owner's own admission does not work from another workspace.
		foreignOwner := context.WithValue(owner, ctxWorkspaceID, "proposal-foreign-ws")
		for _, got := range []*httptest.ResponseRecorder{
			craftNativeProposalRoute(d, ctx, ownerToken, "GET", "", "", ""),
			craftNativeProposalRoute(d, foreignOwner, ownerToken, "GET", "", "", ""),
			craftNativeProposalRoute(d, foreignOwner, ownerToken, "GET", "/ncp_open_a", "ncp_open_a", ""),
			craftNativeProposalRoute(d, foreignOwner, ownerToken, "POST", "/ncp_open_a/reject", "ncp_open_a", `{}`),
		} {
			if got.Code != 403 || strings.Contains(got.Body.String(), "ncp_") {
				t.Fatal("foreign workspace reached proposals", got.Code, got.Body)
			}
		}
		if state := craftNativeProposalState(t, f, "ncp_open_a"); state != "open" {
			t.Fatal("foreign workspace decided a proposal", state)
		}
	})

	t.Run("comment-level never sees or decides", func(t *testing.T) {
		ctx := commenter
		if _, _, code := craftNativeProposalAdmit(t, h, ctx); code != 403 {
			t.Fatal("comment-level admitted", code)
		}
		// An admission taken while writing is retired by a downgrade to comment.
		mustExec(t, f.db, `UPDATE page_grants SET level='write' WHERE page_id='native-page' AND subject_id='proposal-commenter'`)
		token, _, code := craftNativeProposalAdmit(t, h, ctx)
		if code != 201 {
			t.Fatal("writer admission", code)
		}
		mustExec(t, f.db, `UPDATE page_grants SET level='comment' WHERE page_id='native-page' AND subject_id='proposal-commenter'`)
		if got := craftNativeProposalRoute(d, ctx, token, "GET", "", "", ""); got.Code != 403 || strings.Contains(got.Body.String(), "ncp_") {
			t.Fatal("comment-level listed", got.Code, got.Body)
		}
		if got := craftNativeProposalRoute(d, ctx, token, "GET", "/ncp_open_a", "ncp_open_a", ""); got.Code != 403 {
			t.Fatal("comment-level read a candidate", got.Code, got.Body)
		}
		if got := craftNativeProposalRoute(d, ctx, token, "POST", "/ncp_open_a/reject", "ncp_open_a", `{}`); got.Code != 403 {
			t.Fatal("comment-level rejected", got.Code, got.Body)
		}
		if state := craftNativeProposalState(t, f, "ncp_open_a"); state != "open" {
			t.Fatal("comment-level decided a proposal", state)
		}
	})
}
