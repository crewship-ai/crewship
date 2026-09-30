package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/quota"
)

type snapshotProbe struct {
	stop, export int
	fail         bool
}

func (s *snapshotProbe) QuotaSnapshotNamespace() string { return "synthetic-instance" }
func (s *snapshotProbe) StopCrewService(context.Context, string, string, string) error {
	s.stop++
	return nil
}
func (s *snapshotProbe) ExportQuotaVolume(_ context.Context, _ quota.Key, size int64, w io.Writer) error {
	s.export++
	if s.fail {
		return errors.New("snapshot failed")
	}
	_, err := io.CopyN(w, constantSnapshotBytes{}, size)
	return err
}
func (s *snapshotProbe) ImportQuotaVolume(context.Context, quota.Key, int64, io.Reader) error {
	return nil
}

type constantSnapshotBytes struct{}

func (constantSnapshotBytes) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestServiceCaptureRequiresTransportAndRetainsFailureFence(t *testing.T) {
	db := openMigratedDBCov(t)
	_, crew := seedCovWorkspace(t, db, "quota_snapshot")
	body := `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`
	if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, body, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version,updated_at) VALUES('snapshot-intent',?,'database','running',7,'2026-09-30T18:20:00Z')`, crew); err != nil {
		t.Fatal(err)
	}
	var sink bytes.Buffer
	writer, err := NewTarZstWriter(&sink)
	if err != nil {
		t.Fatal(err)
	}
	targets := []CrewTarget{{ID: crew, Slug: "crew-quota_snapshot"}}
	if _, _, err = captureServiceSnapshots(context.Background(), db, nil, writer, targets, time.Now()); err == nil {
		t.Fatal("missing transport silently lost quota data")
	}
	probe := &snapshotProbe{fail: true}
	fences, _, err := captureServiceSnapshots(context.Background(), db, probe, writer, targets, time.Now())
	if err == nil || len(fences) != 1 || probe.stop != 1 || probe.export != 1 {
		t.Fatalf("capture failure fence=%v err=%v stop=%d export=%d", fences, err, probe.stop, probe.export)
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM service_backup_fences WHERE crew_id=?`, crew).Scan(&n); err != nil || n != 1 {
		t.Fatal("failed capture resumed writer", err, n)
	}
	if _, err = db.Exec(`UPDATE service_runtime_intents SET version=8 WHERE id='snapshot-intent'`); err == nil {
		t.Fatal("failed capture allowed intent mutation")
	}
	_ = writer.Close()
}

func TestDeclaredServiceSnapshotsPreservesImmutableGenerationAndRejectsAliases(t *testing.T) {
	body := `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`
	snapshots, err := declaredServiceSnapshots(body, "crew", "crew-slug")
	if err != nil || len(snapshots) != 1 || snapshots[0].key().Generation != 3 {
		t.Fatalf("generation %v %v", snapshots, err)
	}
	duplicate := `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432},{"name":"data","mount":"/other","quota_bytes":33554432}]}]`
	if _, err = declaredServiceSnapshots(duplicate, "crew", "crew-slug"); err == nil {
		t.Fatal("duplicate physical volume accepted")
	}
}
