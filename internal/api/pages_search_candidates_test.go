package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Opaque snapshots here test only header authority, not Docs materialization.
// Real managed-source projection/Router gates follow the candidate checkpoint.
func seedSearchHeaderDocument(t *testing.T, h *PageHandler, workspace, id, owner string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, id, workspace, id, id, owner)
	execOrFatal(t, h.db, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,?,2,x'00','[]')`, workspace, id)
}

func searchCandidatesFor(t *testing.T, h *PageHandler, r *http.Request) ([]nativeSearchCandidate, error) {
	t.Helper()
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return h.nativeSearchCandidates(r, tx)
}

func TestNativeSearchCandidatesAuthorizeBeforeBudgetAndPreserveVisibleOrder(t *testing.T) {
	h, workspace, owner := pagesAccessFixture(t)
	pagesSeedUser(t, h, workspace, "search-viewer", "search-viewer@example.test", "MEMBER")
	seedSearchHeaderDocument(t, h, workspace, "a-visible", "search-viewer")
	seedSearchHeaderDocument(t, h, workspace, "z-visible", "search-viewer")
	// Hidden rows sort between visible rows, exceeding the authorized limit by
	// themselves. Neither output nor the limit depends on their presence.
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("m-hidden-%03d", i)
		if _, err = tx.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, id, workspace, id, "Hidden title canary", owner); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,?,2,x'00','[]')`, workspace, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	request := pageAssetHumanRequest(t, h, "GET", "/api/v1/pages/search/content", workspace, "search-viewer", "")
	candidates, err := searchCandidatesFor(t, h, request)
	if err != nil || len(candidates) != 2 || candidates[0].PageID != "a-visible" || candidates[1].PageID != "z-visible" {
		t.Fatalf("visible candidates %+v, %v", candidates, err)
	}
	ownerRequest := pageAssetHumanRequest(t, h, "GET", "/api/v1/pages/search/content", workspace, owner, "")
	if candidates, err = searchCandidatesFor(t, h, ownerRequest); !errors.Is(err, errNativeSearchCandidates) || candidates != nil {
		t.Fatalf("authorized cap ignored: %d %v", len(candidates), err)
	}
}

func TestNativeSearchCandidatesUseActualReadTransactionAndCredential(t *testing.T) {
	h, workspace, owner := pagesAccessFixture(t)
	const page = "search-granted-native"
	seedSearchHeaderDocument(t, h, workspace, page, owner)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','dave','read',?)`, page, owner)
	request := pageAssetHumanRequest(t, h, "GET", "/api/v1/pages/search/content", workspace, "dave", "")
	// A fallback h.db query while this sole connection is held would time out.
	h.db.SetMaxOpenConns(1)
	tx, err := h.db.BeginTx(request.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.nativeSearchCandidates(request, tx)
	if err != nil || len(before) != 1 || before[0].PageID != page {
		t.Fatalf("live grant candidates %+v %v", before, err)
	}
	if _, err = tx.Exec(`DELETE FROM page_grants WHERE page_id=? AND subject_id='dave'`, page); err != nil {
		t.Fatal(err)
	}
	after, err := h.nativeSearchCandidates(request, tx)
	if err != nil || len(after) != 0 {
		t.Fatalf("reader escaped transaction: %+v %v", after, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	restored, err := searchCandidatesFor(t, h, request)
	if err != nil || len(restored) != 1 {
		t.Fatalf("rollback grant not restored %+v %v", restored, err)
	}
	if err = sessions.NewDBStore(h.db).Revoke(t.Context(), UserFromContext(request.Context()).SessionID, "synthetic search revocation"); err != nil {
		t.Fatal(err)
	}
	if _, err = searchCandidatesFor(t, h, request); !errors.Is(err, errNativeSearchAuthentication) {
		t.Fatalf("revoked exact session admitted: %v", err)
	}
}

func TestNativeSearchCandidatesCrewShareAndLeafACL(t *testing.T) {
	f := newFoldersFixture(t)
	pagesSeedUser(t, f.h, f.wsID, "search-crew-reader", "search-crew-reader@example.test", "MEMBER")
	pagesJoinCrew(t, f.h, "search-crew-reader", "crew-lookout")
	parent := f.createFolder(t, "search-private-parent", "Hidden ancestor canary", "crew/lookout")
	leaf := f.createFolder(t, "search-leaf", "Visible leaf", "crew/engine")
	execOrFatal(t, f.h.db, `UPDATE page_folders SET parent_id=? WHERE id=?`, parent["id"], leaf["id"])
	const page = "search-leaf-native"
	seedSearchHeaderDocument(t, f.h, f.wsID, page, "alice")
	f.file(t, "search-leaf", page)
	// Native custody excludes legacy panels. Use the real native equivalent,
	// an explicit crew Page share, while keeping the shared canSeePage policy.
	execOrFatal(t, f.h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'crew','crew-lookout','read','alice')`, page)
	crew := pageAssetHumanRequest(t, f.h, "GET", "/api/v1/pages/search/content", f.wsID, "search-crew-reader", "")
	if got, err := searchCandidatesFor(t, f.h, crew); err != nil || len(got) != 1 {
		t.Fatalf("native crew share lost: %+v %v", got, err)
	}
	// frank starts in neither crew in newFoldersFixture; direct leaf share only.
	f.share(t, "search-leaf", "user", "frank", false)
	request := pageAssetHumanRequest(t, f.h, "GET", "/api/v1/pages/search/content", f.wsID, "frank", "")
	if got, err := searchCandidatesFor(t, f.h, request); err != nil || len(got) != 1 || got[0].PageID != page {
		t.Fatalf("leaf reach lost: %+v %v", got, err)
	}
	f.unshare(t, "search-leaf", "user", "frank")
	f.share(t, "search-private-parent", "user", "frank", false)
	if got, err := searchCandidatesFor(t, f.h, request); err != nil || len(got) != 0 {
		t.Fatalf("ancestor ACL inherited: %+v %v", got, err)
	}
}
