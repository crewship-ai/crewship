package backup

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

func TestServiceSnapshotExpiredProducerCannotPublishAfterRecovery(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	_, crew := seedCovWorkspace(t, db, "expired_snapshot")
	old, err := servicelifecycle.BeginBackupFence(ctx, db, crew, "backup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = servicelifecycle.AdoptBackupFence(ctx, db, crew, "backup"); err == nil {
		t.Fatal("recovery stole live producer")
	}
	if _, err = db.Exec(`UPDATE service_backup_fences SET producer_until='2000-01-01T00:00:00Z' WHERE crew_id=?`, crew); err != nil {
		t.Fatal(err)
	}
	current, err := servicelifecycle.AdoptBackupFence(ctx, db, crew, "backup")
	if err != nil {
		t.Fatal(err)
	}
	published := false
	err = publishServiceSnapshotBundle(ctx, db, []serviceBackupFence{{crew: crew, token: old}}, func() error { published = true; return nil })
	if err == nil || published {
		t.Fatal("stale capture published archive")
	}
	if err = servicelifecycle.EndBackupFence(ctx, db, crew, old); err == nil {
		t.Fatal("stale producer cleared recovered epoch")
	}
	if err = servicelifecycle.RenewBackupFence(ctx, db, crew, old); err == nil {
		t.Fatal("stale heartbeat regained epoch")
	}
	if err = publishServiceSnapshotBundle(ctx, db, []serviceBackupFence{{crew: crew, token: current}}, func() error { published = true; return nil }); err != nil || !published {
		t.Fatalf("fresh fenced producer denied: %v", err)
	}
}
