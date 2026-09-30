package restricteddispatch

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
)

func TestDelegationUsesFrozenIndependentTargetKey(t *testing.T) {
	for _, key := range []string{"key", "key-b"} {
		t.Run(key, func(t *testing.T) { testFrozenDelegation(t, key) })
	}
}

func testFrozenDelegation(t *testing.T, mutationKey string) {
	a := providerFixture(t)
	ctx := t.Context()
	for _, query := range []string{
		`UPDATE agents SET restricted_execution_profile='responses_text' WHERE id='a'`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,llm_provider,llm_model,restricted_execution_profile) VALUES('b','w','crew','B','dispatch-b','AGENT','OPENAI','fixture-model','responses_text')`,
		`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('key-b','w','B key','','API_KEY','OPENAI','owner')`,
		`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('grant-b','b','key-b','OPENAI_API_KEY')`,
		`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('recipe','w','Recipe','delegated','{}','fixture','active')`,
	} {
		if _, err := a.Store.DB.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := encryption.Encrypt("synthetic-independent-b-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.DB.ExecContext(ctx, `UPDATE credentials SET encrypted_value=? WHERE id='key-b'`, cipher); err != nil {
		t.Fatal(err)
	}
	rights := []access.Right{{Kind: "agent", ID: "a", Operation: "run"}, {Kind: "agent", ID: "b", Operation: "run"}, {Kind: "agent", ID: "b", Operation: "delegate"}}
	setRights(t, a, "h1", rights)
	originHandle, origin, err := a.Store.Admit(ctx, "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := a.Store.AppendContext(ctx, originHandle, access.ContextUser, "classified workflow input")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Store.CompleteAttempt(ctx, originHandle); err != nil {
		t.Fatal(err)
	}
	originCipher, err := encryption.Encrypt(originHandle)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err = a.Store.DB.ExecContext(ctx, `INSERT INTO restricted_workflow_jobs(id,workspace_id,principal_id,member_id,member_revision,pipeline_id,recipe_hash,recipe_json,agent_id,execution_profile,chat_id,origin_attempt_id,origin_handle_ciphertext,source_kind,inputs_json,created_at,fire_at,expires_at) VALUES('job','w','h1',?,?,'recipe','fixture','{}','a','responses_text','c1',?,?,'manual','{}',?,?,?)`, origin.Member, origin.Revision, origin.ID, originCipher, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := a.ProviderDelegationHash(ctx, "h1", "w", "b")
	if err != nil {
		t.Fatal(err)
	}
	hashA, err := a.ProviderDelegationHash(ctx, "h1", "w", "a")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.FreezeWorkflowProvider(ctx, tx, "job", "b", hash, 128); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = a.FreezeWorkflowProvider(ctx, tx, "job", "a", hashA, 128); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.DB.ExecContext(ctx, `UPDATE restricted_workflow_jobs SET state='running' WHERE id='job'`); err != nil {
		t.Fatal(err)
	}
	parentHandle, parent, err := a.Store.Admit(ctx, "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.BindProviderDelegation(ctx, parentHandle, "job", "b", hash, 128); err == nil {
		t.Fatal("unbound orchestration parent consumed workflow provider slot")
	}
	if _, err = a.Store.BuildContext(ctx, parent, "orchestrate only"); err != nil {
		t.Fatal(err)
	}
	if err = a.BindProviderDelegation(ctx, parentHandle, "job", "b", "wrong-policy", 128); err == nil {
		t.Fatal("different provider policy accepted")
	}
	if _, _, err = a.PrepareDelegatedResponses(ctx, "h1", "w", "b", "c1", parentHandle, rights, 128, func(context.Context, string, access.Attempt) ([]string, error) {
		t.Fatal("builder reached before explicit slot binding")
		return nil, nil
	}); err == nil {
		t.Fatal("missing provider slot accepted")
	}
	if err = a.BindProviderDelegation(ctx, parentHandle, "job", "b", hash, 128); err != nil {
		t.Fatal(err)
	}
	if err = a.BindProviderDelegation(ctx, parentHandle, "job", "a", hashA, 128); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.Store.AdmitWorkflowContinuation(ctx, "h1", "w", "b", "c1", parentHandle, rights); err == nil {
		t.Fatal("continuation selected a foreign target")
	}
	childHandle, childAttempt, err := a.PrepareDelegatedResponses(ctx, "h1", "w", "b", "c1", parentHandle, rights, 128, func(ctx context.Context, handle string, child access.Attempt) ([]string, error) {
		if _, err := a.Store.ImportDelegatedContext(ctx, parentHandle, handle, []string{seed.ID}); err != nil {
			return nil, err
		}
		if _, err := a.Store.BuildContext(ctx, child, "delegated task"); err != nil {
			return nil, err
		}
		return []string{"/bin/true"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.DB.ExecContext(ctx, `INSERT INTO access_attempts(id,handle_hash,member_id,member_revision,workspace_id,principal_id,agent_id,chat_id,parent_id,generation,rights,created_at,chat_generation,chat_revision,admission_operation,context_audience,workflow_continuation)
 SELECT 'forged-return','forged-return-hash',member_id,member_revision,workspace_id,principal_id,agent_id,chat_id,parent_id,generation+100,rights,created_at,chat_generation,chat_revision,admission_operation,context_audience,1 FROM access_attempts WHERE id=?`, childAttempt.ID); err == nil {
		t.Fatal("forged foreign-agent continuation inserted")
	}
	secret, err := a.BrokerSecret(ctx, childHandle, "key-b")
	if err != nil || secret.Value != "synthetic-independent-b-key" {
		t.Fatalf("B own key not selected: %v", err)
	}
	if _, err = a.BrokerSecret(ctx, childHandle, "key"); err == nil {
		t.Fatal("A credential inherited into B")
	}
	if _, _, err = a.PrepareResponses(ctx, "h1", "w", "b", "c1", parentHandle, rights, 128, command("/bin/true")); err == nil {
		t.Fatal("ordinary inherited provider path relaxed")
	}
	result, err := a.Store.AppendContext(ctx, childHandle, access.ContextAssistant, "classified B output")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Store.CompleteAttempt(ctx, childHandle); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.Store.Admit(ctx, "h1", "w", "a", "c1", parentHandle, rights); err == nil {
		t.Fatal("generic admission bypassed delegate(A)")
	}
	returnHandle, returned, err := a.PrepareDelegatedResponses(ctx, "h1", "w", "a", "c1", parentHandle, rights, 128, func(ctx context.Context, handle string, child access.Attempt) ([]string, error) {
		if _, err := a.Store.ImportDelegatedContext(ctx, parentHandle, handle, []string{result.ID}); err == nil {
			t.Fatal("generic import gained continuation exception")
		}
		if _, err := a.Store.ImportWorkflowContinuation(ctx, parentHandle, handle, []string{result.ID}); err != nil {
			return nil, err
		}
		if _, err := a.Store.BuildContext(ctx, child, "return to A"); err != nil {
			return nil, err
		}
		return []string{"/bin/true"}, nil
	})
	if err != nil || !returned.WorkflowContinuation {
		t.Fatalf("A continuation denied without delegate(A): %v", err)
	}
	if _, err = a.Store.DB.ExecContext(ctx, `UPDATE access_attempts SET workflow_continuation=0 WHERE id=?`, returned.ID); err == nil {
		t.Fatal("continuation flag mutation accepted")
	}
	runner := &TextRunner{Authority: a, MaxOutputTokens: 128}
	runner.StartSession = func(ctx context.Context, handle string) (TextSession, error) {
		attempt, err := a.Store.Resolve(ctx, handle)
		if err != nil {
			return nil, err
		}
		plan, err := a.Resolve(ctx, handle)
		if err != nil {
			return nil, err
		}
		expected := "key"
		if attempt.Agent == "b" {
			expected = "key-b"
		}
		if plan.Network == nil || plan.Network.Credentials[0].ID != expected {
			t.Fatal("typed runner selected another target's provider")
		}
		done := make(chan struct{})
		close(done)
		return &textFixtureSession{output: "{\"type\":\"text\",\"text\":\"typed classified answer\"}\n{\"type\":\"done\"}\n", done: done}, nil
	}
	emit := func(string, string) error { return nil }
	bProof, err := runner.ExecuteWorkflowRun(ctx, DelegatedRunRequest{User: "h1", Workspace: "w", Agent: "b", Chat: "c1", ParentHandle: parentHandle, Input: "B task", Rights: rights, SourceEntryIDs: []string{seed.ID}}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bProof.Read(ctx, a.Store, "h2", "w", "b", "c1"); err == nil {
		t.Fatal("foreign actor read delegated proof")
	}
	aProof, err := runner.ExecuteWorkflowRun(ctx, DelegatedRunRequest{User: "h1", Workspace: "w", Agent: "a", Chat: "c1", ParentHandle: parentHandle, Input: "A returns from B", Rights: rights, SourceEntryIDs: bProof.ContextIDs()}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if err = aProof.Check(ctx, a.Store, "h1", "w", "a", "c1"); err != nil {
		t.Fatal(err)
	}
	if err = a.Store.RevokeAttempt(ctx, bProof.handle); err != nil {
		t.Fatal(err)
	}
	if err = aProof.Check(ctx, a.Store, "h1", "w", "a", "c1"); err == nil {
		t.Fatal("completed B revocation did not revoke completed typed A return")
	}
	if err = a.Store.RevokeAttempt(ctx, childHandle); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.Resolve(ctx, returnHandle); err == nil {
		t.Fatal("B source revocation did not fence A return")
	}
	var originalCipher string
	if err = a.Store.DB.QueryRowContext(ctx, `SELECT encrypted_value FROM credentials WHERE id=?`, mutationKey).Scan(&originalCipher); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.DB.ExecContext(ctx, `UPDATE credentials SET encrypted_value='rotated' WHERE id=?`, mutationKey); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.DB.ExecContext(ctx, `UPDATE credentials SET encrypted_value=? WHERE id=?`, originalCipher, mutationKey); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.Resolve(ctx, parentHandle); err == nil {
		t.Fatal("target key regrant revived orchestration parent")
	}
	if _, err = a.Store.Resolve(ctx, childHandle); err == nil {
		t.Fatal("target key regrant revived child")
	}
}
