package api

import (
	"context"
	"testing"
)

func TestDocumentCLITokenLiveRevocationAndScopeUnderWriter(t *testing.T) {
	h, _, user, _ := documentAuthorityFixture(t)
	token := "crewship_cli_document_test"
	execOrFatal(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes,created_at) VALUES('doc-cli',?,'document test',?,'STANDARD','["pages:read"]','2026-10-15T00:00:00Z')`, user, hashStandard(token))
	check := func(write bool, want bool) {
		t.Helper()
		tx, err := h.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`UPDATE cli_tokens SET name=name WHERE id='doc-cli'`); err != nil {
			t.Fatal(err)
		}
		err = validateDocumentCLITokenTx(context.Background(), tx, token, user, write)
		if (err == nil) != want {
			t.Fatalf("write=%v err=%v want=%v", write, err, want)
		}
	}
	check(false, true)
	check(true, false)
	execOrFatal(t, h.db, `UPDATE cli_tokens SET scopes='["pages:*"]' WHERE id='doc-cli'`)
	check(true, true)
	execOrFatal(t, h.db, `UPDATE cli_tokens SET revoked_at='2026-10-15T00:00:00Z' WHERE id='doc-cli'`)
	check(false, false)
	execOrFatal(t, h.db, `UPDATE cli_tokens SET revoked_at=NULL,expires_at='invalid' WHERE id='doc-cli'`)
	check(false, false)
}
