package access

import (
	"fmt"
	"strings"
	"testing"
)

func failAccessWrite(t *testing.T, s Store, operation, table string) {
	t.Helper()
	if _, err := s.DB.Exec(fmt.Sprintf(`CREATE TRIGGER fail_access_write BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'injected storage failure'); END`, operation, table)); err != nil {
		t.Fatal(err)
	}
}
func clearAccessFailure(t *testing.T, s Store) {
	t.Helper()
	if _, err := s.DB.Exec(`DROP TRIGGER fail_access_write`); err != nil {
		t.Fatal(err)
	}
}

func TestProjectReplacementStorageRollback(t *testing.T) {
	for _, stage := range []struct{ op, table string }{{"UPDATE", "projects"}, {"UPDATE", "project_file_versions"}, {"DELETE", "project_file_blobs"}, {"UPDATE", "project_files"}, {"INSERT", "project_file_versions"}, {"INSERT", "project_file_blobs"}} {
		t.Run(stage.op+stage.table, func(t *testing.T) {
			s, one, _ := projectFileFixture(t)
			failAccessWrite(t, s, stage.op, stage.table)
			in := ProjectFileWrite{FileID: one.FileID, Name: one.Name, ExpectedRevision: one.Revision}
			got, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", in, []byte("replacement"))
			if err == nil || got.ID != "" {
				t.Fatalf("failed replacement returned a version: %#v %v", got, err)
			}
			clearAccessFailure(t, s)
			current, data, err := s.ReadProjectFile(t.Context(), "h1", "w", "p1", one.ID)
			if err != nil || current != one || string(data) != "P1_PRIVATE_CANARY" {
				t.Fatalf("original not preserved: %#v %q %v", current, data, err)
			}
			var count int
			if err := s.DB.QueryRow(`SELECT count(*) FROM project_file_versions WHERE file_id=?`, one.FileID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("partial version persisted: %d %v", count, err)
			}
			if _, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", in, []byte("replacement")); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
		})
	}
}
func TestProjectRetirementStorageRollback(t *testing.T) {
	for _, stage := range []struct{ op, table string }{{"UPDATE", "projects"}, {"UPDATE", "project_files"}, {"UPDATE", "project_file_versions"}, {"DELETE", "project_file_blobs"}} {
		t.Run(stage.table, func(t *testing.T) {
			s, one, _ := projectFileFixture(t)
			failAccessWrite(t, s, stage.op, stage.table)
			if err := s.RetireProjectFile(t.Context(), "h1", "w", "p1", one.FileID, one.Revision); err == nil {
				t.Fatal("failed retirement succeeded")
			}
			clearAccessFailure(t, s)
			_, data, err := s.ReadProjectFile(t.Context(), "h1", "w", "p1", one.ID)
			if err != nil || string(data) != "P1_PRIVATE_CANARY" {
				t.Fatalf("retirement lost live data: %q %v", data, err)
			}
			if err := s.RetireProjectFile(t.Context(), "h1", "w", "p1", one.FileID, one.Revision); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestDelegatedContextStorageRollback(t *testing.T) {
	for _, table := range []string{"access_context", "access_context_delegations", "access_context_dependencies"} {
		t.Run(table, func(t *testing.T) {
			s := fixture(t)
			rights := []Right{{"agent", "a", "run"}, {"agent", "b", "run"}, {"agent", "b", "delegate"}}
			policy(t, s, "h1", rights...)
			parent, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
			if err != nil {
				t.Fatal(err)
			}
			source, err := s.AppendContext(t.Context(), parent, ContextUser, "CLASSIFIED_INPUT")
			if err != nil {
				t.Fatal(err)
			}
			child, attempt, err := s.Admit(t.Context(), "h1", "w", "b", "c1", parent, rights)
			if err != nil {
				t.Fatal(err)
			}
			failAccessWrite(t, s, "INSERT", table)
			entry, err := s.ImportDelegatedContext(t.Context(), parent, child, []string{source.ID})
			if err == nil || entry.ID != "" {
				t.Fatalf("failed transfer returned data: %#v %v", entry, err)
			}
			clearAccessFailure(t, s)
			for _, query := range []string{`SELECT count(*) FROM access_context WHERE attempt_id=?`, `SELECT count(*) FROM access_context_delegations WHERE child_attempt_id=?`, `SELECT count(*) FROM access_context_dependencies WHERE attempt_id=?`} {
				var count int
				if err := s.DB.QueryRow(query, attempt.ID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial context transfer: %d %v", count, err)
				}
			}
			if _, err := s.ImportDelegatedContext(t.Context(), parent, child, []string{source.ID}); err != nil {
				t.Fatal(err)
			}
			prompt, err := s.BuildContext(t.Context(), attempt, "question")
			if err != nil || !strings.Contains(prompt.Input, "CLASSIFIED_INPUT") {
				t.Fatalf("retry lost context: %#v %v", prompt, err)
			}
		})
	}
}
func TestPromptRefusesUnboundContextAfterDependencyFailure(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "run"})
	parent, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.AppendContext(t.Context(), parent, ContextUser, "PRIVATE_INPUT")
	if err != nil {
		t.Fatal(err)
	}
	_, child, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	failAccessWrite(t, s, "INSERT", "access_context_dependencies")
	prompt, err := s.BuildContext(t.Context(), child, "question")
	if err == nil || prompt.Input != "" || prompt.System != "" {
		t.Fatalf("unbound prompt escaped: %#v %v", prompt, err)
	}
	clearAccessFailure(t, s)
	prompt, err = s.BuildContext(t.Context(), child, "question")
	if err != nil || !strings.Contains(prompt.Input, source.Content) {
		t.Fatalf("retry failed: %#v %v", prompt, err)
	}
	if err := s.RevokeAttempt(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BuildContext(t.Context(), child, "question"); err == nil {
		t.Fatal("retry did not preserve provenance revocation")
	}
}

func TestPromptAndDelegationByteBudgets(t *testing.T) {
	s := fixture(t)
	rights := []Right{{"agent", "a", "run"}, {"agent", "b", "run"}, {"agent", "b", "delegate"}}
	policy(t, s, "h1", rights...)
	parent, attempt, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BuildContext(t.Context(), attempt, strings.Repeat("x", 32769)); err == nil {
		t.Fatal("oversized current message accepted")
	}
	if _, err := s.AppendContext(t.Context(), parent, ContextUser, strings.Repeat("x", 32769)); err == nil {
		t.Fatal("oversized source accepted")
	}
	source, err := s.AppendContext(t.Context(), parent, ContextUser, strings.Repeat("\x00", 20000))
	if err != nil {
		t.Fatal(err)
	}
	if prompt, err := s.BuildContext(t.Context(), attempt, "question"); err == nil || prompt.Input != "" {
		t.Fatalf("JSON expansion bypassed prompt limit: %d %v", len(prompt.Input), err)
	}
	child, childAttempt, err := s.Admit(t.Context(), "h1", "w", "b", "c1", parent, rights)
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := s.ImportDelegatedContext(t.Context(), parent, child, []string{source.ID}); err == nil || entry.ID != "" {
		t.Fatalf("oversized delegated envelope accepted: %#v %v", entry, err)
	}
	entries, err := s.ContextEntries(t.Context(), childAttempt)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected delegation retained data: %#v %v", entries, err)
	}
}

func TestContextStoreLossNeverLooksLikeEmptyHistory(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "run"}, Right{"agent", "a", "chat"})
	handle, attempt, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendContext(t.Context(), handle, ContextUser, "PRIVATE_INPUT"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`ALTER TABLE access_context RENAME TO inaccessible_context`); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ContextEntries(t.Context(), attempt); err == nil || rows != nil {
		t.Fatalf("authority read degraded to success: %#v %v", rows, err)
	}
	if rows, err := s.ContextEntriesForChat(t.Context(), "h1", "w", "a", "c1"); err == nil || rows != nil {
		t.Fatalf("chat projection degraded to success: %#v %v", rows, err)
	}
	if _, err := s.AppendContext(t.Context(), handle, ContextUser, "new text"); err == nil {
		t.Fatal("missing context store accepted write")
	}
}

func TestFileAndOutcomeStoreLossNeverReturnsSuccess(t *testing.T) {
	for _, table := range []string{"access_files", "access_attempt_outcomes"} {
		t.Run(table, func(t *testing.T) {
			s := fixture(t)
			policy(t, s, "h1", Right{"agent", "a", "run"}, Right{"agent", "a", "chat"})
			handle, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			file, err := s.SaveFile(t.Context(), handle, "result.txt", []byte("PRIVATE_FILE"))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordOutcome(t.Context(), handle, "completed"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(`ALTER TABLE ` + table + ` RENAME TO inaccessible_store`); err != nil {
				t.Fatal(err)
			}
			if table == "access_files" {
				if _, err := s.SaveFile(t.Context(), handle, "other.txt", []byte("new")); err == nil {
					t.Fatal("file write succeeded without storage")
				}
				if rows, err := s.FilesForChat(t.Context(), "h1", "w", "a", "c1"); err == nil || rows != nil {
					t.Fatalf("file list reported success: %#v %v", rows, err)
				}
				if _, data, err := s.ReadFileForChat(t.Context(), "h1", "w", "a", "c1", file.ID); err == nil || data != nil {
					t.Fatalf("file read reported success: %q %v", data, err)
				}
			} else {
				if err := s.RecordOutcome(t.Context(), handle, "failed"); err == nil {
					t.Fatal("outcome write succeeded without storage")
				}
				if rows, err := s.OutcomesForChat(t.Context(), "h1", "w", "c1"); err == nil || rows != nil {
					t.Fatalf("audit list reported success: %#v %v", rows, err)
				}
			}
		})
	}
}

func TestMalformedStoredContextCannotBecomePromptAuthority(t *testing.T) {
	for _, raw := range []string{"not-json", `["cycle"]`, `["missing"]`, `{"source":"foreign"}`} {
		t.Run(raw, func(t *testing.T) {
			s := fixture(t)
			policy(t, s, "h1", Right{"agent", "a", "run"})
			handle, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(`INSERT INTO access_context(id,attempt_id,scope,agent_id,kind,role,content,sources,created_at) VALUES('cycle',?,?,?,'summary','derived','UNTRUSTED_CORRUPT_CONTEXT',?,'2026-10-03T00:00:00Z')`, a.ID, a.Scope, a.Agent, raw); err != nil {
				t.Fatal(err)
			}
			entries, err := s.ContextEntries(t.Context(), a)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid provenance exposed: %#v %v", entries, err)
			}
			if _, err := s.DeriveContext(t.Context(), handle, ContextMemory, []string{"cycle"}, "laundered"); err == nil {
				t.Fatal("corrupt provenance accepted as a source")
			}
			prompt, err := s.BuildContext(t.Context(), a, "hello")
			if err != nil || strings.Contains(prompt.Input, "UNTRUSTED_CORRUPT_CONTEXT") {
				t.Fatalf("invalid context reached model: %#v %v", prompt, err)
			}
		})
	}
}

func TestNullContextIdentityFailsClosedWithoutPartialHistory(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "run"}, Right{"agent", "a", "chat"})
	h, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendContext(t.Context(), h, ContextUser, "valid history"); err != nil {
		t.Fatal(err)
	}
	// SQLite permits NULL in a non-INTEGER PRIMARY KEY unless explicitly NOT NULL.
	// A damaged restored row must fail the complete read, not return a partial slice.
	if _, err := s.DB.Exec(`INSERT INTO access_context(id,attempt_id,scope,agent_id,kind,role,content,sources,created_at) VALUES(NULL,?,?,?,'history','user','broken','[]','2026-10-03T00:00:00Z')`, a.ID, a.Scope, a.Agent); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ContextEntries(t.Context(), a); err == nil || rows != nil {
		t.Fatalf("partial history returned: %#v %v", rows, err)
	}
	if rows, err := s.ContextEntriesForChat(t.Context(), "h1", "w", "a", "c1"); err == nil || rows != nil {
		t.Fatalf("partial chat returned: %#v %v", rows, err)
	}
}

func TestProjectInputBindingStorageRollback(t *testing.T) {
	for _, stage := range []struct{ op, table string }{{"UPDATE", "access_attempts"}, {"INSERT", "attempt_project_inputs"}} {
		t.Run(stage.table, func(t *testing.T) {
			s, one, _ := projectFileFixture(t)
			h, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", []Right{{"project", "p1", "read"}})
			if err != nil {
				t.Fatal(err)
			}
			failAccessWrite(t, s, stage.op, stage.table)
			if err := s.BindProjectFileInputs(t.Context(), h, []string{one.ID}); err == nil {
				t.Fatal("failed selection accepted")
			}
			clearAccessFailure(t, s)
			inputs, err := s.FrozenProjectInputsForAttempt(t.Context(), a)
			if err != nil || len(inputs) != 0 {
				t.Fatalf("partial selection survived: %#v %v", inputs, err)
			}
			if err := s.BindProjectFileInputs(t.Context(), h, []string{one.ID}); err != nil {
				t.Fatal(err)
			}
			inputs, err = s.FrozenProjectInputs(t.Context(), h)
			if err != nil || len(inputs) != 1 || inputs[0].Version.ID != one.ID {
				t.Fatalf("retry lost selected version: %#v %v", inputs, err)
			}
		})
	}
}

func TestAuthorityRejectsInvalidPolicyAndSelectionWithoutMutation(t *testing.T) {
	s, one, _ := projectFileFixture(t)
	m, err := s.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode, user string
		expected   Membership
		rights     []Right
	}{
		{"invalid", "h1", m, nil}, {"trusted", "h1", m, []Right{{"agent", "a", "run"}}}, {"restricted", "h1", m, make([]Right, 257)},
		{"restricted", "missing", Membership{}, nil}, {"restricted", "h1", m, []Right{{"agent", "a", "run"}, {"agent", "a", "run"}}},
		{"restricted", "h1", m, []Right{{"agent", "", "run"}}},
	} {
		if _, err := s.Replace(t.Context(), "owner", tc.user, "w", tc.mode, tc.expected, tc.rights); err == nil {
			t.Fatalf("invalid policy accepted: %#v", tc)
		}
	}
	owner, err := s.Membership(t.Context(), "owner", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(t.Context(), "owner", "owner", "w", "restricted", owner, nil); err == nil {
		t.Fatal("owner restricted through member policy")
	}
	if _, err := s.Policy(t.Context(), "owner", "missing", "w"); err == nil {
		t.Fatal("missing member policy exposed")
	}
	if _, _, err := s.Admit(t.Context(), "owner", "w", "a", "c1", "", nil); err == nil {
		t.Fatal("trusted principal acquired restricted authority")
	}
	if _, err := s.ProjectFileRights(t.Context(), "h1", "w", []string{"bad/id"}); err == nil {
		t.Fatal("invalid version ID accepted")
	}
	h, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{"missing"}, {one.ID}} {
		if err := s.BindProjectFileInputs(t.Context(), h, ids); err == nil {
			t.Fatalf("ungranted selection accepted: %#v", ids)
		}
	}
	if _, err := s.FrozenProjectInputs(t.Context(), "invalid"); err == nil {
		t.Fatal("invalid handle materialized inputs")
	}
	if err := s.BindProjectFileInputs(t.Context(), "invalid", []string{one.ID}); err == nil {
		t.Fatal("invalid handle bound inputs")
	}
	if _, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{FileID: "missing", Name: "missing.txt", ExpectedRevision: 1}, []byte("x")); err == nil {
		t.Fatal("missing file replaced")
	}
	if err := s.RetireProjectFile(t.Context(), "h2", "w", "p1", one.FileID, one.Revision); err == nil {
		t.Fatal("foreign project retired")
	}
	if err := s.RetireProjectFile(t.Context(), "h1", "w", "p1", one.FileID, one.Revision+1); err == nil {
		t.Fatal("stale retirement accepted")
	}
	current, err := s.Membership(t.Context(), "h1", "w")
	if err != nil || current != m {
		t.Fatalf("invalid operations changed policy: %#v %v", current, err)
	}
}

func TestDelegatedImportRefusesInvalidHandlesAndDuplicateSources(t *testing.T) {
	s := fixture(t)
	rights := []Right{{"agent", "a", "run"}, {"agent", "b", "run"}, {"agent", "b", "delegate"}}
	policy(t, s, "h1", rights...)
	parent, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.AppendContext(t.Context(), parent, ContextUser, "PRIVATE_SOURCE")
	if err != nil {
		t.Fatal(err)
	}
	child, a, err := s.Admit(t.Context(), "h1", "w", "b", "c1", parent, rights)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		parent, child string
		sources       []string
	}{{"invalid", child, []string{source.ID}}, {parent, "invalid", []string{source.ID}}, {parent, child, []string{source.ID, source.ID}}} {
		if e, err := s.ImportDelegatedContext(t.Context(), tc.parent, tc.child, tc.sources); err == nil || e.ID != "" {
			t.Fatalf("invalid transfer accepted: %#v %v", e, err)
		}
	}
	entries, err := s.ContextEntries(t.Context(), a)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid transfer leaked data: %#v %v", entries, err)
	}
	if err := s.CheckWorkflowContextAttempt(t.Context(), parent, "unknown-job"); err == nil {
		t.Fatal("root accepted as completed workflow leaf")
	}
	if _, err := readDelegatedContextSource(t.Context(), s.DB, a, "missing", source.ID, map[string]bool{}); err == nil {
		t.Fatal("unrecorded delegation exposed source")
	}
	entry, err := s.ImportDelegatedContext(t.Context(), parent, child, []string{source.ID})
	if err != nil {
		t.Fatal(err)
	}
	forged := a
	forged.Scope = "foreign"
	if _, err := readDelegatedContextSource(t.Context(), s.DB, forged, entry.ID, source.ID, map[string]bool{}); err == nil {
		t.Fatal("foreign scope read delegation")
	}
	if err := s.RevokeAttempt(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := readDelegatedContextSource(t.Context(), s.DB, a, entry.ID, source.ID, map[string]bool{}); err == nil {
		t.Fatal("revoked parent released delegated context")
	}
}

func TestContextSnapshotRejectsStaleHostIdentityAndUnavailableStore(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "run"})
	handle, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.AppendContext(t.Context(), handle, ContextUser, "PRIVATE_INPUT")
	if err != nil {
		t.Fatal(err)
	}
	forged := a
	forged.Scope = "foreign"
	if err := s.bindContextSnapshot(t.Context(), forged, []ContextEntry{source}); err == nil {
		t.Fatal("mismatched host identity bound a snapshot")
	}
	if _, err := s.resolveContextAttempt(t.Context(), a, ContextEntry{ID: "missing"}); err == nil {
		t.Fatal("missing snapshot source validated")
	}
	if err := s.RevokeAttempt(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolveContextAttempt(t.Context(), a, source); err == nil {
		t.Fatal("revoked host identity validated")
	}
	if err := s.bindContextSnapshot(t.Context(), a, []ContextEntry{source}); err == nil {
		t.Fatal("revoked host identity bound sources")
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ContextEntries(t.Context(), a); err == nil || rows != nil {
		t.Fatalf("closed store returned context: %#v %v", rows, err)
	}
	if _, err := s.resolveContextAttempt(t.Context(), a, source); err == nil {
		t.Fatal("closed store validated context")
	}
	if err := s.bindContextSnapshot(t.Context(), a, []ContextEntry{source}); err == nil {
		t.Fatal("closed store bound context")
	}
}

func TestDamagedFileIdentityDoesNotReturnPartialInventory(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "run"}, Right{"agent", "a", "chat"})
	h, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.SaveFile(t.Context(), h, "valid.txt", []byte("PRIVATE_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO access_files(id,attempt_id,workspace_id,principal_id,scope,agent_id,name,content,size_bytes,sha256,created_at) SELECT NULL,attempt_id,workspace_id,principal_id,scope,agent_id,'broken.txt',content,size_bytes,sha256,created_at FROM access_files WHERE id=?`, file.ID); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.FilesForChat(t.Context(), "h1", "w", "a", "c1"); err == nil || rows != nil {
		t.Fatalf("damaged inventory returned a partial success: %#v %v", rows, err)
	}
	if _, err := s.SaveFile(t.Context(), h, "broken.txt", []byte("PRIVATE_FILE")); err == nil {
		t.Fatal("damaged stored identity accepted as idempotent write")
	}
}

func TestMissingAuthorityTablesRefuseRuntimeResolution(t *testing.T) {
	for _, table := range []string{"restricted_workflow_attempt_roots", "attempt_project_inputs"} {
		t.Run(table, func(t *testing.T) {
			s := fixture(t)
			policy(t, s, "h1", Right{"agent", "a", "run"})
			h, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(`ALTER TABLE ` + table + ` RENAME TO inaccessible_authority`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Resolve(t.Context(), h); err == nil {
				t.Fatal("incomplete authority schema granted runtime access")
			}
		})
	}
}
