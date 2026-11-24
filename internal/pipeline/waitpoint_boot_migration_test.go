package pipeline

import (
	"database/sql/driver"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
	"modernc.org/sqlite"
)

func TestWaitpointBootMigrationCrash(t *testing.T) {
	const env = "CREWSHIP_TEST_WAITPOINT_CRASH_DB"
	if path := os.Getenv(env); path != "" {
		sqlite.MustRegisterScalarFunction("interrupt_conversion", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			os.Exit(72)
			return nil, nil
		})
		db, err := database.Open("file:" + path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("CREATE TEMP TRIGGER interrupt AFTER UPDATE OF source_id ON main.inbox_items BEGIN SELECT interrupt_conversion(); END"); err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if err := database.Migrate(t.Context(), db.DB, slog.Default()); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "migration.db")
	db := testutil.MigratedDBAt(t, path)
	if _, err := db.Exec(`DELETE FROM _migrations WHERE name='convert_waitpoint_identities';
 INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','ws');
 INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,timeout_at) VALUES('old','ws','run','gate','approval','2099-01-01');
 INSERT INTO inbox_items(id,workspace_id,kind,source_id,title) VALUES('ibx_ws_waitpoint_old','ws','waitpoint','old','Approve')`); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWaitpointBootMigrationCrash$")
	child.Env = append(os.Environ(), env+"="+path)
	out, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 72 {
		t.Fatalf("migration did not reach simulated crash: %v\n%s", err, out)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM pipeline_waitpoints w JOIN inbox_items i ON i.source_id=w.token WHERE token='old'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rollback count %d: %v", n, err)
	}
	if err := database.Migrate(t.Context(), db.DB, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM pipeline_waitpoints w JOIN inbox_items i ON i.source_id=w.token WHERE token=? AND callback_token_hash IS NULL", LegacyWaitpointPublicID("ws", "old")).Scan(&n); err != nil || n != 1 {
		t.Fatalf("restart count %d: %v", n, err)
	}
}
