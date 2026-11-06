package mooring

import (
	"database/sql"
	"testing"
)

// What a native save of the bytes already at the head costs (autosave, then
// Ctrl+S): the blob is shared, the version row and its manifest are not. History
// gets one more entry and the quota is charged for that row only.
func TestNativeIdenticalSaveAddsAVersionChargedForItsRowOnly(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, *acting, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	second := []byte(`{"v":2,"body":"the same bytes twice"}`)
	hash := nativeReceiptHash(second)
	if _, err = s.SaveSnapshot(t.Context(), l, 1, "r1", hash, second); err != nil {
		t.Fatal(err)
	}
	db := s.db
	measure := func() (versions, blobs, used int64) {
		t.Helper()
		for _, q := range []struct {
			query string
			into  *int64
		}{
			{`SELECT count(*) FROM artifact_versions WHERE workspace_id=? AND page_id=?`, &versions},
			{`SELECT count(*) FROM artifact_version_blobs WHERE workspace_id=? AND page_id=?`, &blobs},
		} {
			if err := db.QueryRowContext(t.Context(), q.query, scope.WorkspaceID, scope.ArtifactID).Scan(q.into); err != nil {
				t.Fatal(err)
			}
		}
		var total sql.NullInt64
		if err := db.QueryRowContext(t.Context(), `SELECT used_bytes FROM artifact_storage_workspaces WHERE workspace_id=?`, scope.WorkspaceID).Scan(&total); err != nil {
			t.Fatal(err)
		}
		return versions, blobs, total.Int64
	}
	versions, blobs, used := measure()
	repeat, err := s.SaveSnapshot(t.Context(), l, 2, "r2", hash, second)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Version != "3" {
		t.Fatalf("identical save: version %s, want a new version 3", repeat.Version)
	}
	nextVersions, nextBlobs, nextUsed := measure()
	if nextVersions != versions+1 || nextBlobs != blobs {
		t.Fatalf("versions %d -> %d, blobs %d -> %d", versions, nextVersions, blobs, nextBlobs)
	}
	charge := nextUsed - used
	if charge <= 0 || charge >= 2048 {
		t.Fatalf("identical save charged %d bytes, want a version row and no blob", charge)
	}
	t.Logf("identical save of %d bytes charged %d bytes", len(second), charge)
}
