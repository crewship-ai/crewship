package backup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// The migrated schema, not a hand-written one: the new columns and
// restore_reports have to exist where production reads them.
func TestCatalog_ProofPinDrillAndIncomplete(t *testing.T) {
	ctx := context.Background()
	db := testutil.MigratedSQLDB(t)
	e := CatalogEntry{
		FilePath: "/b/one.tar.zst", Scope: "workspace", WorkspaceID: "ws", Slug: "ws",
		CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Size: 10, SHA256: "aa", Encrypted: true, FormatVersion: 3,
		Incomplete: []IncompleteItem{{Kind: IncompleteAttachmentMissing, Detail: "2 files", Count: 2, Workspace: "ws"}},
	}
	if err := UpsertCatalogEntry(ctx, db, e); err != nil {
		t.Fatal(err)
	}
	got, err := GetCatalogEntry(ctx, db, e.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindFull || got.ProofLevel != ProofChecksum || got.Pinned {
		t.Fatalf("defaults: kind=%q proof=%d pinned=%v", got.Kind, got.ProofLevel, got.Pinned)
	}
	if len(got.Incomplete) != 1 || got.Incomplete[0].Count != 2 {
		t.Fatalf("incomplete = %+v", got.Incomplete)
	}

	if err := PinCatalogEntry(ctx, db, e.FilePath); err != nil {
		t.Fatal(err)
	}
	if err := SetCatalogProof(ctx, db, e.FilePath, ProofContents, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A re-catalogue of the same bundle keeps the pin and the proof.
	if err := UpsertCatalogEntry(ctx, db, e); err != nil {
		t.Fatal(err)
	}
	got, _ = GetCatalogEntry(ctx, db, e.FilePath)
	if !got.Pinned || got.ProofLevel != ProofContents || got.ProofCheckedAt == nil {
		t.Fatalf("after re-upsert: pinned=%v proof=%d at=%v", got.Pinned, got.ProofLevel, got.ProofCheckedAt)
	}
	// A different payload under the same name starts over.
	e.SHA256 = "bb"
	if err := UpsertCatalogEntry(ctx, db, e); err != nil {
		t.Fatal(err)
	}
	got, _ = GetCatalogEntry(ctx, db, e.FilePath)
	if got.ProofLevel != ProofChecksum || !got.Pinned {
		t.Fatalf("new payload: proof=%d pinned=%v, want 1/true", got.ProofLevel, got.Pinned)
	}

	// A partial drill is level 3 and still says partial; a failed one proves nothing.
	if err := SetCatalogDrill(ctx, db, e.FilePath, RestoreResultPartial, json.RawMessage(`{"failed":["crew alpha"]}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ = GetCatalogEntry(ctx, db, e.FilePath)
	if got.ProofLevel != ProofRestore || got.DrillResult != RestoreResultPartial || got.DrillAt == nil || string(got.DrillReport) == "" {
		t.Fatalf("drill: %+v", got)
	}
	if err := SetCatalogDrill(ctx, db, "/b/other.tar.zst", RestoreResultOK, nil, time.Now()); !errors.Is(err, ErrCatalogEntryNotFound) {
		t.Fatalf("drill on unknown path: %v", err)
	}
	if err := SetCatalogProof(ctx, db, e.FilePath, 4, time.Now()); err == nil {
		t.Fatal("proof level 4 accepted")
	}

	if err := UnpinCatalogEntry(ctx, db, e.FilePath); err != nil {
		t.Fatal(err)
	}
	if err := PinCatalogEntry(ctx, db, "/nope"); !errors.Is(err, ErrCatalogEntryNotFound) {
		t.Fatalf("pin unknown: %v", err)
	}
}

func TestRestoreReports_RecordAndList(t *testing.T) {
	ctx := context.Background()
	db := testutil.MigratedSQLDB(t)
	for i, kind := range []string{RestoreKindDryRun, RestoreKindRestore} {
		if _, err := RecordRestoreReport(ctx, db, RestoreReport{
			Kind: kind, ActorUserID: "u1", BundlePath: "/b/x.tar.zst", Target: "ws",
			Result: []string{RestoreResultOK, RestoreResultPartial}[i], Report: json.RawMessage(`{"rows_inserted":3}`),
			CreatedAt: time.Date(2026, 9, 30, 10, i, 0, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RecordRestoreReport(ctx, db, RestoreReport{Kind: "undo", BundlePath: "x", Result: "ok"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	got, err := ListRestoreReports(ctx, db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != RestoreKindRestore || got[0].Result != RestoreResultPartial || string(got[1].Report) != `{"rows_inserted":3}` {
		t.Fatalf("list = %+v", got)
	}
}

func TestClassifyRestore(t *testing.T) {
	tests := []struct {
		name string
		res  *RestoreResult
		err  error
		want string
	}{
		{"error", &RestoreResult{}, errors.New("boom"), RestoreResultFailed},
		{"nil result", nil, nil, RestoreResultFailed},
		{"clean", &RestoreResult{RowsInserted: 5}, nil, RestoreResultOK},
		{"dropped crew files", &RestoreResult{DroppedCrewFilesystems: []string{"a"}}, nil, RestoreResultPartial},
		{"clamped", &RestoreResult{SecurityLevelClamped: 1}, nil, RestoreResultPartial},
		{"columns", &RestoreResult{ColumnsDropped: 1}, nil, RestoreResultPartial},
		{"attachments missing", &RestoreResult{AttachmentsMissing: 1}, nil, RestoreResultPartial},
		{"bundle gap", &RestoreResult{Incomplete: []IncompleteItem{{Kind: IncompleteContainerMissing, Count: 1}}}, nil, RestoreResultPartial},
		{"shortfall", &RestoreResult{RowsInsertedShortfalls: []TableRowCountMismatch{{Table: "x"}}}, nil, RestoreResultPartial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyRestore(tt.res, tt.err); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
