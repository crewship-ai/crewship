package restricteddispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
)

func providerFixture(t *testing.T) Authority {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("31", 32))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	a := fixture(t)
	cipher, err := encryption.Encrypt("synthetic-pinned-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE agents SET llm_provider='OPENAI',llm_model='fixture-model' WHERE id='a'`,
		`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('key','w','API key','','API_KEY','OPENAI','owner')`,
		`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('grant','a','key','OPENAI_API_KEY')`,
	} {
		if _, err := a.Store.DB.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE credentials SET encrypted_value=? WHERE id='key'`, cipher); err != nil {
		t.Fatal(err)
	}
	return a
}

func prepareProvider(t *testing.T, a Authority, user, chat, parent string, limit int64) (string, access.Attempt) {
	t.Helper()
	h, attempt, err := a.PrepareResponses(t.Context(), user, "w", "a", chat, parent, []access.Right{{Kind: "agent", ID: "a", Operation: "delegate"}}, limit, command("/bin/true"))
	if err != nil {
		t.Fatal(err)
	}
	return h, attempt
}

func permitProviderDelegation(t *testing.T, a Authority) {
	t.Helper()
	for _, user := range []string{"h1", "h2"} {
		setRights(t, a, user, []access.Right{{Kind: "agent", ID: "a", Operation: "run"}, {Kind: "agent", ID: "a", Operation: "delegate"}})
	}
}

func TestProviderBindingSurvivesReconstructionAndSeparatesHumans(t *testing.T) {
	a := providerFixture(t)
	permitProviderDelegation(t, a)
	h1, first := prepareProvider(t, a, "h1", "c1", "", 128)
	h2, second := prepareProvider(t, a, "h2", "c2", "", 128)
	a = Authority{Store: a.Store}
	p1, err := a.Resolve(t.Context(), h1)
	if err != nil || p1.Network == nil || p1.Network.Grants[0].Responses.Model != "fixture-model" || p1.Network.Grants[0].Responses.MaxOutputTokens != 128 || p1.Network.Credentials[0].ID != "key" || len(p1.Credentials) != 0 || len(p1.Mounts) != 0 {
		t.Fatalf("provider plan not reconstructed: %+v %v", p1, err)
	}
	p2, err := a.Resolve(t.Context(), h2)
	if err != nil || p1.Scope == p2.Scope || p1.Network.Audience == p2.Network.Audience || first.ID == second.ID {
		t.Fatal("same-agent clients share attempt authority", err)
	}
	secret, err := a.BrokerSecret(t.Context(), h1, "key")
	if err != nil || secret.Value != "synthetic-pinned-key" || secret.Revision != p1.Network.Credentials[0].Revision || secret.Account != "key" {
		t.Fatal("host did not receive exact pinned key", err)
	}
	if _, err := a.BrokerSecret(t.Context(), h1, "foreign"); !errors.Is(err, access.ErrDenied) {
		t.Fatal("foreign credential ID accepted", err)
	}
	if delivered, err := a.Secrets(t.Context(), h1); err != nil || len(delivered) != 0 {
		t.Fatal("provider key became agent-visible")
	}
	setRights(t, a, "h1", nil)
	if _, err := a.BrokerSecret(t.Context(), h1, "key"); err == nil {
		t.Fatal("revoked human retained provider key")
	}
	if _, err := a.BrokerSecret(t.Context(), h2, "key"); err != nil {
		t.Fatal("unrelated human lost its grant", err)
	}
}

func TestProviderRevocationCannotBeUndoneByRegrant(t *testing.T) {
	for name, statements := range map[string][]string{
		"credential status":             {`UPDATE credentials SET status='EXPIRED' WHERE id='key'`, `UPDATE credentials SET status='ACTIVE' WHERE id='key'`},
		"credential soft delete":        {`UPDATE credentials SET deleted_at='now' WHERE id='key'`, `UPDATE credentials SET deleted_at=NULL WHERE id='key'`},
		"credential rotate and restore": {`UPDATE credentials SET encrypted_value='different' WHERE id='key'`, `UPDATE credentials SET encrypted_value=(SELECT encrypted_value FROM saved_key) WHERE id='key'`},
		"grant replace same id":         {`INSERT OR REPLACE INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('grant','a','key','OPENAI_API_KEY')`},
		"grant delete reinsert":         {`DELETE FROM agent_credentials WHERE id='grant'`, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('grant','a','key','OPENAI_API_KEY')`},
		"grant expiry restore":          {`UPDATE agent_credentials SET expires_at='2000-01-01T00:00:00Z' WHERE id='grant'`, `UPDATE agent_credentials SET expires_at=NULL WHERE id='grant'`},
		"model change restore":          {`UPDATE agents SET llm_model='other' WHERE id='a'`, `UPDATE agents SET llm_model='fixture-model' WHERE id='a'`},
		"binding deletion":              {`DELETE FROM restricted_provider_bindings`},
	} {
		t.Run(name, func(t *testing.T) {
			a := providerFixture(t)
			permitProviderDelegation(t, a)
			h, _ := prepareProvider(t, a, "h1", "c1", "", 128)
			child, _ := prepareProvider(t, a, "h1", "c1", h, 64)
			if _, err := a.Store.DB.ExecContext(t.Context(), `CREATE TABLE saved_key AS SELECT encrypted_value FROM credentials WHERE id='key'`); err != nil {
				t.Fatal(err)
			}
			for _, query := range statements {
				if _, err := a.Store.DB.ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			for _, handle := range []string{h, child} {
				if _, err := a.Resolve(t.Context(), handle); err == nil {
					t.Fatal("old provider authority revived")
				}
				if _, err := a.BrokerSecret(t.Context(), handle, "key"); err == nil {
					t.Fatal("old provider secret revived")
				}
			}
			fresh, _ := prepareProvider(t, a, "h1", "c1", "", 128)
			if _, err := a.BrokerSecret(t.Context(), fresh, "key"); err != nil {
				t.Fatal("fresh authorized attempt denied", err)
			}
		})
	}
}

func TestProviderAdmissionPrecedesPromptAndCannotWidenParent(t *testing.T) {
	a := providerFixture(t)
	permitProviderDelegation(t, a)
	parent, _ := prepareProvider(t, a, "h1", "c1", "", 64)
	for _, limit := range []int64{0, 65, 32769} {
		called := false
		if _, _, err := a.PrepareResponses(t.Context(), "h1", "w", "a", "c1", parent, nil, limit, func(context.Context, access.Attempt) ([]string, error) { called = true; return []string{"true"}, nil }); err == nil || called {
			t.Fatal("broader provider child reached prompt construction")
		}
	}
	if _, _, err := a.PrepareResponses(t.Context(), "h1", "w", "a", "c2", "", nil, 64, func(context.Context, access.Attempt) ([]string, error) {
		t.Error("foreign chat reached prompt construction")
		return []string{"true"}, nil
	}); err == nil {
		t.Fatal("foreign chat admitted")
	}
	var id string
	_, _, err := a.PrepareResponses(t.Context(), "h1", "w", "a", "c1", "", nil, 64, func(ctx context.Context, attempt access.Attempt) ([]string, error) {
		id = attempt.ID
		_, err := a.Store.DB.ExecContext(ctx, `UPDATE credentials SET status='EXPIRED' WHERE id='key'`)
		return []string{"true"}, err
	})
	if err == nil {
		t.Fatal("credential revoked during prompt construction was accepted")
	}
	var count int
	if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM restricted_launches WHERE attempt_id=?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed preparation retained executable launch", err)
	}
}

func TestProviderExpiryAndUnsupportedCredentialModes(t *testing.T) {
	for name, query := range map[string]string{
		"expired grant":     `UPDATE agent_credentials SET expires_at='2000-01-01T00:00:00Z'`,
		"malformed expiry":  `UPDATE agent_credentials SET expires_at=''`,
		"expired key":       `UPDATE credentials SET token_expires_at='2000-01-01T00:00:00Z'`,
		"login credential":  `UPDATE credentials SET type='PROVIDER_LOGIN'`,
		"wrong provider":    `UPDATE credentials SET provider='ANTHROPIC'`,
		"endpoint provider": `UPDATE credentials SET provider='OPENAI_COMPAT'`,
		"keeper mediated":   `UPDATE credentials SET security_level=3`,
		"crew only":         `DELETE FROM agent_credentials`,
		"wrong slot":        `UPDATE agent_credentials SET env_var_name='OTHER_KEY'`,
		"ambiguous keys":    `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) SELECT 'key2',workspace_id,'Second',encrypted_value,type,provider,created_by FROM credentials; INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('grant2','a','key2','OPENAI_API_KEY')`,
	} {
		t.Run(name, func(t *testing.T) {
			a := providerFixture(t)
			if _, err := a.Store.DB.ExecContext(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			if _, _, err := a.PrepareResponses(t.Context(), "h1", "w", "a", "c1", "", nil, 64, func(context.Context, access.Attempt) ([]string, error) {
				t.Error("unsupported key reached prompt construction")
				return []string{"true"}, nil
			}); err == nil {
				t.Fatal("unsupported provider grant admitted")
			}
		})
	}
	a := providerFixture(t)
	permitProviderDelegation(t, a)
	expires := time.Now().Add(time.Minute).UTC()
	if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE agent_credentials SET expires_at=?`, expires.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	h, _ := prepareProvider(t, a, "h1", "c1", "", 64)
	secret, err := a.BrokerSecret(t.Context(), h, "key")
	if err != nil || !secret.Expires.Equal(expires) {
		t.Fatal("provider secret exceeded grant deadline", err)
	}
}

func TestDelegatedProviderCannotAcquireTargetAgentsDifferentKey(t *testing.T) {
	a := providerFixture(t)
	for _, query := range []string{
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,llm_provider,llm_model) VALUES('b','w','crew','B','dispatch-b','AGENT','OPENAI','fixture-model')`,
		`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) SELECT 'key2',workspace_id,'Other key',encrypted_value,type,provider,created_by FROM credentials WHERE id='key'`,
		`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('grant2','b','key2','OPENAI_API_KEY')`,
	} {
		if _, err := a.Store.DB.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	rights := []access.Right{{Kind: "agent", ID: "a", Operation: "run"}, {Kind: "agent", ID: "b", Operation: "run"}, {Kind: "agent", ID: "b", Operation: "delegate"}}
	setRights(t, a, "h1", rights)
	parent, _, err := a.PrepareResponses(t.Context(), "h1", "w", "a", "c1", "", rights, 128, command("true"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = a.PrepareResponses(t.Context(), "h1", "w", "b", "c1", parent, nil, 64, func(context.Context, access.Attempt) ([]string, error) {
		t.Error("different provider key reached delegated prompt")
		return []string{"true"}, nil
	})
	if !errors.Is(err, access.ErrDenied) {
		t.Fatal("delegation acquired target agent's broader credentials", err)
	}
	if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE agent_credentials SET credential_id='key' WHERE id='grant2'`); err != nil {
		t.Fatal(err)
	}
	child, _, err := a.PrepareResponses(t.Context(), "h1", "w", "b", "c1", parent, nil, 64, command("true"))
	if err != nil {
		t.Fatal("same-key narrowed delegation rejected", err)
	}
	// Revoke only the parent's grant. The child's own explicit grant stays valid,
	// but its inherited provider authority must still die with the parent.
	if _, err := a.Store.DB.ExecContext(t.Context(), `DELETE FROM agent_credentials WHERE id='grant'`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BrokerSecret(t.Context(), child, "key"); err == nil {
		t.Fatal("child survived parent provider revocation")
	}
}

func TestProviderBindingCannotUpgradePreparedOfflineLaunch(t *testing.T) {
	a := providerFixture(t)
	h, attempt, err := a.Prepare(t.Context(), "h1", "w", "a", "c1", "", nil, command("true"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.pinProvider(t.Context(), attempt, 64); err == nil {
		t.Fatal("prepared offline attempt acquired provider authority")
	}
	p, err := a.Resolve(t.Context(), h)
	if err != nil || p.Network != nil {
		t.Fatal("rejected upgrade changed offline launch", err)
	}
}

func TestProviderRejectsStructuredSecretInsteadOfForwardingIt(t *testing.T) {
	a := providerFixture(t)
	for _, value := range []string{`{"baseURL":"https://foreign.example","apiKey":"private","headers":{"X-Secret":"private"}}`, `["private"]`, `"private"`, " key", "key\n", "\ufeff{}"} {
		cipher, err := encryption.Encrypt(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE credentials SET encrypted_value=? WHERE id='key'`, cipher); err != nil {
			t.Fatal(err)
		}
		h, _, err := a.PrepareResponses(t.Context(), "h1", "w", "a", "c1", "", nil, 64, command("true"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.BrokerSecret(t.Context(), h, "key"); !errors.Is(err, access.ErrDenied) {
			t.Fatal("structured or malformed credential released", err)
		}
	}
}
