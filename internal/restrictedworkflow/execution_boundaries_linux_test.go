//go:build linux

package restrictedworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestWorkflowExecutionAdmissionFailuresNeverStartWorker(t *testing.T) {
	s, runner := fixture(t)
	receipt := admitFixtureJob(t, s)
	original, err := s.load(t.Context(), receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := encryption.Decrypt(original.Handle)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner.StartSession = func(context.Context, string) (restricteddispatch.TextSession, error) {
		calls++
		return nil, errors.New("unexpected worker start")
	}
	assignment := dispatch.Assignment{Item: &work.Item{ID: receipt.ID, DomainID: receipt.ID}}
	for _, tc := range []struct {
		name   string
		mutate func(*job)
	}{
		{"invalid graph", func(j *job) { j.Graph = "{" }},
		{"invalid rights", func(j *job) { j.Rights = "{" }},
		{"foreign principal", func(j *job) { j.Principal = "unknown" }},
		{"unowned assignment", func(*job) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := original
			tc.mutate(&j)
			if out, proof, err := s.executeGraph(t.Context(), j, handle, assignment); out != nil || proof != "" || err == nil {
				t.Fatalf("execution admission succeeded: %#v %q %v", out, proof, err)
			}
		})
	}
	s.executor = plainWorkflowExecutor{}
	if _, _, err := s.executeGraph(t.Context(), original, handle, assignment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("untyped execution accepted: %v", err)
	}
	if calls != 0 {
		t.Fatalf("worker started %d times before authority", calls)
	}
}

func TestWorkflowPageReceiptRechecksFrozenActionBinding(t *testing.T) {
	s, _ := fixture(t)
	action := seedPage(t, s)
	receipt, err := s.AdmitPage(t.Context(), "h1", "w", action, map[string]any{"task": "private"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.load(t.Context(), receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := encryption.Decrypt(original.Handle)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*job)
	}{
		{"malformed action", func(j *job) { j.PageAction = "{" }},
		{"changed page hash", func(j *job) { j.PageHash = "obsolete" }},
		{"changed action digest", func(j *job) {
			a := action
			a.ActionDigest = "obsolete"
			raw, _ := json.Marshal(a)
			j.PageAction = string(raw)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := original
			tc.mutate(&j)
			if err := s.checkJob(t.Context(), s.db, j, handle); !errors.Is(err, ErrDenied) {
				t.Fatalf("invalid Page provenance accepted: %v", err)
			}
		})
	}
	if err = s.checkJob(t.Context(), s.db, original, handle); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowDirectoriesReportMissingStorage(t *testing.T) {
	for _, table := range []string{"pipelines", "restricted_workflow_jobs"} {
		t.Run(table, func(t *testing.T) {
			s, _ := fixture(t)
			if _, err := s.db.ExecContext(t.Context(), "DROP TABLE "+table); err != nil {
				t.Fatal(err)
			}
			if table == "pipelines" {
				if rows, err := s.Catalog(t.Context(), "h1", "w"); err == nil || rows != nil {
					t.Fatalf("missing catalog storage reported empty success: %#v %v", rows, err)
				}
			} else {
				if rows, err := s.ResultsForActor(t.Context(), "h1", "w"); err == nil || rows != nil {
					t.Fatalf("missing receipt storage reported empty success: %#v %v", rows, err)
				}
			}
		})
	}
}

func TestWorkflowLifecycleAndPreparationRefuseUnavailableAuthority(t *testing.T) {
	s, _ := fixture(t)
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context()); !errors.Is(err, ErrDenied) {
		t.Fatalf("duplicate dispatcher started: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareManualWithRights(t.Context(), "missing", "w", "private-work", nil, "", []access.Right{{Kind: "project", ID: "project", Operation: "read"}}, "issue"); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing source principal admitted: %v", err)
	}
	s.providers = nil
	if _, err := s.compileGraph(t.Context(), "h1", "w", "routine"); !errors.Is(err, ErrDenied) {
		t.Fatalf("graph without provider adapter accepted: %v", err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = s.freezeGraph(t.Context(), tx, job{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("unavailable graph frozen: %v", err)
	}
}
