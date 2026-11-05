package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Private test-only probe: a newer genuine request cannot stand in for the
// exact receipt of an earlier request at its final data-read authority check.
// This uses the real SQLite writer fence and human credential/authority path;
// the wrapper only injects an audit-storage fault at an explicit boundary.
func TestDashboardAdminAuditRequestBindingFinalRecheck(t *testing.T) {
	const collection = "binding-data"
	const document = "binding-row"
	const canary = "owned-audit-binding-value"
	const admin = "binding-admin"
	h, workspace, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	body := `{"slug":"three-screens","name":"Audit binding",` + strings.TrimPrefix(dashboardBody(0, collection), "{")
	if got := call(owner, http.MethodPost, "artifacts", body); got.Code != http.StatusCreated {
		t.Fatalf("create fixture: HTTP%d", got.Code)
	}
	if got := dashboardRequest(t, h, workspace, owner, http.MethodPost, collection, "", `{"base_version":1,"documents":[{"id":"`+document+`","ifVersion":0,"value":{"secret":"`+canary+`"}}]}`); got.Code != http.StatusOK {
		t.Fatalf("seed fixture: HTTP%d", got.Code)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='three-screens'`, workspace).Scan(&page); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
	pagesSeedUser(t, h, workspace, admin, "binding-admin@example.test", "ADMIN")
	token := mintTokenFor(t, h.db, admin, "binding-admin")
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/three-screens/artifact/data/"+collection+"?version=1", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: admin}), workspace, "ADMIN")
		r = r.WithContext(context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken))
		r.SetPathValue("slug", "three-screens")
		r.SetPathValue("collection", collection)
		return ensureDesignAdminAuditRequest(r, false)
	}
	requestA := request()
	scopeA := requestA.Context().Value(designAdminAuditKey{}).(*designAdminAuditScope)
	key := artifacts.VersionKey{WorkspaceID: workspace, PageID: page}
	if err := h.artifactProjectCheck(requestA, key, false); err != nil {
		t.Fatalf("A genuine admission: %v", err)
	}
	requestB := request()
	scopeB := requestB.Context().Value(designAdminAuditKey{}).(*designAdminAuditScope)
	if scopeA.id == scopeB.id {
		t.Fatal("independent request scopes reused audit ID")
	}
	responseB := httptest.NewRecorder()
	h.GetArtifactData(responseB, requestB)
	if responseB.Code != http.StatusOK || !strings.Contains(responseB.Body.String(), canary) {
		t.Fatalf("B genuine read calibration: HTTP%d", responseB.Code)
	}
	// Both rows are real, committed, same actor/page/action/metadata. Only their
	// request identity/timestamp differs. Never race a second DB writer while
	// A holds its fence: B completes between A's admission and data transaction.
	receiptMetadata := func(id string) string {
		t.Helper()
		var metadata string
		if err := h.db.QueryRow(`SELECT metadata FROM audit_logs WHERE id=? AND workspace_id=? AND user_id=? AND entity_id=? AND action=?`, id, workspace, admin, page, auditActionPrivateArtifactRead).Scan(&metadata); err != nil {
			t.Fatalf("genuine audit row missing: %v", err)
		}
		return metadata
	}
	beforeA, beforeB := receiptMetadata(scopeA.id), receiptMetadata(scopeB.id)
	if beforeA != beforeB {
		t.Fatal("calibration requires same audit payload with distinct request IDs")
	}
	store := artifacts.NewDataStore(h.db, artifacts.DataOptions{Enabled: true})
	authA, _ := h.artifactDataAuthority(requestA, key, collection, 1, false)
	calibrated, err := store.Read(requestA.Context(), key, collection, authA)
	if err != nil || len(calibrated) != 1 || calibrated[0].ID != document || !strings.Contains(string(calibrated[0].Value), canary) {
		t.Fatalf("A remains readable after B with intact exact receipt: %v", err)
	}
	mustExec(t, h.db, `CREATE TABLE private_test_ack_probe(n INTEGER NOT NULL)`)
	mustExec(t, h.db, `INSERT INTO private_test_ack_probe VALUES(0)`)
	mustExec(t, h.db, `CREATE TABLE private_test_ack_target(id TEXT NOT NULL)`)
	mustExec(t, h.db, `INSERT INTO private_test_ack_target(id) VALUES(?)`, scopeA.id)
	calls := 0
	faulted := false
	wrapped := func(ctx context.Context, tx *sql.Tx) (string, error) {
		calls++
		if calls == 2 {
			// VersionStore invokes this callback after its data function, so
			// rows were buffered but have not escaped the transaction.
			var rows int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_data_documents WHERE workspace_id=? AND page_id=? AND collection=? AND id=?`, workspace, page, collection, document).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("final-boundary fixture unavailable: rows=%d err=%v", rows, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE private_test_ack_probe SET n=n+1`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM audit_logs WHERE id=?`, scopeA.id); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `CREATE TRIGGER private_test_refuse_A BEFORE INSERT ON audit_logs WHEN NEW.id=(SELECT id FROM private_test_ack_target) BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			var intactB int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE id=?`, scopeB.id).Scan(&intactB); err != nil || intactB != 1 {
				t.Fatal("B must remain a genuine separate receipt at the fault boundary")
			}
			faulted = true
		}
		return authA(ctx, tx)
	}
	got, err := store.Read(requestA.Context(), key, collection, wrapped)
	if calls != 2 || !faulted {
		t.Fatalf("fault must reach final authority only: calls=%d faulted=%t", calls, faulted)
	}
	if err == nil || got != nil {
		t.Fatalf("request A accepted another request's receipt or returned buffered content: err=%v resultCount=%d", err, len(got))
	}
	var probe int
	if err := h.db.QueryRow(`SELECT n FROM private_test_ack_probe`).Scan(&probe); err != nil || probe != 0 {
		t.Fatalf("failed final proof did not roll back its probe: n=%d err=%v", probe, err)
	}
	if receiptMetadata(scopeA.id) != beforeA || receiptMetadata(scopeB.id) != beforeB {
		t.Fatal("failed A verification corrupted original A or independent B receipt")
	}
	// Trigger creation/deletion and corruption were in the refused transaction;
	// verify real A access recovers without any manual repair or new identity.
	after, err := store.Read(requestA.Context(), key, collection, authA)
	if err != nil || len(after) != 1 || !strings.Contains(string(after[0].Value), canary) {
		t.Fatalf("A recalibration after rollback: %v", err)
	}
}
