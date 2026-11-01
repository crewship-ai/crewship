package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// dashboardEditFixture prepares the Edit-share matrix (#4732): v1 is the
// shared reader pin, v2 is the private latest head, and every principal kind
// that a data write can arrive from is seeded. Both versions declare "sales"
// and the owner seeds one row at latest so a refused write can be shown to
// leave stored data byte-for-byte unchanged.
func dashboardEditFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/versions", dashboardBody(1, "sales")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='three-screens'`, ws).Scan(&page); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct{ id, level string }{{"data-editor", "write"}, {"data-commenter", "comment"}, {"data-reader", "read"}, {"data-revoked", "write"}} {
		mustExec(t, h.db, `INSERT INTO users(id,email) VALUES(?,?)`, seed.id, seed.id+"@example.test")
		mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, seed.id)
		mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,?,?,datetime('now'))`, page, seed.id, seed.level, owner)
	}
	mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='data-revoked'`, page)
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES('data-stranger','data-stranger@example.test')`)
	mustExec(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('data-other-ws','Other','data-other-ws')`)
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES('data-outsider','data-outsider@example.test')`)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,'data-other-ws','data-outsider','OWNER')`, generateCUID())
	if w := dashboardRequest(t, h, ws, owner, "POST", "sales", "", `{"base_version":2,"documents":[{"id":"seed","ifVersion":0,"value":{"amount":1}}]}`); w.Code != 200 {
		t.Fatal("seed", w.Code, w.Body)
	}
	return h, ws, owner, page
}

func dashboardDataSnapshot(t *testing.T, h *PageHandler, page string) string {
	t.Helper()
	rows, err := h.db.Query(`SELECT collection,id,version,value_json,updated_by FROM artifact_data_documents WHERE page_id=? ORDER BY collection,id`, page)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var collection, id, value, actor string
		var version int64
		if err := rows.Scan(&collection, &id, &version, &value, &actor); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s/%s@%d=%s by %s", collection, id, version, value, actor))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

// PRD Artifacts §0.14: Read/Comment are read-only, Edit writes. Data writes use
// the same Edit predicate as source publishing, against the latest head only.
func TestDashboardDataEditShareWriteMatrix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		user      string // "" = the fixture owner
		workspace string // "" = the fixture workspace
		body      string
		code      int
		written   bool
	}{
		{name: "owner writes latest", body: `{"base_version":2,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 200, written: true},
		{name: "edit share writes latest", user: "data-editor", body: `{"base_version":2,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 200, written: true},
		{name: "edit share updates owner row", user: "data-editor", body: `{"base_version":2,"documents":[{"id":"seed","ifVersion":1,"value":{"amount":2}}]}`, code: 200, written: true},
		{name: "edit share CAS conflict", user: "data-editor", body: `{"base_version":2,"documents":[{"id":"seed","ifVersion":0,"value":{"amount":99}}]}`, code: 409},
		{name: "edit share stale ifVersion", user: "data-editor", body: `{"base_version":2,"documents":[{"id":"seed","ifVersion":7,"value":{"amount":99}}]}`, code: 409},
		{name: "edit share cannot write shared pin", user: "data-editor", body: `{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 409},
		{name: "edit share cannot write future version", user: "data-editor", body: `{"base_version":3,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 409},
		{name: "comment share refused at pin", user: "data-commenter", body: `{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 403},
		{name: "comment share refused at latest", user: "data-commenter", body: `{"base_version":2,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 403},
		{name: "read share refused", user: "data-reader", body: `{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 403},
		// A removed grant leaves no entitlement to the Page, so it looks absent (404),
		// exactly like the source-publish path; it never reveals that a write was denied.
		{name: "revoked edit sees absent", user: "data-revoked", body: `{"base_version":2,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 404},
		{name: "non-member sees absent", user: "data-stranger", body: `{"base_version":2,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 404},
		{name: "other workspace sees absent", user: "data-outsider", workspace: "data-other-ws", body: `{"base_version":2,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`, code: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, owner, page := dashboardEditFixture(t)
			user, workspace := tc.user, tc.workspace
			if user == "" {
				user = owner
			}
			if workspace == "" {
				workspace = ws
			}
			before := dashboardDataSnapshot(t, h, page)
			w := dashboardRequest(t, h, workspace, user, "POST", "sales", "", tc.body)
			if w.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.code, w.Body)
			}
			after := dashboardDataSnapshot(t, h, page)
			if !tc.written {
				if after != before {
					t.Fatalf("refused write changed data:\n%s\n->\n%s", before, after)
				}
				for _, leak := range []string{"seed", "amount", page, owner} {
					if tc.code != 409 && strings.Contains(w.Body.String(), leak) {
						t.Fatalf("refusal leaked %q: %s", leak, w.Body)
					}
				}
				return
			}
			var receipt artifactDataResponse
			if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || len(receipt.Documents) != 1 || receipt.Documents[0].SourceLabel != "You" || receipt.CodeVersion != 2 {
				t.Fatalf("receipt %v %s", err, w.Body)
			}
			// Provenance is the audit for data writes (owner writes add no audit_logs
			// row either): the stored actor is the authenticated human, never the owner.
			var actor string
			if err := h.db.QueryRow(`SELECT updated_by FROM artifact_data_documents WHERE page_id=? AND id=?`, page, receipt.Documents[0].ID).Scan(&actor); err != nil || actor != user {
				t.Fatalf("actor %q want %q: %v", actor, user, err)
			}
			if user != owner {
				read := dashboardRequest(t, h, ws, owner, "GET", "sales", "?version=2", "")
				if read.Code != 200 || strings.Contains(read.Body.String(), user) || !strings.Contains(read.Body.String(), `"source_label":"Contributor"`) {
					t.Fatalf("owner projection of editor write: %d %s", read.Code, read.Body)
				}
			}
			var latest int64
			if err := h.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, page).Scan(&latest); err != nil || latest != 2 {
				t.Fatalf("data write changed code version: %d %v", latest, err)
			}
		})
	}
}

// An Edit share that is downgraded or revoked between two writes loses write
// authority at once; the second write is refused and nothing changes.
func TestDashboardDataEditShareRevocationTakesEffect(t *testing.T) {
	for _, change := range []string{"revoke", "downgrade to comment", "downgrade to read"} {
		t.Run(change, func(t *testing.T) {
			h, ws, _, page := dashboardEditFixture(t)
			if w := dashboardRequest(t, h, ws, "data-editor", "POST", "sales", "", `{"base_version":2,"documents":[{"id":"seed","ifVersion":1,"value":{"amount":2}}]}`); w.Code != 200 {
				t.Fatal("editor write", w.Code, w.Body)
			}
			switch change {
			case "revoke":
				mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='data-editor'`, page)
			case "downgrade to comment":
				mustExec(t, h.db, `UPDATE page_grants SET level='comment' WHERE page_id=? AND subject_id='data-editor'`, page)
			default:
				mustExec(t, h.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_id='data-editor'`, page)
			}
			want := 403 // a remaining Read/Comment share may learn that the write was denied
			if change == "revoke" {
				want = 404 // no remaining grant: the Page looks absent
			}
			before := dashboardDataSnapshot(t, h, page)
			if w := dashboardRequest(t, h, ws, "data-editor", "POST", "sales", "", `{"base_version":2,"documents":[{"id":"seed","ifVersion":2,"value":{"amount":3}}]}`); w.Code != want {
				t.Fatalf("post-%s write: %d %s", change, w.Code, w.Body)
			}
			if after := dashboardDataSnapshot(t, h, page); after != before {
				t.Fatalf("post-%s write changed data", change)
			}
		})
	}
}
