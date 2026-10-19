package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type boardHumanBoundary interface {
	InitBoard(http.ResponseWriter, *http.Request)
	GetBoard(http.ResponseWriter, *http.Request)
}

// An interface assertion keeps the pre-implementation failure behavioral:
// current main compiles but has no Board initialization/read boundary.
func boardHumanFixture(t *testing.T) (*PageHandler, *pagesTransferRig, boardHumanBoundary) {
	t.Helper()
	f := pagesTransferSetup(t)
	h := NewPageHandler(f.db, nil, newTestLogger())
	boundary, ok := any(h).(boardHumanBoundary)
	if !ok {
		t.Fatal("Board human initialization/read boundary is missing on current main")
	}
	f.seedPage(t, "board-human-page", "private-board", "Private Board title", f.userID, "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='board-human-page'`)
	for _, user := range []string{f.userID, f.adminID} {
		mustExec(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes,created_at) VALUES(?,?,'board test',?,'STANDARD','["pages:*"]','2026-10-19T00:00:00Z')`, "board-token-"+user, user, hashStandard("crewship_cli_board_"+user))
	}
	return h, f, boundary
}

func boardHumanCall(t *testing.T, f *pagesTransferRig, b boardHumanBoundary, user, method, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesRequest(t, method, "/api/v1/pages/"+slug+"/board", f.wsID, user, "OWNER", body)
	r.SetPathValue("slug", slug)
	r.Header.Set("Authorization", "Bearer crewship_cli_board_"+user)
	r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
	w := httptest.NewRecorder()
	if method == "POST" {
		b.InitBoard(w, r)
	} else {
		b.GetBoard(w, r)
	}
	return w
}

func TestBoardHumanInitializeReadRetryAndDeny(t *testing.T) {
	h, f, b := boardHumanFixture(t)
	for _, method := range []string{"GET", "POST"} {
		w := boardHumanCall(t, f, b, f.userID, method, "@board-human-page", `{"mutation_id":"first"}`)
		if method == "GET" && w.Code != 404 {
			t.Fatalf("uninitialized read=%d %s", w.Code, w.Body.String())
		}
		if method == "POST" && w.Code != 200 {
			t.Fatalf("initialize=%d %s", w.Code, w.Body.String())
		}
	}
	first := boardHumanCall(t, f, b, f.userID, "GET", "private-board", "")
	retry := boardHumanCall(t, f, b, f.userID, "POST", "@board-human-page", `{"mutation_id":"first"}`)
	if first.Code != 200 || retry.Code != 200 || first.Body.String() != retry.Body.String() {
		t.Fatalf("lost ACK retry differs: %s / %s", first.Body.String(), retry.Body.String())
	}
	if !strings.Contains(first.Body.String(), `"records":[]`) || !strings.Contains(first.Body.String(), `"page_id":"board-human-page"`) || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("snapshot=%s", first.Body.String())
	}
	if got := boardHumanCall(t, f, b, f.userID, "POST", "@board-human-page", `{"mutation_id":"different"}`); got.Code != 409 {
		t.Fatalf("different retry=%d %s", got.Code, got.Body.String())
	}
	mustExec(t, h.db, `UPDATE workspace_members SET role='ADMIN' WHERE user_id=?`, f.adminID)
	for _, method := range []string{"GET", "POST"} {
		got := boardHumanCall(t, f, b, f.adminID, method, "@board-human-page", `{"mutation_id":"first"}`)
		if got.Code != 404 || strings.Contains(got.Body.String(), "Private Board title") {
			t.Fatalf("admin leaked Board: %d %s", got.Code, got.Body.String())
		}
	}
	mustExec(t, h.db, `UPDATE workspace_members SET role='OWNER' WHERE user_id=?`, f.adminID)
	got := boardHumanCall(t, f, b, f.adminID, "GET", "private-board", "")
	if got.Code != 200 {
		t.Fatalf("OWNER read=%d %s", got.Code, got.Body.String())
	}
	var audits int
	if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='board-human-page'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
	mustExec(t, h.db, `UPDATE cli_tokens SET revoked_at='2026-10-19T00:00:00Z' WHERE user_id=?`, f.userID)
	if got := boardHumanCall(t, f, b, f.userID, "GET", "private-board", ""); got.Code != 404 {
		t.Fatalf("revoked read=%d %s", got.Code, got.Body.String())
	}
}

func TestBoardHumanFailClosedInputsAndAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, body, change string
		status             int
	}{
		{"unknown field", `{"mutation_id":"one","html":"<script>"}`, "", 400},
		{"duplicate key", `{"mutation_id":"one","mutation_id":"two"}`, "", 400},
		{"empty ID", `{"mutation_id":""}`, "", 400},
		{"oversize", strings.Repeat("x", 1025), "", 413},
		{"shared Page", `{"mutation_id":"one"}`, `UPDATE pages SET private_audience=0 WHERE id='board-human-page'`, 404},
		{"revoked token", `{"mutation_id":"one"}`, `UPDATE cli_tokens SET revoked_at='2026-10-19'`, 404},
		{"read scope", `{"mutation_id":"one"}`, `UPDATE cli_tokens SET scopes='["pages:read"]'`, 404},
		{"suspended", `{"mutation_id":"one"}`, `UPDATE users SET suspended_at='2026-10-19' WHERE id='ptdeparting'`, 404},
		{"restricted", `{"mutation_id":"one"}`, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='ptdeparting'`, 404},
		{"departed", `{"mutation_id":"one"}`, `DELETE FROM workspace_members WHERE user_id='ptdeparting'`, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f, b := boardHumanFixture(t)
			if tc.change != "" {
				mustExec(t, h.db, tc.change)
			}
			got := boardHumanCall(t, f, b, f.userID, "POST", "@board-human-page", tc.body)
			if got.Code != tc.status {
				t.Fatalf("status=%d want %d: %s", got.Code, tc.status, got.Body.String())
			}
			var count int
			if err := h.db.QueryRow(`SELECT count(*) FROM page_boards`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("denied write persisted: %d %v", count, err)
			}
		})
	}
}

func TestBoardHumanUnsupportedAndExclusiveType(t *testing.T) {
	h, f, b := boardHumanFixture(t)
	got := boardHumanCall(t, f, b, f.userID, "POST", "@board-human-page", `{"mutation_id":"first"}`)
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	for _, sql := range []string{
		`UPDATE pages SET private_audience=0 WHERE id='board-human-page'`,
		`UPDATE pages SET owner_user_id=NULL,owner_crew_id='missing' WHERE id='board-human-page'`,
		`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('ptws1','board-human-page',1,x'00',x'00')`,
	} {
		if _, err := h.db.Exec(sql); err == nil {
			t.Fatalf("type/custody guard accepted %s", sql)
		}
	}
	mustExec(t, h.db, `UPDATE page_boards SET seq=1 WHERE page_id='board-human-page'`)
	for _, method := range []string{"GET", "POST"} {
		got := boardHumanCall(t, f, b, f.userID, method, "@board-human-page", `{"mutation_id":"first"}`)
		if got.Code != 409 || strings.Contains(got.Body.String(), `"records"`) {
			t.Fatalf("unsupported state looked empty: %d %s", got.Code, got.Body.String())
		}
	}
	// Failure to persist the OWNER audit must never release the snapshot.
	mustExec(t, h.db, `UPDATE page_boards SET seq=0 WHERE page_id='board-human-page'`)
	mustExec(t, h.db, `CREATE TRIGGER fail_board_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
	got = boardHumanCall(t, f, b, f.adminID, "GET", "private-board", "")
	if got.Code != 500 || strings.Contains(got.Body.String(), `"records"`) {
		t.Fatalf("unaudited OWNER bytes: %d %s", got.Code, got.Body.String())
	}
}

func TestBoardHumanExplicitCustodyTransferDoesNotGrantPriorRead(t *testing.T) {
	h, f, b := boardHumanFixture(t)
	if got := boardHumanCall(t, f, b, f.userID, "POST", "@board-human-page", `{"mutation_id":"first"}`); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	pagesSeedUser(t, h, f.wsID, "board-reader", "board-reader@example.test", "MEMBER")
	mustExec(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes,created_at) VALUES('board-reader-token','board-reader','test',?,'STANDARD','["pages:*"]','2026-10-19T00:00:00Z')`, hashStandard("crewship_cli_board_board-reader"))
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('board-human-page','user','board-reader','read',?)`, f.userID)
	if got := boardHumanCall(t, f, b, "board-reader", "GET", "private-board", ""); got.Code != 404 {
		t.Fatalf("grant widened private audience: %d %s", got.Code, got.Body.String())
	}
	if pagesSlugListed(pagesListSlugs(t, h, f.wsID, "board-reader", "MEMBER"), "private-board") {
		t.Fatal("private Board appeared in another member's catalog")
	}
	request := ownershipRequest(t, f, "POST", "/", `{"expected_owner_user_id":"ptdeparting","successor_user_id":"board-reader"}`)
	request.SetPathValue("pageID", "board-human-page")
	response := httptest.NewRecorder()
	NewWorkspaceHandler(h.db, newTestLogger()).TransferPageOwnership(response, request)
	if response.Code != 200 {
		t.Fatalf("explicit custody transfer=%d %s", response.Code, response.Body.String())
	}
	if got := boardHumanCall(t, f, b, "board-reader", "GET", "private-board", ""); got.Code != 200 {
		t.Fatalf("new custodian denied: %d %s", got.Code, got.Body.String())
	}
	if got := boardHumanCall(t, f, b, f.userID, "GET", "private-board", ""); got.Code != 404 {
		t.Fatalf("former author retained private access: %d %s", got.Code, got.Body.String())
	}
}

func TestBoardHumanRechecksAfterUpload(t *testing.T) {
	for _, change := range []string{`UPDATE cli_tokens SET revoked_at='2026-10-19'`, `UPDATE pages SET owner_user_id='ptadmin' WHERE id='board-human-page'`} {
		h, f, b := boardHumanFixture(t)
		r := pagesRequest(t, "POST", "/", f.wsID, f.userID, "MEMBER", "")
		r.SetPathValue("slug", "@board-human-page")
		r.Header.Set("Authorization", "Bearer crewship_cli_board_"+f.userID)
		r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
		r.Body = &ownershipChangingBody{Reader: strings.NewReader(`{"mutation_id":"first"}`), change: func() { mustExec(t, h.db, change) }}
		w := httptest.NewRecorder()
		b.InitBoard(w, r)
		if w.Code != 404 {
			t.Fatalf("authority changed during upload=%d %s", w.Code, w.Body.String())
		}
		var count int
		if err := h.db.QueryRow(`SELECT count(*) FROM page_boards`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("revoked upload committed: %d %v", count, err)
		}
	}
}

func TestBoardHumanAgentCannotBorrowHumanCredential(t *testing.T) {
	_, f, b := boardHumanFixture(t)
	if got := boardHumanCall(t, f, b, f.userID, "POST", "@board-human-page", `{"mutation_id":"first"}`); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	r := pagesRequest(t, "GET", "/", f.wsID, f.userID, "MEMBER", "")
	r.SetPathValue("slug", "private-board")
	r.Header.Set("Authorization", "Bearer crewship_cli_board_"+f.userID)
	r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
	r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, &artifactRunAuthority{HumanUserID: f.userID}))
	w := httptest.NewRecorder()
	b.GetBoard(w, r)
	if w.Code != 404 {
		t.Fatalf("run borrowed human authority: %d %s", w.Code, w.Body.String())
	}
}
