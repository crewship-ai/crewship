package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestScheduledAuthorizerRejectsMissingAndMalformedAuthority(t *testing.T) {
	for _, scenario := range []string{"invalid occurrence", "missing agent", "missing crew", "closed database"} {
		t.Run(scenario, func(t *testing.T) {
			db, store := dueFixture(t)
			receipt, err := acceptDue(t.Context(), db, store, "ws1", work.IngressLimits{})
			if err != nil {
				t.Fatal(err)
			}
			item, err := store.Get(t.Context(), receipt.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			var want dispatch.Decision
			switch scenario {
			case "invalid occurrence":
				var input ScheduledInput
				if err := json.Unmarshal([]byte(item.InputJSON), &input); err != nil {
					t.Fatal(err)
				}
				input.Occurrence = "not-a-timestamp"
				data, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				item.InputJSON, item.SourceRef = string(data), input.Occurrence
				sum := sha256.Sum256(data)
				item.InputSHA256 = hex.EncodeToString(sum[:])
				want = dispatch.Refuse("scheduled work has invalid occurrence identity")
			case "missing agent":
				// The accepted item can outlive an erased identity. Change the
				// immutable fixture together, so the database check must decide.
				var input ScheduledInput
				if err := json.Unmarshal([]byte(item.InputJSON), &input); err != nil {
					t.Fatal(err)
				}
				input.AgentID = "erased-agent"
				data, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				item.AgentID, item.InputJSON = input.AgentID, string(data)
				sum := sha256.Sum256(data)
				item.InputSHA256 = hex.EncodeToString(sum[:])
				want = dispatch.Refuse("scheduled agent or workspace no longer exists")
			case "missing crew":
				// Corrupt legacy references must not become executable authority.
				if _, err := db.ExecContext(t.Context(), `PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(t.Context(), `UPDATE agents SET crew_id='erased-crew' WHERE id='a1'`); err != nil {
					t.Fatal(err)
				}
				item.CrewID = "erased-crew"
				want = dispatch.Refuse("scheduled agent crew no longer exists")
			case "closed database":
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			got, err := NewScheduledAuthorizer(db).Authorize(t.Context(), dispatch.Assignment{Item: item})
			if scenario == "closed database" {
				if err == nil || !strings.Contains(err.Error(), "read agent at dispatch") {
					t.Fatalf("database failure authorized work: %+v, %v", got, err)
				}
			} else if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestCursorInitializationReportsStorageFailures(t *testing.T) {
	if err := (&Scheduler{}).InitializeMissingCursors(t.Context(), acceptanceNow); err == nil {
		t.Fatal("missing database accepted")
	}
	db, store := dueFixture(t)
	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	if _, err := db.Exec(`UPDATE agents SET schedule_next_run=NULL WHERE id='a1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_new_cursor BEFORE UPDATE OF schedule_next_run ON agents BEGIN SELECT RAISE(ABORT, 'disk unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeMissingCursors(t.Context(), acceptanceNow); err == nil || !strings.Contains(err.Error(), "initialize cursor for a1") {
		t.Fatalf("cursor write failure = %v", err)
	}
	_, next := agentSchedule(t, db, "a1")
	if next.Valid {
		t.Fatal("failed initialization published a due instant")
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := AcceptDueTx(t.Context(), tx, store, "ws1", "a1", acceptanceNow, work.IngressLimits{}); err == nil || !strings.Contains(err.Error(), "read due occurrence") {
		t.Fatalf("finished transaction accepted work: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeMissingCursors(t.Context(), acceptanceNow); err == nil || !strings.Contains(err.Error(), "list schedules") {
		t.Fatalf("closed database initialization = %v", err)
	}
}

func TestDueAcceptanceRefusesInvalidDurabilityPrerequisites(t *testing.T) {
	if _, err := AcceptDue(t.Context(), nil, nil, "ws1", "a1", acceptanceNow, work.IngressLimits{}); err == nil || !strings.Contains(err.Error(), "acceptor") {
		t.Fatalf("unconfigured acceptance = %v", err)
	}
	for _, cron := range []string{"invalid cron", "0 0 31 2 *"} {
		t.Run(cron, func(t *testing.T) {
			db, store := dueFixture(t)
			if _, err := db.ExecContext(t.Context(), `UPDATE agents SET schedule_cron=? WHERE id='a1'`, cron); err != nil {
				t.Fatal(err)
			}
			if _, err := acceptDue(t.Context(), db, store, "ws1", work.IngressLimits{}); err == nil {
				t.Fatal("invalid schedule accepted")
			}
			_, next := agentSchedule(t, db, "a1")
			if next.String != acceptanceDue {
				t.Fatalf("failed acceptance advanced cursor: %v", next)
			}
		})
	}
}
