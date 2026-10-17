package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Adapted from the private-output custody regressions: handlers use migrated
// SQLite, installed Document/Slides persistence and otherwise synthetic extensions,
// without publishing content or agents.
func ownershipFixture(t *testing.T, table string) (*pagesTransferRig, *WorkspaceHandler) {
	t.Helper()
	r := pagesTransferSetup(t)
	r.seedPage(t, "private-output", "private-output", "Private output", r.userID, "")
	seedOwnershipOutput(t, r.db, r.wsID, "private-output", table)
	mustExec(t, r.db, `INSERT INTO users(id,email) VALUES('reader','reader@example.test'),('ungranted','ungranted@example.test')`)
	mustExec(t, r.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('reader-member',?,'reader','MEMBER'),('ungranted-member',?,'ungranted','MEMBER')`, r.wsID, r.wsID)
	mustExec(t, r.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('private-output','user','reader','read',?)`, r.userID)
	h := NewWorkspaceHandler(r.db, newTestLogger())
	h.SetJournal(r.spy)
	return r, h
}

const ownershipArtifactContentQuery = `(SELECT sha256 FROM page_artifact_revisions r WHERE r.page_id=page_artifacts.page_id AND r.revision=page_artifacts.head_rev)`
const ownershipBoardContentQuery = `(SELECT json_extract(element_json,'$.text') FROM page_board_elements e WHERE e.board_page_id=page_boards.page_id AND e.element_id=page_boards.page_id || '-private-element')`

func seedOwnershipOutput(t *testing.T, db *sql.DB, workspace, pageID, table string) {
	t.Helper()
	var installed bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name=?)`, table).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	if !installed {
		mustExec(t, db, `CREATE TABLE `+table+`(page_id TEXT PRIMARY KEY REFERENCES pages(id), private_content TEXT)`)
	}
	switch ownershipContentColumn(t, db, table) {
	case ownershipArtifactContentQuery:
		content := []byte("NEVER_EXPOSE_THIS")
		root := t.TempDir()
		sha, _, err := storeAttachmentBlob(root, workspace, content)
		if err != nil {
			t.Fatal(err)
		}
		// Custody retains the actual immutable HTML bytes, not a synthetic
		// column on the production head. Check them before TempDir cleanup.
		t.Cleanup(func() {
			got, err := readAttachmentBlob(root, workspace, sha)
			if err != nil || !bytes.Equal(got, content) {
				t.Errorf("private Artifact bytes changed: %v", err)
			}
		})
		manifest, err := json.Marshal(map[string]any{"format_version": 1, "runtime_profile": "static-html-v1", "entrypoint": "index.html", "files": []any{map[string]any{"path": "index.html", "mime": "text/html; charset=utf-8", "size_bytes": len(content), "sha256": sha}}, "source": nil})
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,1)`, pageID)
		mustExec(t, db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at,manifest_json) VALUES(?,?,1,?,'ownership-seed',?,'2026-10-17T00:00:00Z',?)`, pageID+"-private-revision", pageID, sha, attachmentDigest([]byte("ownership-seed")), string(manifest))
	case ownershipBoardContentQuery:
		// Closed Excalidraw text element from the pinned public fixture,
		// detached from its original container and retaining private text.
		var element map[string]any
		if err := json.Unmarshal([]byte(`{"id":"private-element","type":"text","x":0,"y":200,"width":133.837890625,"height":25,"angle":0,"strokeColor":"#1e1e1e","backgroundColor":"transparent","fillStyle":"solid","strokeWidth":2,"strokeStyle":"solid","roughness":1,"opacity":100,"groupIds":[],"frameId":null,"index":"a3","roundness":null,"seed":153151818,"version":2,"versionNonce":572473366,"isDeleted":false,"boundElements":null,"updated":1791914664268,"link":null,"locked":false,"text":"NEVER_EXPOSE_THIS","fontSize":20,"fontFamily":5,"textAlign":"left","verticalAlign":"top","containerId":null,"originalText":"NEVER_EXPOSE_THIS","autoResize":true,"lineHeight":1.25}`), &element); err != nil {
			t.Fatal(err)
		}
		element["id"] = pageID + "-private-element"
		raw, err := json.Marshal(element)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, db, `INSERT INTO page_boards(page_id,seq,format) VALUES(?,1,'crewship-board-excalidraw-0.18.1-v1')`, pageID)
		mustExec(t, db, `INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,updated_seq) VALUES(?,?,1,?,1)`, pageID, pageID+"-private-element", string(raw))
	case "snapshot":
		mustExec(t, db, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,?,1,?,?)`, workspace, pageID, []byte("NEVER_EXPOSE_THIS"), []byte("[]"))
	case "canonical_json":
		content := []byte("NEVER_EXPOSE_THIS")
		mustExec(t, db, `INSERT INTO slides_heads(workspace_id,page_id,schema_version,head_rev,order_version,canonical_json,content_hash) VALUES(?,?,1,1,1,?,?)`, workspace, pageID, content, fmt.Sprintf("%x", sha256.Sum256(content)))
	default:
		mustExec(t, db, `INSERT INTO `+table+`(page_id,private_content) VALUES(?,?)`, pageID, "NEVER_EXPOSE_THIS")
	}
}

func ownershipContentColumn(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	column := map[string]string{"documents": "snapshot", "slides_heads": "canonical_json", "page_artifacts": "head_rev", "page_boards": "seq"}[table]
	if column != "" {
		var installed bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name=?)`, table, column).Scan(&installed); err != nil {
			t.Fatal(err)
		}
		if installed {
			if table == "page_artifacts" {
				return ownershipArtifactContentQuery
			}
			if table == "page_boards" {
				return ownershipBoardContentQuery
			}
			return column
		}
	}
	return "private_content"
}

func ownershipCall(t *testing.T, r *pagesTransferRig, h *WorkspaceHandler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := ownershipRequest(t, r, method, target, body)
	request.SetPathValue("pageID", "private-output")
	w := httptest.NewRecorder()
	if method == "GET" {
		h.ListPageOwnership(w, request)
	} else {
		h.TransferPageOwnership(w, request)
	}
	return w
}

func TestPrivateOwnershipExplicitTransferPreservesContentAndGrants(t *testing.T) {
	for _, table := range []string{"page_artifacts", "page_boards", "documents", "slides_heads"} {
		t.Run(table, func(t *testing.T) {
			r, h := ownershipFixture(t, table)
			list := ownershipCall(t, r, h, "GET", "/?owner_user_id="+r.userID, "")
			wantCode(t, list, 200, "custody metadata")
			var listing WorkspacePageOwnershipListResponse
			if err := json.Unmarshal(list.Body.Bytes(), &listing); err != nil {
				t.Fatal(err)
			}
			kind := map[string]string{"page_artifacts": "artifact", "page_boards": "board", "documents": "document", "slides_heads": "slides"}[table]
			if len(listing.Pages) != 1 || listing.Pages[0].ID != "private-output" || listing.Pages[0].Type != kind {
				t.Fatalf("listing %+v", listing)
			}
			eligible := map[string]bool{}
			for _, c := range listing.Pages[0].EligibleSuccessors {
				eligible[c.UserID] = true
			}
			if !eligible["reader"] || !eligible[r.adminID] || eligible["ungranted"] || eligible[r.userID] {
				t.Fatalf("eligibility %v", eligible)
			}
			if strings.Contains(list.Body.String(), "NEVER_EXPOSE_THIS") || list.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("custody listing disclosed content or cached contacts")
			}
			for _, successor := range []string{"ungranted", r.userID} {
				denied := ownershipCall(t, r, h, "POST", "/", fmt.Sprintf(`{"expected_owner_user_id":%q,"successor_user_id":%q}`, r.userID, successor))
				wantCode(t, denied, 403, "successor must already read output")
			}
			transfer := ownershipCall(t, r, h, "POST", "/", fmt.Sprintf(`{"expected_owner_user_id":%q,"successor_user_id":"reader"}`, r.userID))
			wantCode(t, transfer, 200, "explicit human transfer")
			var owner, content, issuer string
			if err := r.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id='private-output'`).Scan(&owner); err != nil || owner != "reader" {
				t.Fatalf("owner %s %v", owner, err)
			}
			wantContent := "NEVER_EXPOSE_THIS"
			if ownershipContentColumn(t, r.db, table) == ownershipArtifactContentQuery {
				wantContent = attachmentDigest([]byte(wantContent))
			}
			if err := r.db.QueryRow(`SELECT ` + ownershipContentColumn(t, r.db, table) + ` FROM ` + table + ` WHERE page_id='private-output'`).Scan(&content); err != nil || content != wantContent {
				t.Fatalf("private content changed %s %v", content, err)
			}
			if err := r.db.QueryRow(`SELECT granted_by_user_id FROM page_grants WHERE page_id='private-output'`).Scan(&issuer); err != nil || issuer != r.userID {
				t.Fatalf("grant issuer changed %s %v", issuer, err)
			}
			ph := NewPageHandler(r.db, nil, newTestLogger())
			rec, err := ph.loadPage(t.Context(), r.wsID, "private-output")
			if err != nil {
				t.Fatal(err)
			}
			grants, err := ph.loadPageGrantRecords(t.Context(), r.wsID, rec)
			if err != nil || len(grants) != 1 || grants[0].Live {
				t.Fatalf("former member-owner grant must become inert: %+v %v", grants, err)
			}
			if len(r.spy.entries) != 1 || r.spy.entries[0].Payload["to_user_id"] != "reader" || strings.Contains(r.spy.entries[0].Summary, "private-output") {
				t.Fatalf("private transfer journal %+v", r.spy.entries)
			}
			stale := ownershipCall(t, r, h, "POST", "/", fmt.Sprintf(`{"expected_owner_user_id":%q,"successor_user_id":%q}`, r.userID, r.adminID))
			wantCode(t, stale, 409, "ownership compare-and-swap")
		})
	}
}

func TestPrivateOwnershipInboxFailureRollsBackTransfer(t *testing.T) {
	r, h := ownershipFixture(t, "documents")
	mustExec(t, r.db, `CREATE TRIGGER reject_custody_notice BEFORE INSERT ON inbox_items BEGIN SELECT RAISE(ABORT, 'custody notice rejected'); END`)
	response := ownershipCall(t, r, h, "POST", "/", fmt.Sprintf(`{"expected_owner_user_id":%q,"successor_user_id":"reader"}`, r.userID))
	wantCode(t, response, 500, "custody notice must commit atomically")
	var owner string
	if err := r.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id='private-output'`).Scan(&owner); err != nil || owner != r.userID {
		t.Fatalf("failed notice changed custody: owner %s error %v", owner, err)
	}
	var notices int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='private-output'`).Scan(&notices); err != nil || notices != 0 || len(r.spy.entries) != 0 {
		t.Fatalf("failed notice emitted inbox or journal: notices %d journal %d error %v", notices, len(r.spy.entries), err)
	}
}

type ownershipChangingBody struct {
	io.Reader
	once   sync.Once
	change func()
}

func (b *ownershipChangingBody) Read(p []byte) (int, error) {
	b.once.Do(b.change)
	return b.Reader.Read(p)
}
func (b *ownershipChangingBody) Close() error { return nil }

func TestPrivateOwnershipTransferRechecksAfterBodyRead(t *testing.T) {
	for _, change := range []string{"actor-demoted", "actor-removed", "actor-suspended", "workspace-deleted", "successor-removed", "successor-grant-revoked", "successor-suspended", "successor-restricted"} {
		t.Run(change, func(t *testing.T) {
			r, h := ownershipFixture(t, "documents")
			body := fmt.Sprintf(`{"expected_owner_user_id":%q,"successor_user_id":"reader"}`, r.userID)
			request := ownershipRequest(t, r, "POST", "/", body)
			request.SetPathValue("pageID", "private-output")
			request.Body = &ownershipChangingBody{Reader: strings.NewReader(body), change: func() {
				switch change {
				case "actor-demoted":
					mustExec(t, r.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, r.adminID)
				case "actor-removed":
					mustExec(t, r.db, `DELETE FROM workspace_members WHERE user_id=?`, r.adminID)
				case "actor-suspended":
					mustExec(t, r.db, `UPDATE users SET suspended_at='2026-10-16T00:00:00Z' WHERE id=?`, r.adminID)
				case "workspace-deleted":
					mustExec(t, r.db, `UPDATE workspaces SET deleted_at='2026-10-16T00:00:00Z' WHERE id=?`, r.wsID)
				case "successor-removed":
					mustExec(t, r.db, `DELETE FROM workspace_members WHERE user_id='reader'`)
				case "successor-grant-revoked":
					mustExec(t, r.db, `DELETE FROM page_grants WHERE subject_id='reader'`)
				case "successor-suspended":
					mustExec(t, r.db, `UPDATE users SET suspended_at='2026-10-16T00:00:00Z' WHERE id='reader'`)
				case "successor-restricted":
					mustExec(t, r.db, `INSERT INTO workspaces(id,name,slug) VALUES('restricted-other','Other','restricted-other')`)
					mustExec(t, r.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('restricted-reader','restricted-other','reader','MEMBER','restricted')`)
				}
			}}
			w := httptest.NewRecorder()
			h.TransferPageOwnership(w, request)
			wantCode(t, w, 403, "live custody authority")
			var owner string
			if err := r.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id='private-output'`).Scan(&owner); err != nil || owner != r.userID {
				t.Fatalf("refusal changed custody %s %v", owner, err)
			}
			if len(r.spy.entries) != 0 {
				t.Fatal("denied transfer journaled")
			}
		})
	}
}

func TestPrivateOwnershipListingPaginatesAndScopesCursor(t *testing.T) {
	r, h := ownershipFixture(t, "page_artifacts")
	for i := 0; i < 26; i++ {
		id := fmt.Sprintf("paged-%02d", i)
		r.seedPage(t, id, id, id, r.userID, "")
		mustExec(t, r.db, `INSERT INTO page_artifacts VALUES(?,'private')`, id)
	}
	path := "/?owner_user_id=" + r.userID
	read := func(path string) WorkspacePageOwnershipListResponse {
		t.Helper()
		w := ownershipCall(t, r, h, "GET", path, "")
		wantCode(t, w, 200, "paged custody")
		var doc WorkspacePageOwnershipListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	first := read(path)
	if len(first.Pages) != 25 || first.NextCursor == "" {
		t.Fatalf("first page %+v", first)
	}
	second := read(path + "&cursor=" + first.NextCursor)
	if len(second.Pages) != 2 || second.NextCursor != "" {
		t.Fatalf("second page %+v", second)
	}
	for _, bad := range []string{path + "&limit=0", path + "&limit=101", path + "&cursor=bad", "/?owner_user_id=reader&cursor=" + first.NextCursor} {
		wantCode(t, ownershipCall(t, r, h, "GET", bad, ""), 400, "invalid ownership paging")
	}
}

func TestPrivateOwnershipNoExtensionsReturnsEmptyList(t *testing.T) {
	r := pagesTransferSetup(t)
	h := NewWorkspaceHandler(r.db, newTestLogger())
	r.seedPage(t, "ordinary", "ordinary", "Ordinary Page", r.userID, "")
	w := ownershipCall(t, r, h, "GET", "/", "")
	wantCode(t, w, 200, "main baseline custody list")
	if strings.TrimSpace(w.Body.String()) != `{"pages":[]}` {
		t.Fatalf("ordinary Page exposed as private output: %s", w.Body.String())
	}
}

func TestPrivateOwnershipConcurrentSelectionsCommitOne(t *testing.T) {
	r, h := ownershipFixture(t, "documents")
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, successor := range []string{"reader", r.adminID} {
		request := ownershipRequest(t, r, "POST", "/", fmt.Sprintf(`{"expected_owner_user_id":%q,"successor_user_id":%q}`, r.userID, successor))
		request.SetPathValue("pageID", "private-output")
		go func() {
			<-start
			response := httptest.NewRecorder()
			h.TransferPageOwnership(response, request)
			results <- response
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.Code == 409 {
		first, second = second, first
	}
	wantCode(t, first, 200, "winning custody selection")
	wantCode(t, second, 409, "competing custody selection")
	var notices int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='private-output'`).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("committed custody notices: %d %v", notices, err)
	}
}

func TestPrivateOwnershipEndpointsRequireHumanAuthentication(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, header := range []string{"X-Internal-Token", actingAgentSlugHeader} {
			t.Run(method+"/"+header, func(t *testing.T) {
				r, h := ownershipFixture(t, "documents")
				request := ownershipRequest(t, r, method, "/", fmt.Sprintf(`{"expected_owner_user_id":%q,"successor_user_id":"reader"}`, r.userID))
				request.Header.Set(header, "synthetic-agent")
				request.SetPathValue("pageID", "private-output")
				response := httptest.NewRecorder()
				if method == "GET" {
					h.ListPageOwnership(response, request)
				} else {
					h.TransferPageOwnership(response, request)
				}
				wantCode(t, response, 401, "agent-shaped caller cannot administer custody")
			})
		}
	}
}

// Direct handler fixtures carry a real synthetic session row and its matching
// trusted context. Unknown authentication kinds are never a test bypass.
func ownershipRequest(t *testing.T, r *pagesTransferRig, method, target, body string) *http.Request {
	t.Helper()
	request := pagesRequest(t, method, target, r.wsID, r.adminID, "OWNER", body)
	session, err := sessions.NewDBStore(r.db).Create(t.Context(), r.adminID, "custody-test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(request.Context(), ctxAuthKind, AuthKindSession)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: r.adminID, SessionID: session.ID})
	return request.WithContext(ctx)
}
