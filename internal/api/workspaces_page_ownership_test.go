package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
)

// Real Router, authentication token middleware and migrated SQLite; no provider
// or agent execution. The actor has no ownership or explicit Page grant.
func custodyRouterFixture(t *testing.T, kind string) *instanceFixture {
	t.Helper()
	f := newInstanceFixture(t)
	for _, id := range []string{"departing", "reader", "ungranted"} {
		seedInstanceUser(t, f.db, id, id+"@ex.com", "ws-old", "MEMBER")
	}
	wmSeedPage(t, f.db, "ws-old", "private-output", "private-output", "Private output", "departing", "")
	wmSeedPage(t, f.db, "ws-new", "foreign-output", "foreign-output", "Foreign output", "carol", "")
	wmSeedPage(t, f.db, "ws-old", "regular-page", "regular-page", "Regular Page", "departing", "")
	mustExec(t, f.db, `UPDATE pages SET spec_json='{"private_spec":"NEVER_EXPOSE_THIS"}' WHERE id='private-output'`)
	if kind == "artifact" {
		mustExec(t, f.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES('private-output',1),('foreign-output',1)`)
		mustExec(t, f.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at) VALUES('private-revision','private-output',1,?,'departing','original','digest','2026-10-14T00:00:00Z')`, strings.Repeat("a", 64))
	} else {
		mustExec(t, f.db, `INSERT INTO page_boards(page_id,seq,format) VALUES('private-output',1,'crewship-board-excalidraw-0.18.1-v1'),('foreign-output',0,'crewship-board-excalidraw-0.18.1-v1')`)
		mustExec(t, f.db, `INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,author_user_id,updated_seq) VALUES('private-output','private-element',1,'{"text":"NEVER_EXPOSE_THIS"}','departing',1)`)
	}
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('private-output','user','reader','read','departing')`)
	return f
}

func custodySnapshot(t *testing.T, db *sql.DB, tables ...string) string {
	t.Helper()
	out := map[string]any{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		data := [][]any{}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			data = append(data, vals)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		out[table] = data
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

var custodyProtectedTables = []string{"users", "pages", "workspace_members", "crew_members", "page_grants", "page_versions", "page_artifacts", "page_artifact_revisions", "page_boards", "page_board_elements", "inbox_items"}

func TestOutputCustodyRouterExplicitTransfer(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := custodyRouterFixture(t, kind)
			spy := &pagesJournalSpy{}
			var err error
			f.r, err = NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithJournal(spy))
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/workspaces/ws-old/page-ownership"
			list := f.do(f.wsAdmin, "GET", path+"?owner_user_id=departing", "")
			wantCode(t, list, 200, "custody list")
			var doc struct {
				Pages []pageOwnershipSummary `json:"pages"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Pages) != 1 || doc.Pages[0].ID != "private-output" || doc.Pages[0].Type != kind || doc.Pages[0].OwnerUserID != "departing" {
				t.Fatalf("list %+v", doc)
			}
			ids := map[string]bool{}
			for _, c := range doc.Pages[0].EligibleSuccessors {
				ids[c.UserID] = true
			}
			if !ids["reader"] || !ids["wsadmin"] || !ids["boss"] || ids["ungranted"] || ids["carol"] || ids["departing"] {
				t.Fatalf("eligibility %v", ids)
			}
			for _, forbidden := range []string{"NEVER_EXPOSE_THIS", "spec_json", "element_json", "sha256", "html"} {
				if strings.Contains(list.Body.String(), forbidden) {
					t.Fatalf("metadata leaks %s", forbidden)
				}
			}
			if list.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("custody contacts must not be cached")
			}
			protected := []string{"page_grants", "page_versions", "page_artifacts", "page_artifact_revisions", "page_boards", "page_board_elements", "workspace_members", "crew_members"}
			before := custodySnapshot(t, f.db, protected...)
			transfer := f.do(f.wsAdmin, "POST", path+"/private-output", `{"expected_owner_user_id":"departing","successor_user_id":"reader"}`)
			wantCode(t, transfer, 200, "explicit member custody")
			var p pageOwnershipSummary
			if err := json.Unmarshal(transfer.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.OwnerUserID != "reader" || p.ID != "private-output" {
				t.Fatalf("summary %+v", p)
			}
			if after := custodySnapshot(t, f.db, protected...); after != before {
				t.Fatal("transfer changed content, history, grants or memberships")
			}
			var notices int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='private-output' AND payload_json LIKE '%explicit_human_transfer%'`).Scan(&notices); err != nil {
				t.Fatal(err)
			}
			if len(spy.entries) != 1 || spy.entries[0].Type != entryPageOwnerTransferred || spy.entries[0].ActorID != "wsadmin" || spy.entries[0].Payload["from_user_id"] != "departing" || spy.entries[0].Payload["to_user_id"] != "reader" || spy.entries[0].Payload["reason"] != "explicit_human_transfer" {
				t.Fatalf("human transfer audit %+v", spy.entries)
			}
			if notices != 1 {
				t.Fatalf("notices %d", notices)
			}
			before = custodySnapshot(t, f.db, custodyProtectedTables...)
			wantCode(t, f.do(f.wsAdmin, "POST", path+"/private-output", `{"expected_owner_user_id":"departing","successor_user_id":"boss"}`), 409, "old owner CAS")
			if after := custodySnapshot(t, f.db, custodyProtectedTables...); after != before {
				t.Fatal("stale transfer changed state")
			}
			// Remove the unrelated regular page so retry demonstrates output precondition.
			mustExec(t, f.db, `DELETE FROM pages WHERE id='regular-page'`)
			var memberID string
			if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE workspace_id='ws-old' AND user_id='departing'`).Scan(&memberID); err != nil {
				t.Fatal(err)
			}
			wantCode(t, f.do(f.wsAdmin, "DELETE", "/api/v1/workspaces/ws-old/members/"+memberID, ""), 200, "departure after explicit transfer")
			var owner string
			if err := f.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id='private-output'`).Scan(&owner); err != nil || owner != "reader" {
				t.Fatalf("custody after departure %s %v", owner, err)
			}
		})
	}
}

func TestOutputCustodyDepartureAndGDPRRefuseAtomically(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		for _, mode := range []string{"member", "instance", "gdpr"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := custodyRouterFixture(t, kind)
				wmSeedCrew(t, f.db, "ws-old", "custody-crew", "Crew", "custody-crew")
				wmSeedCrewMember(t, f.db, "custody-crew", "departing")
				before := custodySnapshot(t, f.db, custodyProtectedTables...)
				path := "/api/v1/admin/instance/workspaces/ws-old/members/departing"
				body := ""
				token := f.boss
				if mode == "member" {
					var id string
					if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE workspace_id='ws-old' AND user_id='departing'`).Scan(&id); err != nil {
						t.Fatal(err)
					}
					path = "/api/v1/workspaces/ws-old/members/" + id
					token = f.wsAdmin
				}
				if mode == "gdpr" {
					path = "/api/v1/admin/users/departing/data?workspace_id=ws-old"
					body = `{"reason":"explicit ownership regression"}`
					token = f.wsAdmin
				}
				w := f.do(token, "DELETE", path, body)
				wantCode(t, w, 409, "blocked departure")
				var doc struct {
					Code  string                 `json:"code"`
					Pages []pageOwnershipSummary `json:"pages"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				if doc.Code != "page_ownership_transfer_required" || len(doc.Pages) != 1 || doc.Pages[0].ID != "private-output" || (mode != "instance" && doc.Pages[0].Type != kind) {
					t.Fatalf("structured conflict %s", w.Body.String())
				}
				if after := custodySnapshot(t, f.db, custodyProtectedTables...); after != before {
					t.Fatal("blocked departure mutated subject/pages/content/grants/notices")
				}
				var audits int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM gdpr_actions WHERE status='failed'`).Scan(&audits); err != nil {
					t.Fatal(err)
				}
				if (mode == "gdpr" && audits != 1) || (mode != "gdpr" && audits != 0) {
					t.Fatalf("failed attempt audits %d", audits)
				}
			})
		}
	}
}

// Body consumption happens before the writer transaction; this reproduces a
// permission changed after middleware admission and before the mutation fence.
type custodyRevocationBody struct {
	io.Reader
	revoke func()
	done   bool
}

func (r *custodyRevocationBody) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		r.revoke()
	}
	return r.Reader.Read(p)
}

func TestOutputCustodyLiveAuthorityAndEligibility(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		for _, why := range []string{"no read", "same owner", "foreign user", "foreign page", "regular page", "suspended successor", "restricted successor", "removed successor", "revoked grant", "inactive grant issuer", "actor demoted", "actor suspended", "actor restricted", "actor removed", "notice failure", "bad trailing JSON"} {
			t.Run(kind+"/"+why, func(t *testing.T) {
				f := custodyRouterFixture(t, kind)
				successor := "reader"
				page := "private-output"
				want := 403
				revoke := func() {}
				switch why {
				case "no read":
					successor = "ungranted"
				case "same owner":
					successor = "departing"
				case "foreign user":
					successor = "carol"
				case "foreign page":
					page = "foreign-output"
					want = 404
				case "regular page":
					page = "regular-page"
					want = 404
				case "suspended successor":
					revoke = func() { mustExec(t, f.db, `UPDATE users SET suspended_at='2026-10-14' WHERE id='reader'`) }
				case "restricted successor":
					revoke = func() {
						mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='reader'`)
					}
				case "removed successor":
					revoke = func() { mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id='reader'`) }
				case "revoked grant":
					revoke = func() {
						mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='private-output' AND subject_id='reader'`)
					}
				case "inactive grant issuer":
					revoke = func() { mustExec(t, f.db, `UPDATE users SET suspended_at='2026-10-14' WHERE id='departing'`) }
				case "actor demoted":
					revoke = func() { mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id='wsadmin'`) }
				case "actor suspended":
					revoke = func() { mustExec(t, f.db, `UPDATE users SET suspended_at='2026-10-14' WHERE id='wsadmin'`) }
				case "actor restricted":
					revoke = func() {
						mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='wsadmin'`)
					}
				case "actor removed":
					revoke = func() { mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id='wsadmin'`) }
				case "notice failure":
					mustExec(t, f.db, `CREATE TRIGGER reject_custody_notice BEFORE INSERT ON inbox_items BEGIN SELECT RAISE(ABORT,'notice unavailable'); END`)
					want = 500
				case "bad trailing JSON":
					want = 400
				}
				body := fmt.Sprintf(`{"expected_owner_user_id":"departing","successor_user_id":%q}`, successor)
				if why == "bad trailing JSON" {
					body += " garbage"
				}
				request := pagesGrantRequest(t, http.MethodPost, "/", "ws-old", "wsadmin", "ADMIN", body)
				request.SetPathValue("pageID", page)
				var before string
				request.Body = io.NopCloser(&custodyRevocationBody{Reader: strings.NewReader(body), revoke: func() { revoke(); before = custodySnapshot(t, f.db, custodyProtectedTables...) }})
				h := NewWorkspaceHandler(f.db, newTestLogger())
				w := httptest.NewRecorder()
				h.TransferPageOwnership(w, request)
				wantCode(t, w, want, why)
				if before == "" {
					t.Fatal("body was not consumed")
				}
				if after := custodySnapshot(t, f.db, custodyProtectedTables...); after != before {
					t.Fatal("denied transfer changed state")
				}
				if strings.HasPrefix(why, "actor ") {
					req := pagesGrantRequest(t, "GET", "/", "ws-old", "wsadmin", "ADMIN", "")
					list := httptest.NewRecorder()
					h.ListPageOwnership(list, req)
					wantCode(t, list, 403, "live listing actor")
				}
			})
		}
	}
}

func TestOutputCustodyConcurrentHumanChoicesCommitOne(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := custodyRouterFixture(t, kind)
			var ready sync.WaitGroup
			ready.Add(2)
			start := make(chan struct{})
			results := make(chan *httptest.ResponseRecorder, 2)
			for _, successor := range []string{"reader", "boss"} {
				go func() {
					ready.Done()
					<-start
					request := httptest.NewRequest("POST", "/api/v1/workspaces/ws-old/page-ownership/private-output", strings.NewReader(fmt.Sprintf(`{"expected_owner_user_id":"departing","successor_user_id":%q}`, successor)))
					request.Header.Set("Authorization", "Bearer "+f.wsAdmin)
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					f.r.ServeHTTP(response, request)
					results <- response
				}()
			}
			ready.Wait()
			close(start)
			first, second := <-results, <-results
			if first.Code == 409 {
				first, second = second, first
			}
			wantCode(t, first, 200, "winning custody choice")
			wantCode(t, second, 409, "losing custody choice")
			var winner pageOwnershipSummary
			if err := json.Unmarshal(first.Body.Bytes(), &winner); err != nil {
				t.Fatal(err)
			}
			var owner string
			if err := f.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id='private-output'`).Scan(&owner); err != nil || owner != winner.OwnerUserID {
				t.Fatalf("persisted owner %s winner %s error %v", owner, winner.OwnerUserID, err)
			}
			var notices int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='private-output'`).Scan(&notices); err != nil || notices != 1 {
				t.Fatalf("notices %d error %v", notices, err)
			}
		})
	}
}

func TestOutputCustodyGDPRConflictPreservesSubjectIdentity(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			r := pagesIdentitySetup(t)
			wmSeedPage(t, r.db, pidWSA, "identity-output", "identity-output", "Private output", r.userID, "")
			if kind == "artifact" {
				mustExec(t, r.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES('identity-output',1)`)
			} else {
				mustExec(t, r.db, `INSERT INTO page_boards(page_id,seq,format) VALUES('identity-output',0,'crewship-board-excalidraw-0.18.1-v1')`)
			}
			tableSet := map[string]bool{}
			for _, table := range custodyProtectedTables {
				tableSet[table] = true
			}
			for _, shape := range identityShapes {
				tableSet[shape.table] = true
			}
			tables := []string{}
			for table := range tableSet {
				tables = append(tables, table)
			}
			before := custodySnapshot(t, r.db, tables...)
			response := r.eraseAs(t, pidWSA, r.adminID, http.StatusConflict)
			if response["code"] != "page_ownership_transfer_required" {
				t.Fatalf("conflict %+v", response)
			}
			if after := custodySnapshot(t, r.db, tables...); after != before {
				t.Fatal("409 erased or reattributed seeded subject data")
			}
			if len(r.spy.entries) != 0 {
				t.Fatal("refused transfer emitted a journal entry")
			}
			var audits int
			if err := r.db.QueryRow(`SELECT COUNT(*) FROM gdpr_actions WHERE status='failed'`).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("preserved failed-attempt audit %d %v", audits, err)
			}
		})
	}
}

func TestOutputCustodyTransferDoesNotReissueFormerOwnerGrants(t *testing.T) {
	h, ws, owner, page, token := artifactRunFixture(t)
	const publication = `{"html":"<h1>Private retained source</h1>","base_rev":0,"mutation_id":"before-custody"}`
	artifactExpect(t, artifactAgentSave(t, h, ws, token, publication), 201)
	pagesSeedUser(t, h, ws, "custody-admin", "custody-admin@example.test", "ADMIN")
	pagesSeedUser(t, h, ws, "custody-next", "custody-next@example.test", "MEMBER")
	pagesSeedUser(t, h, ws, "custody-prior-reader", "custody-prior-reader@example.test", "MEMBER")
	for _, id := range []string{"custody-next", "custody-prior-reader"} {
		pagesGrant(t, h, ws, owner, "report", fmt.Sprintf(`{"subject_type":"user","subject":%q,"level":"read"}`, id))
	}
	mustExec(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, owner)
	// A role change correctly invalidates the old captured run. Admit a new
	// synthetic run through the real trusted-chat capture/delegation path, so
	// subsequent denial isolates ownership/issuer standing rather than its epoch.
	receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", owner, nil)
	if err != nil || receipt == nil {
		t.Fatalf("trusted member receipt %v %v", receipt, err)
	}
	hostRequest := httptest.NewRequest("POST", "/", nil)
	if err = captureArtifactChatPrincipal(hostRequest, h.db, "custody-run", ws, "artifact-agent", "artifact-crew", "artifact-chat", "artifact-message", receipt); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('custody-run-start',?,'artifact-crew','artifact-agent','run.started','system','synthetic custody test','custody-run')`, ws)
	delegate := pagesGrantRequest(t, "POST", "/", ws, owner, "MEMBER", `{"run_id":"custody-run"}`)
	delegate.SetPathValue("slug", "report")
	delegated := httptest.NewRecorder()
	h.GrantArtifactRun(delegated, delegate)
	artifactExpect(t, delegated, 201)
	token = internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, ws, "artifact-crew"), ws, "artifact-agent", "custody-run")
	readAgent := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/internal/artifacts/report?revision=1", nil)
		req.SetPathValue("slug", "report")
		req.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(req.Context(), ctxInternalTokenWS, ws)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
		response := httptest.NewRecorder()
		h.GetArtifactAgent(response, req.WithContext(ctx))
		return response
	}
	artifactExpect(t, readAgent(), 200)
	const memberPublication = `{"html":"<h1>Member-owned source</h1>","base_rev":1,"mutation_id":"member-before-custody"}`
	artifactExpect(t, artifactAgentSave(t, h, ws, token, memberPublication), 201)
	artifactExpect(t, artifactCall(t, h, ws, "custody-prior-reader", "MEMBER", "GET", "?revision=1", ""), 200)
	before := custodySnapshot(t, h.db, "page_grants", "page_artifact_revisions", "page_artifact_mutation_receipts", "artifact_workspace_writes")
	request := pagesGrantRequest(t, "POST", "/", ws, "custody-admin", "ADMIN", fmt.Sprintf(`{"expected_owner_user_id":%q,"successor_user_id":"custody-next"}`, owner))
	request.SetPathValue("pageID", page)
	response := httptest.NewRecorder()
	NewWorkspaceHandler(h.db, newTestLogger()).TransferPageOwnership(response, request)
	wantCode(t, response, 200, "explicit transfer")
	if after := custodySnapshot(t, h.db, "page_grants", "page_artifact_revisions", "page_artifact_mutation_receipts", "artifact_workspace_writes"); after != before {
		t.Fatal("transfer reissued grants or rewrote historical authority")
	}
	rec, err := h.loadPage(t.Context(), ws, "report")
	if err != nil {
		t.Fatal(err)
	}
	grants, err := h.loadPageGrantRecords(t.Context(), ws, rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 3 {
		t.Fatalf("stored grants %d", len(grants))
	}
	for _, grant := range grants {
		if grant.Live {
			t.Fatalf("former MEMBER owner grant still live: %+v", grant)
		}
	}
	artifactExpect(t, artifactCall(t, h, ws, "custody-next", "MEMBER", "GET", "?revision=1", ""), 200)
	artifactExpect(t, artifactCall(t, h, ws, "custody-prior-reader", "MEMBER", "GET", "?revision=1", ""), 404)
	artifactExpect(t, readAgent(), 404)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, memberPublication), 404)
}

func TestOutputCustodyListingPaginatesWithoutHidingOlderOwners(t *testing.T) {
	f := custodyRouterFixture(t, "artifact")
	for i := 0; i < 26; i++ {
		id := fmt.Sprintf("paged-%02d", i)
		wmSeedPage(t, f.db, "ws-old", id, id, id, "departing", "")
		mustExec(t, f.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,1)`, id)
	}
	path := "/api/v1/workspaces/ws-old/page-ownership?owner_user_id=departing"
	read := func(query string) ([]pageOwnershipSummary, string) {
		t.Helper()
		response := f.do(f.wsAdmin, "GET", query, "")
		wantCode(t, response, 200, "paged custody")
		var doc struct {
			Pages []pageOwnershipSummary `json:"pages"`
			Next  string                 `json:"next_cursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Pages, doc.Next
	}
	first, cursor := read(path)
	if len(first) != 25 || cursor == "" {
		t.Fatalf("first page %d cursor %q", len(first), cursor)
	}
	second, next := read(path + "&cursor=" + cursor)
	if len(second) != 2 || next != "" {
		t.Fatalf("second page %d cursor %q", len(second), next)
	}
	seen := map[string]bool{}
	for _, p := range append(first, second...) {
		if seen[p.ID] {
			t.Fatalf("duplicate %s", p.ID)
		}
		seen[p.ID] = true
		if p.OwnerUserID != "departing" {
			t.Fatalf("foreign owner %+v", p)
		}
	}
	if len(seen) != 27 || !seen["private-output"] || !seen["paged-00"] {
		t.Fatalf("unreachable older outputs %v", seen)
	}
	all, next := read(path + "&limit=100")
	if len(all) != 27 || next != "" {
		t.Fatalf("maximum page %d %q", len(all), next)
	}
	small, next := read(path + "&limit=1")
	if len(small) != 1 || next == "" {
		t.Fatal("explicit page size ignored")
	}
	for _, bad := range []string{path + "&limit=0", path + "&limit=101", path + "&limit=bad", path + "&cursor=not-a-cursor", strings.Replace(path, "departing", "reader", 1) + "&cursor=" + cursor} {
		wantCode(t, f.do(f.wsAdmin, "GET", bad, ""), 400, "invalid paging")
	}
	member := mintTokenFor(t, f.db, "reader", "custodyreader000000000000000000")
	wantCode(t, f.do(member, "GET", path, ""), 403, "member cannot list custody contacts")
}
