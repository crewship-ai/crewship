package server

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
)

// #3964: a crew runtime the provider replaced because of an outdated sidecar
// lands in the journal as sidecar.recreated, scoped to the crew's workspace,
// with both hashes — the operator-visible proof that sidecar.stale resolved
// itself.
func TestSidecarRecreatedJournal(t *testing.T) {
	db := sweepDB(t)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, slug, name) VALUES ('crew-1', 'ws-1', 'eng', 'Engineering')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		ev       provider.SidecarRecreatedEvent
		wantRows int
	}{
		{
			name: "stamped container",
			ev: provider.SidecarRecreatedEvent{CrewID: "crew-1", CrewSlug: "eng", PreviousContainerID: "old0123456789abcdef",
				ContainerID: "new0123456789abcdef", PreviousSidecarHash: "0b6348e06bf8", SidecarHash: "1c2d3e4f5a6b"},
			wantRows: 1,
		},
		{
			name: "container from before the stamp",
			ev: provider.SidecarRecreatedEvent{CrewID: "crew-1", CrewSlug: "eng", PreviousContainerID: "old0123456789abcdef",
				ContainerID: "new0123456789abcdef", SidecarHash: "1c2d3e4f5a6b"},
			wantRows: 1,
		},
		{
			name:     "unknown crew is not journaled unscoped",
			ev:       provider.SidecarRecreatedEvent{CrewID: "missing", SidecarHash: "1c2d3e4f5a6b"},
			wantRows: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingEmitter{}
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // the triggering run is already gone; the record must still land
			sidecarRecreatedJournal(db, rec, quietLogger())(ctx, tc.ev)
			if len(rec.entries) != tc.wantRows {
				t.Fatalf("entries = %d, want %d", len(rec.entries), tc.wantRows)
			}
			if tc.wantRows == 0 {
				return
			}
			e := rec.entries[0]
			if e.Type != journal.EntrySidecarRecreated || e.Severity != journal.SeverityInfo {
				t.Fatalf("type/severity = %s/%s", e.Type, e.Severity)
			}
			if e.WorkspaceID != "ws-1" || e.CrewID != "crew-1" {
				t.Fatalf("scope = %s/%s", e.WorkspaceID, e.CrewID)
			}
			if e.Payload["previous_sidecar_hash"] != tc.ev.PreviousSidecarHash || e.Payload["sidecar_hash"] != tc.ev.SidecarHash {
				t.Fatalf("payload = %+v", e.Payload)
			}
			if e.Payload["previous_container_id"] != "old012345678" || e.Payload["container_id"] != "new012345678" {
				t.Fatalf("container ids = %+v", e.Payload)
			}
		})
	}
}
