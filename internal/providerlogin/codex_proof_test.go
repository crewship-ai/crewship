package providerlogin

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
)

type proofVerifier func(context.Context, string, string, string) (CodexIdentity, error)

func (f proofVerifier) Verify(ctx context.Context, id, access, account string) (CodexIdentity, error) {
	return f(ctx, id, access, account)
}
func proofFixture(t *testing.T) (*sql.DB, *CodexProofStore, string) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("31", 32))
	db, e := database.Open("file:" + filepath.Join(t.TempDir(), "proof.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if e = database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); e != nil {
		t.Fatal(e)
	}
	for _, statement := range []string{`INSERT INTO users(id,email,full_name) VALUES('h1','h1@synthetic.invalid','H1'),('h2','h2@synthetic.invalid','H2')`, `INSERT INTO workspaces(id,name,slug) VALUES('w1','W1','w1'),('w2','W2','w2')`} {
		if _, e = db.Exec(statement); e != nil {
			t.Fatal(e)
		}
	}
	encrypt := func(value string) string {
		v, e := encryption.Encrypt(value)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	access, refresh, id := encrypt("access-old"), encrypt("refresh-old"), encrypt("id-old")
	if _, e = db.Exec(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('c1','w1','Login',?,'PROVIDER_LOGIN','OPENAI','h1')`, access); e != nil {
		t.Fatal(e)
	}
	for key, value := range map[string]string{"refresh_token": refresh, "id_token": id} {
		if _, e = db.Exec(`INSERT INTO credential_fields(credential_id,key,encrypted_value,is_secret)VALUES('c1',?,?,1)`, key, value); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = db.Exec(`INSERT INTO credential_fields(credential_id,key,value,is_secret)VALUES('c1','account_id','account-own',0)`); e != nil {
		t.Fatal(e)
	}
	store := NewCodexProofStore(db.DB, proofVerifier(func(_ context.Context, id, access, account string) (CodexIdentity, error) {
		if account != "account-own" || !(id == "id-old" && access == "access-old" || id == "id-new" && access == "access-new") {
			return CodexIdentity{}, ErrUnverifiedCodexIdentity
		}
		return CodexIdentity{Issuer: openAIIssuer, ClientID: OpenAIClientID, Subject: "subject-own", AccountID: account, UserID: "user-own", Plan: "plus", AccessExpires: time.Now().Add(time.Hour), IDExpires: time.Now().Add(time.Hour)}, nil
	}))
	return db.DB, store, refresh
}
func rotateProofCiphertexts(t *testing.T, tx *sql.Tx, access, refresh, id string) {
	t.Helper()
	encrypt := func(v string) string {
		s, e := encryption.Encrypt(v)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	if _, e := tx.Exec(`UPDATE credentials SET encrypted_value=? WHERE id='c1'`, encrypt(access)); e != nil {
		t.Fatal(e)
	}
	for key, value := range map[string]string{"refresh_token": refresh, "id_token": id} {
		if _, e := tx.Exec(`UPDATE credential_fields SET encrypted_value=? WHERE credential_id='c1' AND key=?`, encrypt(value), key); e != nil {
			t.Fatal(e)
		}
	}
}
func TestCodexProofEnrollmentAndAtomicRotation(t *testing.T) {
	db, store, oldRefresh := proofFixture(t)
	if e := store.Enroll(t.Context(), "w2", "c1"); e == nil {
		t.Fatal("foreign workspace enrolled")
	}
	if e := store.Enroll(t.Context(), "w1", "c1"); e != nil {
		t.Fatal(e)
	}
	if _, e := store.Current(t.Context(), "w1", "c1"); e != nil {
		t.Fatal(e)
	}
	if _, e := store.Current(t.Context(), "w2", "c1"); e == nil {
		t.Fatal("foreign workspace got proof")
	}
	result := RefreshResult{AccessToken: "access-new", RefreshToken: "refresh-new", IDToken: "id-new"}
	for _, bad := range []RefreshResult{{AccessToken: "access-new", RefreshToken: "refresh-new"}, {AccessToken: "foreign", RefreshToken: "refresh-new", IDToken: "id-new"}, {AccessToken: "access-new", IDToken: "id-new"}} {
		if _, e := store.PrepareRotation(t.Context(), "c1", oldRefresh, bad); e == nil {
			t.Fatal("unverified rotation accepted")
		}
		if e := store.Enroll(t.Context(), "w1", "c1"); e != nil {
			t.Fatal(e)
		}
	}
	proof, e := store.PrepareRotation(t.Context(), "c1", oldRefresh, result)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	rotateProofCiphertexts(t, tx, "access-new", "refresh-new", "id-new")
	if e = store.CommitRotation(t.Context(), tx, "c1", proof); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = store.Current(t.Context(), "w1", "c1"); e != nil {
		t.Fatal(e)
	}
	// An old guard cannot settle a second transaction or reset the generation.
	tx, e = db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.CommitRotation(t.Context(), tx, "c1", proof); e == nil {
		t.Fatal("duplicate rotation accepted")
	}
	tx.Rollback()
	// Any legacy/out-of-band replacement makes the identity proof unusable.
	cipher, e := encryption.Encrypt("foreign")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`UPDATE credentials SET encrypted_value=? WHERE id='c1'`, cipher); e != nil {
		t.Fatal(e)
	}
	if _, e = store.Current(t.Context(), "w1", "c1"); e == nil {
		t.Fatal("stale proof survived replacement")
	}
	// Verify the crypto verifier and durable store together using real synthetic
	// signatures; the fake verifier above only isolates transaction races.
	now := time.Now().UTC()
	key := identityKey(t)
	verifier, calls := identityVerifier(t, key, now)
	store.verifier = verifier
	idToken := signIdentity(t, key, "own-key", identityClaims(now, OpenAIClientID))
	accessToken := signIdentity(t, key, "own-key", identityClaims(now, "https://api.openai.com/v1"))
	tx, e = db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	rotateProofCiphertexts(t, tx, accessToken, "signed-refresh-old", idToken)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = store.Enroll(t.Context(), "w1", "c1"); e != nil {
		t.Fatal(e)
	}
	var expectedCipher string
	if e = db.QueryRow(`SELECT encrypted_value FROM credential_fields WHERE credential_id='c1' AND key='refresh_token'`).Scan(&expectedCipher); e != nil {
		t.Fatal(e)
	}
	result = RefreshResult{AccessToken: accessToken, RefreshToken: "signed-refresh-new", IDToken: idToken}
	proof, e = store.PrepareRotation(t.Context(), "c1", expectedCipher, result)
	if e != nil {
		t.Fatal(e)
	}
	tx, e = db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	rotateProofCiphertexts(t, tx, accessToken, result.RefreshToken, idToken)
	if e = store.CommitRotation(t.Context(), tx, "c1", proof); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = store.Current(t.Context(), "w1", "c1"); e != nil || calls.Load() != 1 {
		t.Fatalf("signed rotation current=%v JWKS=%d", e, calls.Load())
	}

}
func TestCodexProofRotationEnrollmentRaceAndTokenSubstitution(t *testing.T) {
	db, store, oldRefresh := proofFixture(t)
	result := RefreshResult{AccessToken: "access-new", RefreshToken: "refresh-new", IDToken: "id-new"}
	proof, e := store.PrepareRotation(t.Context(), "c1", oldRefresh, result)
	if e != nil || proof != nil {
		t.Fatalf("legacy guard %v %v", proof, e)
	}
	if e = store.Enroll(t.Context(), "w1", "c1"); e != nil {
		t.Fatal(e)
	}
	tx, e := db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	rotateProofCiphertexts(t, tx, "access-new", "refresh-new", "id-new")
	if e = store.CommitRotation(t.Context(), tx, "c1", proof); e == nil {
		t.Fatal("unenrolled remote call overwrote newly enrolled proof")
	}
	tx.Rollback()
	proof, e = store.PrepareRotation(t.Context(), "c1", oldRefresh, result)
	if e != nil {
		t.Fatal(e)
	}
	tx, e = db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	rotateProofCiphertexts(t, tx, "foreign", "refresh-new", "id-new")
	if e = store.CommitRotation(t.Context(), tx, "c1", proof); e == nil {
		t.Fatal("verified result substituted during commit")
	}
	tx.Rollback()
	if _, e = store.Current(t.Context(), "w1", "c1"); e != nil {
		t.Fatal("rollback discarded old proof", e)
	}
	// Explicit re-enrollment changes generation even for the identical identity.
	if e = store.Enroll(t.Context(), "w1", "c1"); e != nil {
		t.Fatal(e)
	}
	tx, e = db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	rotateProofCiphertexts(t, tx, "access-new", "refresh-new", "id-new")
	if e = store.CommitRotation(t.Context(), tx, "c1", proof); e == nil {
		t.Fatal("generation race accepted")
	}
	tx.Rollback()
}

func TestCodexProofMutationRevokesBoundAttemptAndDescendants(t *testing.T) {
	db, store, oldRefresh := proofFixture(t)
	if e := store.Enroll(t.Context(), "w1", "c1"); e != nil {
		t.Fatal(e)
	}
	statements := []string{
		`INSERT INTO workspace_members(id,workspace_id,user_id,role)VALUES('m1','w1','h1','MEMBER'),('m2','w1','h2','MEMBER')`,
		`INSERT INTO crews(id,workspace_id,name,slug)VALUES('crew','w1','Crew','crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role)VALUES('a','w1','crew','A','a','AGENT')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility)VALUES('chat1','w1','a','h1','private'),('chat2','w1','a','h2','private')`,
		`INSERT INTO access_attempts(id,handle_hash,member_id,member_revision,workspace_id,principal_id,agent_id,chat_id,generation,rights,created_at)VALUES('parent','parent-hash','m1',1,'w1','h1','a','chat1',1,'[]','2026-09-30T00:00:00Z'),('other','other-hash','m2',1,'w1','h2','a','chat2',1,'[]','2026-09-30T00:00:00Z')`,
		`INSERT INTO access_attempts(id,handle_hash,member_id,member_revision,workspace_id,principal_id,agent_id,chat_id,generation,rights,created_at,parent_id)VALUES('child','child-hash','m1',1,'w1','h1','a','chat1',2,'[]','2026-09-30T00:00:00Z','parent')`,
		`INSERT INTO restricted_provider_bindings(attempt_id,credential_id,grant_id,credential_revision,model,max_output_tokens)VALUES('parent','c1','synthetic-grant','revision','gpt-5-mini',128)`,
	}
	for _, statement := range statements {
		if _, e := db.Exec(statement); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := store.PrepareRotation(t.Context(), "c1", oldRefresh, RefreshResult{AccessToken: "foreign", RefreshToken: "new", IDToken: "id-new"}); e == nil {
		t.Fatal("foreign identity accepted")
	}
	for _, attempt := range []string{"parent", "child", "other"} {
		var revoked sql.NullString
		if e := db.QueryRow(`SELECT revoked_at FROM access_attempts WHERE id=?`, attempt).Scan(&revoked); e != nil {
			t.Fatal(e)
		}
		if revoked.Valid != (attempt != "other") {
			t.Fatalf("attempt %s revoked=%v", attempt, revoked.Valid)
		}
	}
	if e := store.Enroll(t.Context(), "w1", "c1"); e != nil {
		t.Fatal(e)
	}
	var revoked sql.NullString
	if e := db.QueryRow(`SELECT revoked_at FROM access_attempts WHERE id='parent'`).Scan(&revoked); e != nil || !revoked.Valid {
		t.Fatal("explicit re-enrollment revived old attempt", e)
	}
}
