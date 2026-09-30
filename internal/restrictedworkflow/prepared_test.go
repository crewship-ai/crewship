package restrictedworkflow

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
)

func TestPreparedSourceRightsAtomicEnqueueAndEveryStep(t *testing.T) {
	s, runner := fixture(t)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO projects(id,workspace_id,name,slug) VALUES('project','w','Project','project')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO missions(id,workspace_id,crew_id,delegate_agent_id,lead_agent_id,trace_id,title,description,mission_type,status,project_id) VALUES('source-issue','w','crew','agent','agent','source-trace','Issue','Source','issue','TODO','project')`); err != nil {
		t.Fatal(err)
	}
	store := runner.Authority.Store
	m, err := store.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	rights := []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}, {Kind: "project", ID: "project", Operation: "read"}}
	if _, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", m, rights); err != nil {
		t.Fatal(err)
	}
	extra := rights[1:]
	prepare := func() *PreparedInvocation {
		p, err := s.PrepareManualWithRights(t.Context(), "h1", "w", "private-work", map[string]any{"task": "CLASSIFIED_PROJECT_INPUT"}, "", extra, "issue")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := prepare()
	var queued int
	if err = s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM restricted_workflow_jobs`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("prepared unexpectedly queued %d %v", queued, err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnqueuePrepared(t.Context(), tx, p); !errors.Is(err, ErrDenied) {
		t.Fatalf("absent source checker allowed %v", err)
	}
	_ = tx.Rollback()
	if err = s.CancelPrepared(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	p = prepare()
	meta := p.Metadata()
	s.SourceChecker = func(ctx context.Context, q pipeline.PageActionQuery, origin string) error {
		if origin != meta.OriginAttemptID {
			return ErrDenied
		}
		return nil
	}
	tx, err = s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Source binding and job must commit together; production preflight owns this row.
	if _, err = tx.ExecContext(t.Context(), `INSERT INTO restricted_preflight_reservations(origin_attempt_id,workflow_id,workspace_id,principal_id,member_id,member_revision,agent_id,issue_id,project_id,source_hash,authority_hash,recipe_hash,work_revision,brief_revision)
 SELECT ?,?,'w','h1',?,?, 'agent','source-issue','project',?,?,?,iw.revision,iw.brief_revision FROM issue_work iw WHERE iw.mission_id='source-issue'`, meta.OriginAttemptID, meta.WorkflowID, meta.MemberID, meta.MemberRevision, hash("synthetic-source"), hash("synthetic-authority"), meta.RecipeHash); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	receipt, err := s.EnqueuePrepared(t.Context(), tx, p)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	starts := 0
	base := runner.StartSession
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		a, err := store.Resolve(ctx, handle)
		if err != nil {
			return nil, err
		}
		starts++
		found := false
		for _, right := range a.Rights {
			if right == extra[0] {
				found = true
			}
		}
		if !found {
			t.Fatal("source rights not preserved on fresh step")
		}
		return base(ctx, handle)
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || err != nil || starts != 2 {
		t.Fatalf("source dispatch %v %v starts%d", worked, err, starts)
	}
	if _, err = s.ReceiptForActor(t.Context(), "h2", "w", receipt.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign receipt exposed")
	}
	if own, err := s.ReceiptForActor(t.Context(), "h1", "w", receipt.ID); err != nil || own.ChatID != meta.ChatID {
		t.Fatalf("own receipt %v %v", own, err)
	}
	s.SourceChecker = nil
	if _, err = s.Result(t.Context(), "h1", "w", receipt.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("missing source checker exposed result")
	}
	s.SourceChecker = func(context.Context, pipeline.PageActionQuery, string) error { return nil }
	if _, err = s.db.ExecContext(t.Context(), `DELETE FROM access_grants WHERE member_id='m1' AND resource_kind='project'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReceiptForActor(t.Context(), "h1", "w", receipt.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("source permission revocation exposed receipt")
	}
}
