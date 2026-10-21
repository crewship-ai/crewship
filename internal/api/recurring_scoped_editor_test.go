package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestScopedRecurringEditorCannotBorrowAnotherPersonsAuthority(t *testing.T) {
	h, owner, workspace, crew := newRecurringHandler(t)
	execOrFatal(t, h.db, `INSERT INTO users(id,email) VALUES('scoped-editor','scoped-editor@example.test')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('editor-member','scoped-editor',?,'ADMIN')`, workspace)
	create := httptest.NewRequest("POST", "/", strings.NewReader(`{"crew_id":"`+crew+`","title":"Original work","description":"Original instructions","cron_expression":"0 9 * * *"}`))
	create = create.WithContext(withWorkspace(withUser(create.Context(), &AuthUser{ID: owner}), workspace, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, create)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var template recurringIssueResponse
	mustUnmarshal(t, rr, &template)
	snapshot := func() string {
		t.Helper()
		var raw string
		err := h.db.QueryRow(`SELECT json_object('title',title,'description',description,'crew',crew_id,'assignee',assignee_id,'cron',cron_expression,'origin',chat_origin_id,'enabled',enabled,'labels',labels_json,'next',next_run,'updated',updated_at) FROM recurring_issues WHERE id=?`, template.ID).Scan(&raw)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	patch := func(user, body string, limited bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PATCH", "/", strings.NewReader(body))
		req.SetPathValue("recurringId", template.ID)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "ADMIN"))
		if limited {
			req = withTokenScopes(req, "workspace:admin")
		}
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		return rr
	}
	for _, body := range []string{
		`{"description":"Foreign instructions"}`, `{"title":"Foreign title"}`, `{"cron_expression":"*/5 * * * *"}`,
		`{"assignee_type":"user","assignee_id":"scoped-editor"}`, `{"priority":"high"}`, `{"crew_id":"` + crew + `"}`,
		`{"description":"Hidden in disable","enabled":false}`, `{"description":"Hidden in labels","labels_json":"[]"}`,
	} {
		before := snapshot()
		rr := patch("scoped-editor", body, true)
		if rr.Code != 403 || !strings.Contains(rr.Body.String(), "requester_authority_required") || snapshot() != before {
			t.Fatalf("foreign scoped edit accepted or mutated row: body=%s code=%d response=%s", body, rr.Code, rr.Body.String())
		}
	}
	// A real subsequent firing still uses only the original admitted definition.
	execOrFatal(t, h.db, `UPDATE recurring_issues SET next_run='2020-01-01T00:00:00Z' WHERE id=?`, template.ID)
	NewRecurringIssueDispatcher(h.db, nil, newTestLogger()).tick(t.Context())
	var title, description, origin string
	if err := h.db.QueryRow(`SELECT title,description,chat_origin_id FROM missions WHERE workspace_id=?`, workspace).Scan(&title, &description, &origin); err != nil {
		t.Fatal(err)
	}
	proof, err := runauthority.ValidateOriginReceipt(t.Context(), h.db, origin, workspace)
	if err != nil || proof.HumanAuthority == nil || proof.HumanAuthority.UserID != owner || title != "Original work" || description != "Original instructions" {
		t.Fatalf("foreign work borrowed original grant: %s %s %+v %v", title, description, proof, err)
	}
	var pinned string
	if err := h.db.QueryRow(`SELECT chat_origin_id FROM recurring_issues WHERE id=?`, template.ID).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"labels_json":"[]"}`, `{"enabled":false}`} {
		if rr := patch("scoped-editor", body, true); rr.Code != 200 {
			t.Fatalf("safe edit rejected: %d %s", rr.Code, rr.Body.String())
		}
		var retained string
		if err := h.db.QueryRow(`SELECT chat_origin_id FROM recurring_issues WHERE id=?`, template.ID).Scan(&retained); err != nil || retained != pinned {
			t.Fatalf("safe edit changed authority: %s %v", retained, err)
		}
	}
	// Full authenticated editing binds the editor, including edits mixed with disable.
	if rr := patch("scoped-editor", `{"description":"Explicit editor admission","enabled":false}`, false); rr.Code != 200 {
		t.Fatalf("full admission: %d %s", rr.Code, rr.Body.String())
	}
	if err := h.db.QueryRow(`SELECT chat_origin_id FROM recurring_issues WHERE id=?`, template.ID).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	proof, err = runauthority.ValidateOriginReceipt(t.Context(), h.db, origin, workspace)
	if err != nil || proof.HumanAuthority == nil || proof.HumanAuthority.UserID != "scoped-editor" {
		t.Fatalf("full editor did not get own grant: %+v %v", proof, err)
	}
	if rr := patch("scoped-editor", `{"description":"Same-person scoped edit"}`, true); rr.Code != 200 {
		t.Fatalf("same editor rejected: %d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, h.db, `UPDATE recurring_issues SET chat_origin_id=NULL WHERE id=?`, template.ID)
	if rr := patch("scoped-editor", `{"description":"Unbound scoped CRUD"}`, true); rr.Code != 200 {
		t.Fatalf("unbound CRUD rejected: %d %s", rr.Code, rr.Body.String())
	}
	var unbound bool
	if err := h.db.QueryRow(`SELECT chat_origin_id IS NULL FROM recurring_issues WHERE id=?`, template.ID).Scan(&unbound); err != nil || !unbound {
		t.Fatalf("unbound edit minted authority: %v %v", unbound, err)
	}
}
