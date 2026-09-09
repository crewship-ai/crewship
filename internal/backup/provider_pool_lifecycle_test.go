package backup_test

import (
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/providerpool"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestRetiredProviderPoolRoundTrip(t *testing.T) {
	source := testutil.MigratedSQLDB(t)
	_, err := source.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug) VALUES ('ws','Pool','pool');
	INSERT INTO provider_login_pools(id,workspace_id,name,provider,mode,revision,deleted_at) VALUES ('pool','ws','Retired','OPENAI','api_key',3,'2026-09-08T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, "ws")
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.MigratedSQLDB(t)
	if err := backup.RestoreDump(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	var revision int
	var deleted string
	if err := target.QueryRowContext(t.Context(), `SELECT revision,deleted_at FROM provider_login_pools WHERE id='pool'`).Scan(&revision, &deleted); err != nil || revision != 3 || deleted != "2026-09-08T00:00:00Z" {
		t.Fatalf("retirement lost: %d %q %v", revision, deleted, err)
	}
	if _, err := providerpool.NewStore(target).Choose(t.Context(), "ws", "pool", time.Now()); !errors.Is(err, providerpool.ErrNotFound) {
		t.Fatalf("restored retirement bypass: %v", err)
	}
}
