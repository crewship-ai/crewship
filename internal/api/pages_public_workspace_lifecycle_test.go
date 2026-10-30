package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPublicView_WorkspaceDeletionRevokesLinks(t *testing.T) {
	h, _, _, workspaceID, userID := newPagesPublicFixture(t, "deleted-public-page")
	link := pagesPublish(t, h, workspaceID, userID, "deleted-public-page", `{}`)
	token := link["token"].(string)
	if rr := pagesPublicGet(t, h, token); rr.Code != http.StatusOK {
		t.Fatalf("baseline public view: %d %s", rr.Code, rr.Body.String())
	}
	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := softDeleteWorkspaceTx(context.Background(), tx, workspaceID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if rr := pagesPublicGet(t, h, token); rr.Code != http.StatusNotFound {
		t.Fatalf("public view after workspace deletion: %d %s", rr.Code, rr.Body.String())
	}
	var revoked sql.NullString
	if err := h.db.QueryRow(`SELECT revoked_at FROM page_public_tokens WHERE id=?`, link["id"]).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked.Valid {
		t.Fatal("workspace deletion left its public link unrevoked")
	}
}

func TestPublicView_RefusesPreviouslyDeletedWorkspace(t *testing.T) {
	h, _, _, workspaceID, userID := newPagesPublicFixture(t, "legacy-deleted-page")
	link := pagesPublish(t, h, workspaceID, userID, "legacy-deleted-page", `{}`)
	// Simulate a workspace deleted before link revocation was part of the cascade.
	if _, err := h.db.Exec(`UPDATE workspaces SET deleted_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339), workspaceID); err != nil {
		t.Fatal(err)
	}
	if rr := pagesPublicGet(t, h, link["token"].(string)); rr.Code != http.StatusNotFound {
		t.Fatalf("old orphan link remained accessible: %d %s", rr.Code, rr.Body.String())
	}
}

func TestWorkspaceDeletePublicLinks_RollsBackWithCascade(t *testing.T) {
	h, _, _, workspaceID, userID := newPagesPublicFixture(t, "rollback-public-page")
	link := pagesPublish(t, h, workspaceID, userID, "rollback-public-page", `{}`)
	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := softDeleteWorkspaceTx(context.Background(), tx, workspaceID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if rr := pagesPublicGet(t, h, link["token"].(string)); rr.Code != http.StatusOK {
		t.Fatalf("rolled-back deletion revoked link: %d %s", rr.Code, rr.Body.String())
	}
	var revoked sql.NullString
	if err := h.db.QueryRow(`SELECT revoked_at FROM page_public_tokens WHERE id=?`, link["id"]).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked.Valid {
		t.Fatal("revocation escaped rollback")
	}
}

func TestWorkspaceDeletePublicLinks_PreservesOtherWorkspaceAndPriorRevocation(t *testing.T) {
	h, _, _, workspaceID, userID := newPagesPublicFixture(t, "scoped-public-page")
	link := pagesPublish(t, h, workspaceID, userID, "scoped-public-page", `{}`)
	if _, err := h.db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('other-public-ws','Other','other-public-ws')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('other-public-page','other-public-ws','other-public-page','Other',?,'{}')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO page_public_tokens(id,page_id,token_hash,expires_at,created_by_user_id) VALUES('other-public-link','other-public-page',?,'2999-01-01T00:00:00Z',?)`, hashPagePublicToken("other-public-token"), userID); err != nil {
		t.Fatal(err)
	}
	const priorRevoked = "2026-01-01T00:00:00Z"
	if _, err := h.db.Exec(`INSERT INTO page_public_tokens(id,page_id,token_hash,expires_at,created_by_user_id,revoked_at) SELECT 'prior-public-link',page_id,?,'2999-01-01T00:00:00Z',created_by_user_id,? FROM page_public_tokens WHERE id=?`, hashPagePublicToken("prior-public-token"), priorRevoked, link["id"]); err != nil {
		t.Fatal(err)
	}
	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := softDeleteWorkspaceTx(context.Background(), tx, workspaceID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var otherRevoked sql.NullString
	if err := h.db.QueryRow(`SELECT revoked_at FROM page_public_tokens WHERE id='other-public-link'`).Scan(&otherRevoked); err != nil {
		t.Fatal(err)
	}
	if otherRevoked.Valid {
		t.Fatal("deleting workspace revoked another workspace's link")
	}
	var prior string
	if err := h.db.QueryRow(`SELECT revoked_at FROM page_public_tokens WHERE id='prior-public-link'`).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	if prior != priorRevoked {
		t.Fatalf("changed previous revocation timestamp: %q", prior)
	}
	if rr := pagesPublicGet(t, h, "other-public-token"); rr.Code != http.StatusOK {
		t.Fatalf("other workspace became unavailable: %d %s", rr.Code, rr.Body.String())
	}
}

func TestWorkspaceDeletePublicLinks_HandlerFailureRollsBackRevocation(t *testing.T) {
	h, _, _, workspaceID, userID := newPagesPublicFixture(t, "failed-delete-public-page")
	link := pagesPublish(t, h, workspaceID, userID, "failed-delete-public-page", `{}`)
	wh := NewWorkspaceHandler(h.db, h.logger)
	seedOtherWorkspace(t, wh, "rollback-other-ws", "rollback-other", userID)
	var slug string
	if err := h.db.QueryRow(`SELECT slug FROM workspaces WHERE id=?`, workspaceID).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	// Fail after token revocation and the other cascade writes, at its final
	// workspace update. The handler must roll the entire transaction back.
	if _, err := h.db.Exec(`CREATE TRIGGER fail_workspace_delete BEFORE UPDATE OF deleted_at ON workspaces WHEN NEW.deleted_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected deletion failure'); END`); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	wh.Delete(rr, deleteReq(t, userID, workspaceID, "OWNER", slug))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("injected deletion failure: %d %s", rr.Code, rr.Body.String())
	}
	var deleted, revoked sql.NullString
	if err := h.db.QueryRow(`SELECT deleted_at FROM workspaces WHERE id=?`, workspaceID).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT revoked_at FROM page_public_tokens WHERE id=?`, link["id"]).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if deleted.Valid || revoked.Valid {
		t.Fatalf("failed deletion partially committed: workspace=%v token=%v", deleted, revoked)
	}
	if got := pagesPublicGet(t, h, link["token"].(string)); got.Code != http.StatusOK {
		t.Fatalf("failed deletion made public link unavailable: %d %s", got.Code, got.Body.String())
	}
}
