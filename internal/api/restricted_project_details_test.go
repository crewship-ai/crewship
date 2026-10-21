package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestGuestProjectDetailsManagementKeepsLocalAuthority(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('project-manager','manager@example.test'),('project-reader','reader@example.test'),('project-issuer','issuer@example.test'),('project-manage-only','only@example.test')`)
	for _, u := range []string{"project-manager", "project-reader", "project-manage-only", "project-issuer"} {
		role := "VIEWER"
		if u == "project-issuer" {
			role = "ADMIN"
		}
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, u, ws, u, role)
	}
	execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug,description) VALUES('details-project',?,'Client','client','Client brief'),('FOREIGN_PROJECT_CANARY',?,'Private project','private','FOREIGN_DESCRIPTION_CANARY')`, ws, ws)
	store := access.Store{DB: db}
	for _, u := range []string{"project-manager", "project-reader", "project-manage-only"} {
		m, err := store.Membership(t.Context(), u, ws)
		if err != nil {
			t.Fatal(err)
		}
		rights := []access.Right{{Kind: "project", ID: "details-project", Operation: "read"}, {Kind: "project", ID: "details-project", Operation: "list"}}
		if u == "project-manage-only" {
			rights = []access.Right{{Kind: "project", ID: "details-project", Operation: "manage"}}
		}
		if _, err = store.Replace(t.Context(), owner, u, ws, "restricted", m, rights); err != nil {
			t.Fatal(err)
		}
	}
	m, err := store.Membership(t.Context(), "project-manager", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "project-issuer", "project-manager", ws, "restricted", m, []access.Right{{Kind: "project", ID: "details-project", Operation: "read"}, {Kind: "project", ID: "details-project", Operation: "list"}, {Kind: "project", ID: "details-project", Operation: "manage"}}); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	router, err := NewRouter(db, "project-details-fixture-secret-long-enough", newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	tokens := map[string]string{}
	for _, u := range []string{"project-manager", "project-reader", "project-manage-only"} {
		tokens[u] = mintTokenFor(t, db, u, u+strings.Repeat("f", 40))
	}
	base := "/api/v1/workspaces/" + ws + "/restricted-projects/details-project"
	call := func(user, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", ws)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s: HTTP %d want %d: %s", method, path, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	read := func(user string) guestProjectDetails {
		t.Helper()
		rr := call(user, "GET", base, "", 200)
		var d guestProjectDetails
		if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	current := read("project-manager")
	if !current.CanManage || current.Name != "Client" || len(current.Version) != 64 {
		t.Fatal(current)
	}
	denied := call("project-reader", "GET", base, "", 404)
	if strings.Contains(denied.Body.String(), "Client brief") {
		t.Fatal("Read alone disclosed project details")
	}

	body := fmt.Sprintf(`{"name":"Updated client","description":"Updated brief","version":%q}`, current.Version)
	call("project-reader", "PUT", base, body, 404)
	call("project-manage-only", "GET", base, "", 404)
	call("project-manage-only", "PUT", base, body, 404)
	own := call("project-manage-only", "GET", "/api/v1/workspaces/"+ws+"/access/me", "", 200)
	if !strings.Contains(own.Body.String(), "Project management also requires a current Read grant") || !strings.Contains(own.Body.String(), `"state":"denied"`) {
		t.Fatal("Manage-only explanation claims usable access", own.Body.String())
	}

	call("project-manager", "GET", "/api/v1/workspaces/not-selected/restricted-projects/details-project?workspace_id="+ws, "", 404)
	call("project-manager", "GET", strings.Replace(base, "details-project", "FOREIGN_PROJECT_CANARY", 1), "", 404)
	call("project-manager", "PUT", base, strings.TrimSuffix(body, "}")+`,"lead_id":"forged"}`, 400)
	call("project-manager", "PUT", base, body, 200)
	changed := read("project-manager")
	if changed.Name != "Updated client" || changed.Version == current.Version {
		t.Fatal("edit/version missing", changed)
	}
	call("project-manager", "PUT", base, body, 409)
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND actor_id='project-manager' AND summary='Guest updated shared project details'`, ws).Scan(&count); err != nil || count != 1 {
		t.Fatal("atomic audit missing", count, err)
	}
	// A trusted editor changing content also invalidates this snapshot.
	execOrFatal(t, db, `UPDATE projects SET name='Trusted edit' WHERE id='details-project'`)
	call("project-manager", "PUT", base, fmt.Sprintf(`{"name":"Stale","description":"Stale","version":%q}`, changed.Version), 409)
	current = read("project-manager")
	// SQL failure of the audit must roll the project mutation back.
	execOrFatal(t, db, `CREATE TRIGGER reject_project_details_audit BEFORE INSERT ON journal_entries WHEN NEW.summary='Guest updated shared project details' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
	call("project-manager", "PUT", base, fmt.Sprintf(`{"name":"Must roll back","description":"No change","version":%q}`, current.Version), 500)
	if read("project-manager").Name != "Trusted edit" {
		t.Fatal("audit failure committed the edit")
	}
	execOrFatal(t, db, `DROP TRIGGER reject_project_details_audit`)
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes='["agents:write"]' WHERE user_id='project-manager'`)
	if read("project-manager").CanManage {
		t.Fatal("narrow token advertised mutation")
	}
	call("project-manager", "PUT", base, body, 403)
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes=NULL WHERE user_id='project-manager'`)
	// Read/List were issued by OWNER; revoking the Manage issuer must keep
	// those local shares live while immediately refusing writes.
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE id='project-issuer'`)
	call("project-manager", "GET", base, "", 404)
	if err = store.Check(t.Context(), "project-manager", ws, access.Right{Kind: "project", ID: "details-project", Operation: "read"}); err != nil {
		t.Fatal("unrelated OWNER Read grant was revoked", err)
	}

	call("project-manager", "PUT", base, body, 404)
}
