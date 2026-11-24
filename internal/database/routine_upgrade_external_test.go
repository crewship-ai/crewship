package database_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestT4RoutineUpgradePreservesLegacyApproval(t *testing.T) {
	db := database.RoutineUpgradeTestDB(t)
	store := pipeline.NewSQLWaitpointStore(db)
	defer store.Close()
	// #1910: the startup schema migration re-keys legacy waitpoints (their old token was
	// a callback capability) to a public wp_ id. The pending legacy approval
	// must still be decidable under that new id.
	var id string
	if err := db.QueryRowContext(context.Background(), `SELECT token FROM pipeline_waitpoints WHERE workspace_id='upgrade-ws' AND pipeline_run_id='legacy-run' AND status='pending'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, pipeline.WaitpointIDPrefix) {
		t.Fatalf("legacy waitpoint not re-keyed: %q", id)
	}
	if err := store.CompleteApproval(context.Background(), "upgrade-ws", id, true, "reviewer", ""); err != nil {
		t.Fatal(err)
	}
	output, err := store.ApprovalOutput(context.Background(), "upgrade-ws", id)
	if err != nil || output != "waited:approval:approved" {
		t.Fatalf("legacy output: %q %v", output, err)
	}
}
