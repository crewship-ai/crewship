package mooring

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

func TestWorkbookHostCrashHelper(t *testing.T) {
	phase := os.Getenv("WORKBOOK_CRASH_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	db, err := database.Open("file:" + os.Getenv("WORKBOOK_CRASH_DB"))
	if err != nil {
		t.Fatal(err)
	}
	store := New(db.DB, Options{Enabled: true})
	engine := &fakeEngine{}
	if phase == "before" {
		engine.prepare = func(context.Context, Binding, State, Command) (Candidate, error) {
			os.Exit(73)
			return Candidate{}, nil
		}
	}
	auth := func(context.Context, *sql.Tx) (Principal, error) {
		return Principal{AuthorityID: "user:owner", MandateID: "human-import", Targets: []string{"*"}}, nil
	}
	if _, err = store.Execute(t.Context(), Key{"ws", "page"}, edit(), auth, engine); err != nil {
		t.Fatal(err)
	}
	os.Exit(74) // committed, but no caller acknowledgement was delivered
}
func TestWorkbookActualHostProcessDeathBeforeAndAfterCommit(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			store, db, auth, key := fixture(t)
			imported(t, store, auth, key, &fakeEngine{})
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(t.Context(), binary, "-test.run=^TestWorkbookHostCrashHelper$", "-test.timeout=30s")
			child.Env = []string{"WORKBOOK_CRASH_PHASE=" + phase, "WORKBOOK_CRASH_DB=" + db.Path()}
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			wantExit := 74
			if phase == "before" {
				wantExit = 73
			}
			if !errors.As(err, &exit) || exit.ExitCode() != wantExit {
				t.Fatalf("child=%v %s", err, output)
			}
			current, err := store.Read(t.Context(), key, auth)
			if err != nil {
				t.Fatal(err)
			}
			wantRev := int64(2)
			if phase == "before" {
				wantRev = 1
			}
			if current.Rev != wantRev {
				t.Fatalf("head=%d want=%d", current.Rev, wantRev)
			}
			fresh := New(db.DB, Options{Enabled: true})
			engine := &fakeEngine{}
			if phase == "before" {
				if _, err = fresh.Execute(t.Context(), key, edit(), auth, engine); !errors.Is(err, ErrInProgress) {
					t.Fatalf("lost live reservation: %v", err)
				}
				// Expiry is deterministic in this test; no sleep or fake commit is used.
				if _, err = db.Exec(`UPDATE gridcraft_operations SET lease_until=0 WHERE mutation_id='edit-1'`); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := fresh.Execute(t.Context(), key, edit(), auth, engine)
			if err != nil || receipt.Rev != 2 {
				t.Fatalf("recovery receipt=%+v %v", receipt, err)
			}
			calls := int32(0)
			if phase == "before" {
				calls = 1
			}
			if engine.calls.Load() != calls {
				t.Fatalf("candidate reran after confirmed commit: %d", engine.calls.Load())
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM gridcraft_revisions`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("duplicate revision=%d %v", count, err)
			}
		})
	}
}
