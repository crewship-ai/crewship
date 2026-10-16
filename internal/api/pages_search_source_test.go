package api

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/pagesearch"
)

// Source headers isolate the real credential/revision CAS here. Managed Yjs
// materialization is a later separate gate, not established by opaque snapshots.
func TestNativeSearchSourceGuardRechecksAfterCandidateAndAtCacheRead(t *testing.T) {
	h, workspace, owner := pagesAccessFixture(t)
	const page = "search-source-native"
	seedSearchHeaderDocument(t, h, workspace, page, owner)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','dave','read',?)`, page, owner)
	request := pageAssetHumanRequest(t, h, "GET", "/api/v1/pages/search/content", workspace, "dave", "")
	candidates, err := searchCandidatesFor(t, h, request)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidate: %+v %v", candidates, err)
	}
	guard := h.nativeSearchGuard(request, page)
	tx, err := h.db.BeginTx(request.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	source, err := guard(request.Context(), tx)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := pagesearch.FromDocument(source, []pagesearch.DocumentBlock{}, []pagesearch.BlockHash{})
	if err != nil {
		t.Fatal(err)
	}
	store := pagesearch.NewStore(h.db)
	if err = store.Publish(request.Context(), projection, guard); err != nil {
		t.Fatal(err)
	}
	// A changed head revision after the former materialization stamp cannot
	// publish old text. This isolates the durable CAS; actual managed human
	// mutation/materialization is a later integration gate.
	execOrFatal(t, h.db, `UPDATE documents SET head_rev=head_rev+1 WHERE page_id=?`, page)
	if err = store.Publish(request.Context(), projection, guard); !errors.Is(err, pagesearch.ErrSourceChanged) {
		t.Fatalf("changed head published old projection: %v", err)
	}
	execOrFatal(t, h.db, `UPDATE documents SET head_rev=head_rev-1 WHERE page_id=?`, page)
	// A source change after the former candidate/read cannot publish old text.
	execOrFatal(t, h.db, `UPDATE documents SET generation=generation+1 WHERE page_id=?`, page)
	if err = store.Publish(request.Context(), projection, guard); !errors.Is(err, pagesearch.ErrSourceChanged) {
		t.Fatalf("stale source published: %v", err)
	}
	tx, err = h.db.BeginTx(request.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(request.Context(), tx, source, guard); !errors.Is(err, pagesearch.ErrSourceChanged) {
		t.Fatalf("stale cache admitted: %v", err)
	}
	_ = tx.Rollback()
	execOrFatal(t, h.db, `UPDATE documents SET generation=generation-1 WHERE page_id=?`, page)
	execOrFatal(t, h.db, `UPDATE documents SET schema_version=3 WHERE page_id=?`, page)
	if err = store.Publish(request.Context(), projection, guard); !errors.Is(err, pagesearch.ErrSourceChanged) {
		t.Fatalf("schema change published old projection: %v", err)
	}
	execOrFatal(t, h.db, `UPDATE documents SET schema_version=2 WHERE page_id=?`, page)
	// Revoking only the real session, keeping membership/grants, is sufficient.
	if err = sessions.NewDBStore(h.db).Revoke(t.Context(), UserFromContext(request.Context()).SessionID, "synthetic search CAS revocation"); err != nil {
		t.Fatal(err)
	}
	if err = store.Publish(request.Context(), projection, guard); !errors.Is(err, errNativeSearchAuthentication) {
		t.Fatalf("revoked publication credential admitted: %v", err)
	}
	tx, err = h.db.BeginTx(request.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = store.Load(request.Context(), tx, source, guard); !errors.Is(err, errNativeSearchAuthentication) {
		t.Fatalf("cache read skipped exact credential: %v", err)
	}
}

func TestNativeSearchPublicationRejectsRevokedPageShareWithoutNewRows(t *testing.T) {
	h, workspace, owner := pagesAccessFixture(t)
	const page = "search-share-native"
	seedSearchHeaderDocument(t, h, workspace, page, owner)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','dave','read',?)`, page, owner)
	request := pageAssetHumanRequest(t, h, "GET", "/api/v1/pages/search/content", workspace, "dave", "")
	guard := h.nativeSearchGuard(request, page)
	tx, err := h.db.BeginTx(request.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	source, err := guard(request.Context(), tx)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := pagesearch.FromDocument(source, []pagesearch.DocumentBlock{}, []pagesearch.BlockHash{})
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='dave'`, page)
	if err = pagesearch.NewStore(h.db).Publish(request.Context(), projection, guard); !errors.Is(err, pagesearch.ErrSourceChanged) {
		t.Fatalf("revoked Page reach published: %v", err)
	}
	var count int
	if err = h.db.QueryRow(`SELECT count(*) FROM page_search_projections`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked publication wrote %d rows %v", count, err)
	}
}
