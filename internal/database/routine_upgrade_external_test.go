package database_test

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestT4RoutineUpgradePreservesLegacyApproval(t *testing.T) {
	db := database.RoutineUpgradeTestDB(t)
	store := pipeline.NewSQLWaitpointStore(db)
	defer store.Close()
	if err := store.CompleteApproval(context.Background(), "upgrade-ws", "pending", true, "reviewer", ""); err != nil {
		t.Fatal(err)
	}
	output, err := store.ApprovalOutput(context.Background(), "upgrade-ws", "pending")
	if err != nil || output != "waited:approval:approved" {
		t.Fatalf("legacy output: %q %v", output, err)
	}
}
