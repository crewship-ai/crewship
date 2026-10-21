package restricteddispatch

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestPublicationProviderCapturesExactDeclaration(t *testing.T) {
	a := providerFixture(t)
	if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE agents SET restricted_execution_profile='responses_text' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	tx, err := a.Store.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	snapshot, err := a.PublicationProviderIn(t.Context(), tx, "w", "crew", "a")
	if err != nil {
		t.Fatalf("eligible declaration must capture in caller transaction: %v", err)
	}
	if snapshot.WorkspaceID != "w" || snapshot.CrewID != "crew" || snapshot.AgentID != "a" || snapshot.CredentialID != "key" || snapshot.GrantID != "grant" || len(snapshot.CredentialRevision) != 64 || len(snapshot.PolicyHash) != 64 {
		t.Fatalf("wrong exact declaration: %+v", snapshot)
	}
}

func publicationProviderFixture(t *testing.T) Authority {
	t.Helper()
	a := providerFixture(t)
	if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE agents SET restricted_execution_profile='responses_text' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	return a
}
func publicationProviderTx(t *testing.T, a Authority) *sql.Tx {
	t.Helper()
	tx, err := a.Store.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}
func publicationProviderRead(t *testing.T, a Authority) access.PublicationProviderSnapshot {
	t.Helper()
	tx := publicationProviderTx(t, a)
	defer func() { _ = tx.Rollback() }()
	result, err := a.PublicationProviderIn(t.Context(), tx, "w", "crew", "a")
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func publicationProviderExec(t *testing.T, tx *sql.Tx, query string) {
	t.Helper()
	if _, err := tx.ExecContext(t.Context(), query); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationProviderPreservesPolicyHashAndPrivacy(t *testing.T) {
	a := publicationProviderFixture(t)
	oldHash, err := a.ProviderDelegationHash(t.Context(), "h1", "w", "a")
	if err != nil {
		t.Fatal(err)
	}
	// Snapshot hashing does not need decryption, broker/runtime or a valid key.
	t.Setenv("ENCRYPTION_KEY", "not-a-usable-encryption-key")
	snapshot := publicationProviderRead(t, a)
	if snapshot.PolicyHash != oldHash || snapshot.Profile != "responses_text" || snapshot.Model != "fixture-model" {
		t.Fatal("existing planning hash semantics changed")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil || string(raw) != "{}" {
		t.Fatalf("host declaration exposed via JSON: %s %v", raw, err)
	}
	for _, name := range []string{"Ciphertext", "Value", "Secret", "Token"} {
		if _, exists := reflect.TypeOf(snapshot).FieldByName(name); exists {
			t.Fatalf("secret-bearing snapshot field: %s", name)
		}
	}
}

func TestPublicationProviderReadsCallerTransactionWithoutWrites(t *testing.T) {
	a := publicationProviderFixture(t)
	before := publicationProviderRead(t, a)
	tx := publicationProviderTx(t, a)
	publicationProviderExec(t, tx, `UPDATE agents SET llm_model='changed-model' WHERE id='a'`)
	changed, err := a.PublicationProviderIn(t.Context(), tx, "w", "crew", "a")
	if err != nil || changed.Model != "changed-model" || changed.PolicyHash == before.PolicyHash {
		t.Fatalf("transaction-local provider change not captured: %+v %v", changed, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after := publicationProviderRead(t, a); after != before {
		t.Fatal("read helper wrote persistent provider state")
	}
}

func TestPublicationProviderExactCrewCannotBeSubstituted(t *testing.T) {
	a := publicationProviderFixture(t)
	before := publicationProviderRead(t, a)
	tx := publicationProviderTx(t, a)
	publicationProviderExec(t, tx, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('other-crew','w','Other','other-crew')`)
	publicationProviderExec(t, tx, `UPDATE agents SET crew_id='other-crew' WHERE id='a'`)
	empty, err := a.PublicationProviderIn(t.Context(), tx, "w", "crew", "a")
	if !errors.Is(err, access.ErrDenied) || empty != (access.PublicationProviderSnapshot{}) {
		t.Fatal("old crew silently accepted current agent crew")
	}
	moved, err := a.PublicationProviderIn(t.Context(), tx, "w", "other-crew", "a")
	if err != nil || moved.CrewID != "other-crew" || moved.PolicyHash != before.PolicyHash {
		t.Fatal("crew identity lost or legacy provider hash altered", err)
	}
	if moved == before {
		t.Fatal("exact crew change absent from declaration")
	}
}

func TestPublicationProviderRejectsInertAmbiguousOrForeignDeclaration(t *testing.T) {
	cases := map[string][]string{
		"agent deleted":        {`UPDATE agents SET deleted_at='2026-01-01T00:00:00Z' WHERE id='a'`},
		"crew deleted":         {`UPDATE crews SET deleted_at='2026-01-01T00:00:00Z' WHERE id='crew'`},
		"workspace deleted":    {`UPDATE workspaces SET deleted_at='2026-01-01T00:00:00Z' WHERE id='w'`},
		"unsupported profile":  {`UPDATE agents SET restricted_execution_profile='disabled' WHERE id='a'`},
		"wrong provider":       {`UPDATE agents SET llm_provider='ANTHROPIC' WHERE id='a'`},
		"bad model":            {`UPDATE agents SET llm_model='model injected' WHERE id='a'`},
		"credential deleted":   {`UPDATE credentials SET deleted_at='2026-01-01T00:00:00Z' WHERE id='key'`},
		"credential inactive":  {`UPDATE credentials SET status='EXPIRED' WHERE id='key'`},
		"credential level":     {`UPDATE credentials SET security_level=3 WHERE id='key'`},
		"credential expired":   {`UPDATE credentials SET token_expires_at='2000-01-01T00:00:00Z' WHERE id='key'`},
		"grant expired":        {`UPDATE agent_credentials SET expires_at='2000-01-01T00:00:00Z' WHERE id='grant'`},
		"grant missing":        {`DELETE FROM agent_credentials WHERE id='grant'`},
		"wrong env":            {`UPDATE agent_credentials SET env_var_name='OTHER_KEY' WHERE id='grant'`},
		"foreign credential":   {`INSERT INTO workspaces(id,name,slug) VALUES('other-w','Other','other-w')`, `UPDATE credentials SET workspace_id='other-w' WHERE id='key'`},
		"foreign crew":         {`INSERT INTO workspaces(id,name,slug) VALUES('other-w','Other','other-w')`, `UPDATE crews SET workspace_id='other-w' WHERE id='crew'`},
		"ambiguous credential": {`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) SELECT 'key2',workspace_id,'Other',encrypted_value,type,provider,created_by FROM credentials WHERE id='key'`, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('grant2','a','key2','OPENAI_API_KEY')`},
	}
	for name, queries := range cases {
		t.Run(name, func(t *testing.T) {
			a := publicationProviderFixture(t)
			tx := publicationProviderTx(t, a)
			for _, query := range queries {
				publicationProviderExec(t, tx, query)
			}
			snapshot, err := a.PublicationProviderIn(t.Context(), tx, "w", "crew", "a")
			if !errors.Is(err, access.ErrDenied) || snapshot != (access.PublicationProviderSnapshot{}) {
				t.Fatalf("inert/ambiguous/foreign declaration accepted: %+v %v", snapshot, err)
			}
		})
	}
}

func TestPublicationProviderRevisionExpiryAndNativeIdentity(t *testing.T) {
	a := publicationProviderFixture(t)
	before := publicationProviderRead(t, a)
	tx := publicationProviderTx(t, a)
	publicationProviderExec(t, tx, `UPDATE credentials SET encrypted_value='different-synthetic-ciphertext',token_expires_at='2999-01-01T00:00:00Z' WHERE id='key'`)
	publicationProviderExec(t, tx, `UPDATE agent_credentials SET expires_at='2998-01-01T00:00:00Z' WHERE id='grant'`)
	publicationProviderExec(t, tx, `UPDATE agents SET restricted_execution_profile='native_api_key' WHERE id='a'`)
	after, err := a.PublicationProviderIn(t.Context(), tx, "w", "crew", "a")
	if err != nil || after.Profile != "native_api_key" || after.CredentialRevision == before.CredentialRevision || after.PolicyHash == before.PolicyHash || after.GrantExpiry != "2998-01-01T00:00:00Z" || after.KeyExpiry != "2999-01-01T00:00:00Z" {
		t.Fatalf("changed provider ceiling identity omitted: %+v %v", after, err)
	}
}

func TestPublicationProviderEmptyScopeAndClosedTransaction(t *testing.T) {
	a := publicationProviderFixture(t)
	if _, err := a.PublicationProviderIn(t.Context(), nil, "w", "crew", "a"); !errors.Is(err, access.ErrDenied) {
		t.Fatal("nil transaction accepted")
	}
	tx := publicationProviderTx(t, a)
	for _, scope := range [][3]string{{"", "crew", "a"}, {"w", "", "a"}, {"w", "crew", ""}, {"foreign", "crew", "a"}, {"w", "foreign", "a"}, {"w", "crew", "foreign"}} {
		result, err := a.PublicationProviderIn(t.Context(), tx, scope[0], scope[1], scope[2])
		if !errors.Is(err, access.ErrDenied) || result != (access.PublicationProviderSnapshot{}) {
			t.Fatal("empty/foreign scope accepted")
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PublicationProviderIn(t.Context(), tx, "w", "crew", "a"); err == nil {
		t.Fatal("closed transaction silently opened a new read")
	}
}
