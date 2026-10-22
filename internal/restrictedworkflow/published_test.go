//go:build linux

package restrictedworkflow

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// publishedFixture publishes the two-step fixture routine as Guest h1 through
// the real compiler. h2 keeps only Run, so it can invoke but never publish.
func publishedFixture(t *testing.T) (*Service, *restricteddispatch.TextRunner) {
	t.Helper()
	s, runner := fixture(t)
	store := access.Store{DB: s.db}
	m, err := store.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", m, []access.Right{
		{Kind: "routine", ID: "routine", Operation: "view"}, {Kind: "routine", ID: "routine", Operation: "edit"},
		{Kind: "routine", ID: "routine", Operation: "publish"}, {Kind: "routine", ID: "routine", Operation: "run"},
		{Kind: "agent", ID: "agent", Operation: "run"},
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, canonical, fingerprint, err := CompilePublicationIn(t.Context(), s.db, tx, "h1", "w", "routine", routine)
	if err != nil {
		t.Fatal(err)
	}
	publishVersion(t, tx, 1, routine, string(canonical), fingerprint)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, runner
}

func publishVersion(t *testing.T, tx *sql.Tx, version int, raw, canonical, fingerprint string) {
	t.Helper()
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint,publication_authority_json) VALUES(?,'routine',?,?,?,'user','h1',?,?)`, "routine-v"+strconv.Itoa(version), version, raw, hash(raw), fingerprint, canonical); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE pipelines SET head_version=?,definition_json=?,definition_hash=?,publication_authority_fingerprint=? WHERE id='routine'`, version, raw, hash(raw), fingerprint); err != nil {
		t.Fatal(err)
	}
}

func publishedJournal(t *testing.T, s *Service, run, action string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), `SELECT COALESCE(json_extract(payload,'$.reason'),'') FROM journal_entries WHERE json_extract(payload,'$.run_id')=? AND json_extract(payload,'$.action')=? ORDER BY seq`, run, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var reasons []string
	for rows.Next() {
		var reason string
		if err = rows.Scan(&reason); err != nil {
			t.Fatal(err)
		}
		reasons = append(reasons, reason)
	}
	return reasons
}

func TestPublishedRoutineRunCompletesAndSurvivesLogout(t *testing.T) {
	s, runner := publishedFixture(t)
	exec := func(q string, args ...any) {
		if _, err := s.db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO user_sessions(id,user_id,expires_at) VALUES('browser','h1','2999-01-01T00:00:00Z')`)
	base, starts := runner.StartSession, 0
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		starts++
		if starts == 1 {
			// Run authority belongs to the grant and account, not the session.
			exec(`UPDATE user_sessions SET revoked_at=?,revoked_reason='logout' WHERE id='browser'`, time.Now().UTC().Format(time.RFC3339))
		}
		return base(ctx, handle)
	}
	receipt, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "published"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if started := publishedJournal(t, s, receipt.ID, "published_run_started"); len(started) != 1 {
		t.Fatalf("run start audit = %v", started)
	}
	var limit float64
	if err = s.db.QueryRowContext(t.Context(), `SELECT round((julianday(expires_at)-julianday(fire_at))*86400) FROM restricted_workflow_jobs WHERE id=?`, receipt.ID).Scan(&limit); err != nil || limit != DefaultPublishedRunLimit.Seconds() {
		t.Fatalf("published run deadline = %vs, err=%v", limit, err)
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || err != nil {
		t.Fatalf("dispatch worked=%v err=%v", worked, err)
	}
	result, err := s.Result(t.Context(), "h1", "w", receipt.ID)
	if err != nil || result.State != "completed" || starts != 2 || result.Outputs["second"] != "answer-h1" {
		t.Fatalf("logout stopped published run: %+v starts=%d err=%v", result, starts, err)
	}
	if _, err = s.Result(t.Context(), "h2", "w", receipt.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("another Guest read the published run output: %v", err)
	}
	// A run-only Guest consumes the same publication within its own grants.
	other, err := s.AdmitManual(t.Context(), "h2", "w", "private-work", map[string]any{"task": "other"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || err != nil {
		t.Fatalf("dispatch worked=%v err=%v", worked, err)
	}
	if _, err = s.Result(t.Context(), "h1", "w", other.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("publisher read another Guest's output: %v", err)
	}
}

func TestPublishedRoutineRunStopsAtNextStep(t *testing.T) {
	cases := []struct {
		name, reason string
		change       func(t *testing.T, s *Service)
		caller       string
	}{
		{"revoke run grant", stopGrant, func(t *testing.T, s *Service) {
			replaceGuest(t, s, "h1", access.Right{Kind: "routine", ID: "routine", Operation: "view"}, access.Right{Kind: "routine", ID: "routine", Operation: "edit"},
				access.Right{Kind: "routine", ID: "routine", Operation: "publish"}, access.Right{Kind: "routine", ID: "routine", Operation: "run"})
		}, ""},
		{"revoke publish binding", stopGrant, func(t *testing.T, s *Service) {
			replaceGuest(t, s, "h1", access.Right{Kind: "routine", ID: "routine", Operation: "view"}, access.Right{Kind: "routine", ID: "routine", Operation: "edit"},
				access.Right{Kind: "routine", ID: "routine", Operation: "run"}, access.Right{Kind: "agent", ID: "agent", Operation: "run"})
		}, ""},
		{"remove member", stopMembership, func(t *testing.T, s *Service) { mustExec(t, s, `DELETE FROM workspace_members WHERE id='m1'`) }, ""},
		{"suspend account", stopSuspended, func(t *testing.T, s *Service) {
			mustExec(t, s, `UPDATE users SET suspended_at=? WHERE id='h1'`, tsformat.Format(time.Now()))
		}, ""},
		{"admin revokes sessions", stopSessions, func(t *testing.T, s *Service) {
			mustExec(t, s, `INSERT INTO user_sessions(id,user_id,expires_at,revoked_at,revoked_reason) VALUES('forced','h1','2999-01-01T00:00:00Z',?,'admin_invalidate')`, time.Now().UTC().Format(time.RFC3339))
		}, ""},
		{"issuer loses authority", stopIssuer, func(t *testing.T, s *Service) {
			mustExec(t, s, `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`)
		}, ""},
		{"routine withdrawn", stopUnpublished, func(t *testing.T, s *Service) {
			mustExec(t, s, `UPDATE pipelines SET status='disabled' WHERE id='routine'`)
		}, ""},
		{"max duration", stopDuration, func(t *testing.T, s *Service) {
			// Wait until the immutable deadline accepted with this run has passed.
			var expires string
			if err := s.db.QueryRowContext(context.Background(), `SELECT expires_at FROM restricted_workflow_jobs`).Scan(&expires); err != nil {
				t.Fatal(err)
			}
			deadline, err := time.Parse(time.RFC3339Nano, expires)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Until(deadline) + 50*time.Millisecond)
		}, ""},
		// The caller's own policy is untouched here: only the composite
		// publication check sees the publisher's frozen binding disappear.
		{"publisher loses publish for another caller", stopGrant, func(t *testing.T, s *Service) {
			replaceGuest(t, s, "h1", access.Right{Kind: "routine", ID: "routine", Operation: "view"}, access.Right{Kind: "routine", ID: "routine", Operation: "edit"},
				access.Right{Kind: "routine", ID: "routine", Operation: "run"}, access.Right{Kind: "agent", ID: "agent", Operation: "run"})
		}, "h2"},
		{"publisher suspended for another caller", stopSuspended, func(t *testing.T, s *Service) {
			mustExec(t, s, `UPDATE users SET suspended_at=? WHERE id='h1'`, tsformat.Format(time.Now()))
		}, "h2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, runner := publishedFixture(t)
			if tc.reason == stopDuration {
				s.publishedRunLimit = 5 * time.Second
			}
			base, starts := runner.StartSession, 0
			runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
				starts++
				if starts == 1 {
					tc.change(t, s)
				}
				return base(ctx, handle)
			}
			caller := tc.caller
			if caller == "" {
				caller = "h1"
			}
			receipt, err := s.AdmitManual(t.Context(), caller, "w", "private-work", map[string]any{"task": "published"}, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := s.DispatchNext(t.Context()); !worked || err == nil {
				t.Fatalf("revoked run completed worked=%v err=%v", worked, err)
			}
			if starts != 1 {
				t.Fatalf("a step started after detection: starts=%d", starts)
			}
			var state string
			if err = s.db.QueryRowContext(t.Context(), `SELECT state FROM restricted_workflow_jobs WHERE id=?`, receipt.ID).Scan(&state); err == nil && state != "failed" {
				t.Fatalf("stopped run state %q", state)
			}
			if stopped := publishedJournal(t, s, receipt.ID, "published_run_stopped"); len(stopped) != 1 || stopped[0] != tc.reason {
				t.Fatalf("stop audit = %v, want %s", stopped, tc.reason)
			}
		})
	}
}

func TestPublishedRoutineAdmissionRefusals(t *testing.T) {
	t.Run("changed profile", func(t *testing.T) {
		s, _ := publishedFixture(t)
		mustExec(t, s, `UPDATE agents SET restricted_execution_profile='disabled' WHERE id='agent'`)
		if _, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "x"}, "", 0); !errors.Is(err, ErrDenied) {
			t.Fatalf("disallowed profile admitted: %v", err)
		}
	})
	t.Run("non agent step under the same manifest", func(t *testing.T) {
		s, _ := publishedFixture(t)
		nested := `{"dsl_version":"1.0","name":"private-work","inputs":[{"name":"task","type":"string","required":true}],"steps":[{"id":"first","type":"agent_run","agent_slug":"worker","prompt":"{{ inputs.task }}"},{"id":"second","type":"call_pipeline","pipeline_slug":"private-work","inputs":{}}]}`
		mustExec(t, s, `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint,publication_authority_json)
 SELECT 'routine-v2',pipeline_id,2,?,?,'user','h1',authority_fingerprint,publication_authority_json FROM pipeline_versions WHERE id='routine-v1'`, nested, hash(nested))
		mustExec(t, s, `UPDATE pipelines SET head_version=2,definition_json=?,definition_hash=? WHERE id='routine'`, nested, hash(nested))
		if _, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "x"}, "", 0); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("nested publication step admitted: %v", err)
		}
	})
	t.Run("page action", func(t *testing.T) {
		s, _ := publishedFixture(t)
		graph, err := s.compileGraph(t.Context(), "h1", "w", "routine")
		if err != nil || graph.Publication == nil {
			t.Fatalf("published graph calibration: %+v %v", graph, err)
		}
		if _, err = s.admit(t.Context(), "h1", "w", "private-work", map[string]any{"task": "x"}, "", 0, &pipeline.PageActionInvocation{PipelineID: "routine"}, "", nil); !errors.Is(err, ErrDenied) {
			t.Fatalf("published routine admitted through a Page action: %v", err)
		}
	})
	t.Run("missing publisher authority", func(t *testing.T) {
		s, _ := publishedFixture(t)
		mustExec(t, s, `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`)
		if _, err := s.AdmitManual(t.Context(), "h2", "w", "private-work", map[string]any{"task": "x"}, "", 0); !errors.Is(err, ErrDenied) {
			t.Fatalf("publication without live issuer admitted: %v", err)
		}
	})
}

func replaceGuest(t *testing.T, s *Service, user string, rights ...access.Right) {
	t.Helper()
	store := access.Store{DB: s.db}
	m, err := store.Membership(t.Context(), user, "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "owner", user, "w", "restricted", m, rights); err != nil {
		t.Fatal(err)
	}
}

func mustExec(t *testing.T, s *Service, q string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
