package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestGuestRoutineDraftAuthorityAndAtomicity(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	seedPipelineRow(t, db, ws, "draft-shared", "draft-shared")
	seedPipelineRow(t, db, ws, "draft-private", "draft-private")
	execOrFatal(t, db, `UPDATE pipelines SET definition_json='{"steps":[],"name":"published"}',description='Published description' WHERE id='draft-shared'`)
	store := access.Store{DB: db}
	for _, u := range []string{"draft-editor", "draft-reader", "draft-edit-only", "draft-other-client"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, u, u+"@example.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES(?,?,?,'VIEWER')`, u, u, ws)
		member, err := store.Membership(t.Context(), u, ws)
		if err != nil {
			t.Fatal(err)
		}
		rights := []access.Right{{Kind: "routine", ID: "draft-shared", Operation: "view"}}
		if u == "draft-editor" {
			rights = append(rights, access.Right{Kind: "routine", ID: "draft-shared", Operation: "edit"})
		} else if u == "draft-edit-only" {
			rights[0].Operation = "edit"
		} else if u == "draft-other-client" {
			rights = []access.Right{{Kind: "routine", ID: "draft-private", Operation: "view"}, {Kind: "routine", ID: "draft-private", Operation: "edit"}}
		}
		if _, err = store.Replace(t.Context(), owner, u, ws, "restricted", member, rights); err != nil {
			t.Fatal(err)
		}
	}
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	router, err := NewRouter(db, "routine-draft-fixture-secret-long-enough", newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	tokens := map[string]string{}
	for _, u := range []string{"draft-editor", "draft-reader", "draft-edit-only", "draft-other-client"} {
		tokens[u] = mintTokenFor(t, db, u, u+strings.Repeat("f", 40))
	}
	base := "/api/v1/workspaces/" + ws + "/restricted-routine-library/draft-shared/draft"
	call := func(user, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", ws)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s %s: got %d want %d: %s", user, method, path, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	read := func() guestRoutineDraft {
		t.Helper()
		var draft guestRoutineDraft
		if err := json.Unmarshal(call("draft-editor", "GET", base, "", 200).Body.Bytes(), &draft); err != nil {
			t.Fatal(err)
		}
		return draft
	}
	body := func(d guestRoutineDraft) string {
		t.Helper()
		b, err := json.Marshal(d.guestRoutineDraftContent)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, u := range []string{"draft-reader", "draft-edit-only", "draft-other-client"} {
		call(u, "GET", base, "", 404)
	}
	explain := call("draft-edit-only", "GET", "/api/v1/workspaces/"+ws+"/access/me", "", 200)
	var effective memberEffectiveAccess
	if err = json.Unmarshal(explain.Body.Bytes(), &effective); err != nil || len(effective.Decisions) != 1 || effective.Decisions[0].Operation != "edit" || effective.Decisions[0].State != "denied" || !strings.Contains(effective.Decisions[0].Reason, "current View share") {
		t.Fatal("Edit explanation missed its View dependency", effective, err)
	}
	var directory []guestRoutineLibraryItem
	if err = json.Unmarshal(call("draft-editor", "GET", strings.TrimSuffix(base, "/draft-shared/draft"), "", 200).Body.Bytes(), &directory); err != nil || len(directory) != 1 || !directory[0].CanEdit || directory[0].CanRun {
		t.Fatal("draft directory advertised wrong authority", directory, err)
	}
	call("draft-editor", "GET", strings.Replace(base, ws, "foreign-workspace", 1), "", 403)
	call("draft-editor", "GET", strings.Replace(base, ws, "foreign-workspace", 1)+"?workspace_id="+ws, "", 404)
	baseline := read()
	if baseline.DraftID != "" || baseline.Revision != 0 || !baseline.CanEdit {
		t.Fatal("unexpected baseline", baseline)
	}
	edit := baseline
	edit.Name = "Client draft"
	edit.Definition = json.RawMessage(`{"steps":[],"client_draft_canary":"unpublished"}`)
	call("draft-reader", "PUT", base, body(edit), 404)
	call("draft-editor", "PUT", base, strings.TrimSuffix(body(edit), "}")+`,"skip_test_gate":true}`, 400)
	call("draft-editor", "PUT", base, strings.TrimSuffix(body(edit), "}")+`,"author_agent_id":"forged"}`, 400)
	call("draft-editor", "PUT", base, body(edit), 200)
	saved := read()
	if saved.DraftID == "" || saved.Revision != 1 || saved.Name != edit.Name {
		t.Fatal("missing draft write", saved)
	}
	call("draft-editor", "PUT", base, body(edit), 409)
	// Ordinary View remains published-only; unpublished work is explicitly shared
	// by the additional Edit grant, not leaked to a read-only client.
	view := call("draft-reader", "GET", strings.TrimSuffix(base, "/draft"), "", 200)
	if strings.Contains(view.Body.String(), "client_draft_canary") {
		t.Fatal("View disclosed unpublished collaborative work", view.Body.String())
	}
	var published, actor string
	if err = db.QueryRow(`SELECT definition_json FROM pipelines WHERE id='draft-shared'`).Scan(&published); err != nil || strings.Contains(published, "client_draft_canary") {
		t.Fatal("draft changed the executable recipe", published, err)
	}
	if err = db.QueryRow(`SELECT updated_by FROM pipeline_drafts WHERE id=?`, saved.DraftID).Scan(&actor); err != nil || actor != "draft-editor" {
		t.Fatal("draft lost authenticated authorship", actor, err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE actor_id='draft-editor' AND summary='Guest edited a shared routine draft' AND payload NOT LIKE '%client_draft_canary%'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing content-free atomic audit", count, err)
	}
	execOrFatal(t, db, `CREATE TRIGGER reject_guest_draft_audit BEFORE INSERT ON journal_entries WHEN NEW.summary='Guest edited a shared routine draft' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
	edit = saved
	edit.Name = "Must roll back"
	call("draft-editor", "PUT", base, body(edit), 500)
	if after := read(); after.Revision != saved.Revision || after.Name != saved.Name {
		t.Fatal("audit failure committed draft", after)
	}
	execOrFatal(t, db, `DROP TRIGGER reject_guest_draft_audit`)
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes='["agents:write"]' WHERE id='clt-draft-editor'`)
	if read().CanEdit {
		t.Fatal("narrow token advertises draft mutation")
	}
	call("draft-editor", "PUT", base, body(saved), 403)
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes=NULL WHERE id='clt-draft-editor'`)
	call("draft-editor", "POST", "/api/v1/workspaces/"+ws+"/pipelines/draft-shared/publish", `{}`, 404)
	// A publication outside the draft workflow invalidates the old base. The
	// editor cannot silently retarget its changes to that new live version.
	execOrFatal(t, db, `UPDATE pipelines SET publication_revision=publication_revision+1 WHERE id='draft-shared'`)
	call("draft-editor", "GET", base, "", 409)
	call("draft-editor", "PUT", base, body(saved), 409)
	execOrFatal(t, db, `UPDATE pipelines SET publication_revision=publication_revision-1 WHERE id='draft-shared'`)
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, owner, ws)
	call("draft-editor", "GET", base, "", 404)
	call("draft-editor", "PUT", base, body(saved), 404)
	explain = call("draft-editor", "GET", "/api/v1/workspaces/"+ws+"/access/me", "", 200)
	if !strings.Contains(explain.Body.String(), "grant issuer no longer has active trusted administrator authority") {
		t.Fatal("own-access did not explain the inert Edit issuer", explain.Body.String())
	}
	// Retained draft metadata does not become a new execution or publication door.
	_, err = pipeline.NewStore(db).GetDraft(t.Context(), ws, "draft-shared")
	if err != nil {
		t.Fatal(err)
	}
}
