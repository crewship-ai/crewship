package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

func iterationFixture(t *testing.T) (*artifactPrincipalFixture, *documentAuthority, string, documents.IterationIntent) {
	t.Helper()
	f, authority, _, created := newDocumentDraftFixture(t, true)
	f.a.RunID = "iteration-run"
	f.a.ChatID = "iteration-chat"
	mustExec(t, f.rig.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, f.a.ChatID, f.a.WorkspaceID, f.a.AgentID, f.a.HumanUserID)
	var err error
	f.receipt, err = chataudience.CaptureTrusted(t.Context(), f.rig.db, f.a.ChatID, f.a.HumanUserID, nil)
	if err != nil || f.receipt == nil {
		t.Fatalf("new private target chat: %v", err)
	}
	f.a.MessageID = "iteration-message"
	mustExec(t, f.rig.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES(?,?,?,'user','Change the existing introduction','2026-10-19T00:00:00Z',?)`, f.a.MessageID, f.a.ChatID, f.a.AgentID, f.a.HumanUserID)
	if err := f.capture(httptest.NewRequest("POST", "/", nil), f.a.MessageID, f.receipt); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('iteration-start',?,'principal-crew','principal-agent','run.started','system','test',?)`, f.a.WorkspaceID, f.a.RunID)
	intent := documents.IterationIntent{SubmissionID: "followup", PageID: created.PageID, Schema: 3}
	if err := f.pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatalf("persist iteration: %v", err)
	}
	if err := bindIterationFixture(t, f, intent); err != nil {
		t.Fatalf("bind iteration: %v", err)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	return f, authority, token, intent
}

func bindIterationFixture(t *testing.T, f *artifactPrincipalFixture, intent documents.IterationIntent) error {
	t.Helper()
	request := httptest.NewRequest("POST", "/", nil)
	tx, err := f.rig.db.BeginTx(t.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	if err = bindDocumentIterationRunTx(request, f.rig.db, tx, f.a.RunID, f.a.MessageID, f.receipt, intent); err != nil {
		return err
	}
	return tx.Commit()
}

func TestDocumentIterationAuthorFollowupUsesExistingPage(t *testing.T) {
	f, _, _, intent := iterationFixture(t)
	scope, err := f.pages.documentIterationScope(context.Background(), &f.a, intent.PageID)
	if err != nil || scope.PageID != intent.PageID {
		t.Fatalf("current author followup cannot edit its original Page: scope=%+v err=%v", scope, err)
	}
	var previous string
	if err := f.rig.db.QueryRow(`SELECT result_page_id FROM document_creation_consents WHERE run_id='principal-run'`).Scan(&previous); err != nil || previous != intent.PageID {
		t.Fatalf("creation grant changed: %q %v", previous, err)
	}
	if err := bindIterationFixture(t, f, intent); err != nil {
		t.Fatalf("exact original admission retry: %v", err)
	}
}

func TestDocumentIterationRejectsChangedAuthorityAndOriginalBounds(t *testing.T) {
	for _, scenario := range []string{"human", "Page", "revision", "generation", "audience", "Page version", "owner", "policy", "member", "chat", "terminal", "revoked", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			f, _, _, intent := iterationFixture(t)
			page := intent.PageID
			switch scenario {
			case "human":
				f.a.HumanUserID = "different"
			case "Page":
				page = "other"
			case "revision":
				mustExec(t, f.rig.db, `UPDATE documents SET head_rev=head_rev+1 WHERE page_id=?`, page)
			case "generation":
				mustExec(t, f.rig.db, `UPDATE documents SET generation=generation+1 WHERE page_id=?`, page)
			case "audience":
				mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, page)
			case "Page version":
				mustExec(t, f.rig.db, `UPDATE pages SET pages_version=pages_version+1 WHERE id=?`, page)
			case "owner":
				mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=?,pages_version=pages_version+1 WHERE id=?`, f.rig.adminID, page)
			case "policy":
				mustExec(t, f.rig.db, `UPDATE workspace_artifact_creation_policy SET version=version+1 WHERE workspace_id=?`, f.a.WorkspaceID)
			case "member":
				mustExec(t, f.rig.db, `UPDATE workspace_members SET access_revision=access_revision+1 WHERE user_id=?`, f.a.HumanUserID)
			case "chat":
				mustExec(t, f.rig.db, `UPDATE chats SET authority_revision=authority_revision+1 WHERE id=?`, f.a.ChatID)
			case "terminal":
				mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('iteration-end',?,'principal-crew','principal-agent','run.cancelled','system','test',?)`, f.a.WorkspaceID, f.a.RunID)
			case "revoked":
				mustExec(t, f.rig.db, `UPDATE document_iteration_intents SET revoked_at='2026-10-19T00:00:00Z'`)
			case "expired":
				mustExec(t, f.rig.db, `UPDATE artifact_run_principals SET expires_at='2000-01-01T00:00:00Z' WHERE run_id=?`, f.a.RunID)
			}
			if _, err := f.pages.documentIterationScope(t.Context(), &f.a, page); err == nil {
				t.Fatal("changed authority renewed the original targeted mandate")
			}
		})
	}
}

func TestDocumentIterationGuardAdvancesOnlyAtomicAuthorizedWrites(t *testing.T) {
	f, _, _, intent := iterationFixture(t)
	key := documents.Key{WorkspaceID: f.a.WorkspaceID, PageID: intent.PageID}
	actor := "agent:" + f.a.AgentID + ":" + f.a.RunID
	for _, commit := range []bool{false, true} {
		tx, err := f.rig.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		state := documents.State{Schema: 3, Generation: intent.Generation, HeadRev: intent.Rev}
		if err = f.pages.documentIterationGuard(&f.a, key, "commit", 0)(t.Context(), tx, actor, state); err != nil {
			t.Fatal(err)
		}
		if commit {
			if _, err = tx.Exec(`UPDATE documents SET head_rev=head_rev+1 WHERE page_id=?`, intent.PageID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
		} else {
			_ = tx.Rollback()
		}
		var rev int64
		if err = f.rig.db.QueryRow(`SELECT draft_rev FROM document_iteration_intents`).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		want := int64(0)
		if commit {
			want = 1
		}
		if rev != want {
			t.Fatalf("cursor=%d want %d", rev, want)
		}
		if err := bindIterationFixture(t, f, intent); err != nil {
			t.Fatalf("original admission retry after own commit: %v", err)
		}
		if err := f.pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, intent); err != nil {
			t.Fatalf("original send retry after own commit: %v", err)
		}
	}
}

func TestDocumentIterationSharedDefaultNeverFallsBackToDirect(t *testing.T) {
	f, _, _, intent := iterationFixture(t)
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, intent.PageID)
	_, _, err := f.pages.documentIterationPage(t.Context(), f.a.WorkspaceID, f.a.HumanUserID, intent)
	if !errors.Is(err, errDocumentIterationProposalUnavailable) {
		t.Fatalf("shared default did not require proposal: %v", err)
	}
}

// Actual SQLite writer/receipt/checkpoint boundary with the existing Store.
// Binary update payloads are fixture bytes; this is not editor/engine acceptance.
func TestDocumentIterationStoreCommitRetryCheckpointAndRevocation(t *testing.T) {
	f, authority, _, intent := iterationFixture(t)
	key := documents.Key{WorkspaceID: f.a.WorkspaceID, PageID: intent.PageID}
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		if _, err := f.pages.documentIterationScope(ctx, &f.a, intent.PageID); err != nil {
			return "", err
		}
		return "agent:" + f.a.AgentID + ":" + f.a.RunID, nil
	}
	lease, err := authority.store.Acquire(t.Context(), key, "iteration-engine", authorize)
	if err != nil {
		t.Fatal(err)
	}
	commit := documents.Commit{Lease: lease, ID: "iteration-update", Fingerprint: "original-intent", IfRev: intent.Rev, IfGeneration: &intent.Generation, Schema: 3, Kind: "agent", Update: []byte("fixture-update"), CandidateSnapshot: []byte("fixture-candidate"), Projection: json.RawMessage(`[{"id":"paragraph","hash":"updated"}]`), IfHashes: map[string]string{"paragraph": "synthetic"}, Guard: f.pages.documentIterationGuard(&f.a, key, "commit", intent.Rev)}
	mustExec(t, f.rig.db, `CREATE TRIGGER reject_iteration_receipt BEFORE INSERT ON document_receipts WHEN NEW.id='iteration-update' BEGIN SELECT RAISE(ABORT,'fixture storage failure'); END`)
	if _, err = authority.store.Commit(t.Context(), key, commit, authorize); err == nil {
		t.Fatal("failed receipt write committed")
	}
	var cursor, head int64
	if err = f.rig.db.QueryRow(`SELECT i.draft_rev,d.head_rev FROM document_iteration_intents i JOIN documents d ON d.page_id=i.page_id`).Scan(&cursor, &head); err != nil || cursor != 0 || head != 0 {
		t.Fatalf("partial commit: cursor=%d head=%d err=%v", cursor, head, err)
	}
	mustExec(t, f.rig.db, `DROP TRIGGER reject_iteration_receipt`)
	first, err := authority.store.Commit(t.Context(), key, commit, authorize)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := authority.store.Commit(t.Context(), key, commit, authorize)
	if err != nil || repeated != first {
		t.Fatalf("exact lost-ACK retry: %v %v", repeated, err)
	}
	if _, err = authority.store.CheckpointRequestGuarded(t.Context(), key, lease, "iteration-pin", "original-pin", []byte("fixture-candidate"), first.Rev, &intent.Generation, authorize, f.pages.documentIterationGuard(&f.a, key, "checkpoint", first.Rev)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `UPDATE document_iteration_intents SET revoked_at='2026-10-19T00:00:00Z'`)
	if _, err = authority.store.Commit(t.Context(), key, commit, authorize); err == nil {
		t.Fatal("exact receipt replay bypassed live revocation")
	}
	var receipts int
	if err = f.rig.db.QueryRow(`SELECT count(*) FROM document_receipts WHERE page_id=? AND id='iteration-update'`, intent.PageID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("duplicate durable mutation: %d %v", receipts, err)
	}
}

func TestDocumentIterationRetainedIntentCannotRetargetOrRenew(t *testing.T) {
	f, _, _, intent := iterationFixture(t)
	for _, query := range []string{
		`UPDATE document_iteration_intents SET page_id='other'`,
		`UPDATE document_iteration_intents SET original_rev=1`,
		`UPDATE document_iteration_intents SET document_generation=1`,
		`UPDATE document_iteration_intents SET expires_at='2100-01-01T00:00:00Z'`,
		`UPDATE document_iteration_intents SET run_id='different'`,
	} {
		if _, err := f.rig.db.Exec(query); err == nil {
			t.Fatalf("retained intent changed: %s", query)
		}
	}
	selected, err := f.pages.documentIterationRun(t.Context(), &f.a)
	if err != nil || !selected {
		t.Fatalf("targeted dispatch: %v/%v", selected, err)
	}
	mustExec(t, f.rig.db, `UPDATE document_iteration_intents SET revoked_at='2026-10-19T00:00:00Z'`)
	selected, err = f.pages.documentIterationRun(t.Context(), &f.a)
	if err != nil || !selected {
		t.Fatal("revoked intent could fall back to creation authority")
	}
	changed := intent
	changed.PageID = "different"
	if err = bindIterationFixture(t, f, changed); err == nil {
		t.Fatal("regrant retargeted original message")
	}
}

func TestDocumentIterationRetainsPrivateCopiedContextAfterGrantEnds(t *testing.T) {
	f, _, _, _ := iterationFixture(t)
	for _, remove := range []bool{false, true} {
		if remove {
			mustExec(t, f.rig.db, `DELETE FROM document_iteration_intents`)
		}
		context, err := chataudience.ResolvePrivateArtifactContext(t.Context(), f.rig.db, f.a.WorkspaceID, f.a.ChatID)
		if err != nil || !context.Sealed || context.AuthorUserID != f.a.HumanUserID || context.Decide(f.rig.adminID, "ADMIN") != chataudience.ArtifactContextDenied {
			t.Fatalf("copied private context widened after grant end=%v: %+v %v", remove, context, err)
		}
	}
}
