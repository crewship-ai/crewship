package api

import (
	"context"
	"errors"
	"testing"
)

// Private outputs keep human custody even when their owner belongs to a crew.
// The synthetic extension tables isolate this contract from output lifecycle.
func TestPrivateOwnershipDepartureRequiresHumanSuccessor(t *testing.T) {
	for _, table := range []string{"page_artifacts", "page_boards", "documents", "slides_heads"} {
		t.Run(table, func(t *testing.T) {
			r := pagesTransferSetup(t)
			r.seedCrew(t, "custody-crew", "Custody crew", "custody-crew")
			r.seedCrewMember(t, "custody-crew", r.userID)
			r.seedPage(t, "private-page", "private-page", "Private output", r.userID, "")
			seedOwnershipOutput(t, r.db, r.wsID, "private-page", table)
			tx, err := r.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = transferDepartingUserPagesTx(context.Background(), tx, r.wsID, r.userID)
			var conflict *ErrPagesNeedManualTransfer
			if !errors.As(err, &conflict) {
				t.Fatalf("private output auto-transferred instead of requiring explicit human custody: %v", err)
			}
			var owner string
			if err := tx.QueryRow(`SELECT owner_user_id FROM pages WHERE id='private-page'`).Scan(&owner); err != nil || owner != r.userID {
				t.Fatalf("custody changed on refusal: %s %v", owner, err)
			}
		})
	}
}

func TestPrivateOwnershipPlainPagesKeepCrewSuccession(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedCrew(t, "plain-crew", "Plain crew", "plain-crew")
	r.seedCrewMember(t, "plain-crew", r.userID)
	r.seedPage(t, "plain-page", "plain-page", "Panel Page", r.userID, "")
	tx, err := r.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	results, err := transferDepartingUserPagesTx(context.Background(), tx, r.wsID, r.userID)
	if err != nil || len(results) != 1 || results[0].ToCrewID != "plain-crew" {
		t.Fatalf("ordinary Page succession changed: %+v %v", results, err)
	}
}

func TestPrivateOwnershipExistingInvalidSchemaFailsClosed(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedCrew(t, "schema-crew", "Schema crew", "schema-crew")
	r.seedCrewMember(t, "schema-crew", r.userID)
	r.seedPage(t, "invalid-schema-page", "invalid-schema-page", "Private output", r.userID, "")
	// Replace only this isolated fixture database's installed schema.
	mustExec(t, r.db, `DROP TABLE IF EXISTS documents`)
	mustExec(t, r.db, `CREATE TABLE documents(id TEXT PRIMARY KEY, body TEXT)`)
	_, _, err := privatePageOwnershipSQL(t.Context(), r.db, "pages")
	if err == nil {
		t.Error("existing private output schema without page_id was treated as absent")
	}
	response := ownershipCall(t, r, NewWorkspaceHandler(r.db, newTestLogger()), "GET", "/", "")
	wantCode(t, response, 500, "invalid private output schema refuses custody listing")
	tx, err := r.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := transferDepartingUserPagesTx(t.Context(), tx, r.wsID, r.userID); err == nil {
		t.Fatal("invalid schema allowed owner departure")
	}
	var owner string
	if err := tx.QueryRow(`SELECT owner_user_id FROM pages WHERE id='invalid-schema-page'`).Scan(&owner); err != nil || owner != r.userID {
		t.Fatalf("invalid schema changed custody %s %v", owner, err)
	}
}
