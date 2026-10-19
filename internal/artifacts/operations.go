package artifacts

import (
	"context"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/slides"
)

// Operations is a compile-time contract over a type's existing requests,
// results and transaction authority. It is not a generic transport dispatcher:
// callers retain the concrete payload, guard, lease and revision types.
// Availability never replaces authorization in the committing transaction.
type Operations[Key, CreateRequest, PatchRequest, RestoreRequest, SnapshotRequest, ExportRequest, CreateResult, State, Receipt, VersionList, SnapshotResult, ExportResult, Authority any] interface {
	Create(context.Context, Key, CreateRequest, Authority) (CreateResult, error)
	Read(context.Context, Key, Authority) (State, error)
	Apply(context.Context, Key, PatchRequest, Authority) (Receipt, error)
	Versions(context.Context, Key, Authority) (VersionList, error)
	Restore(context.Context, Key, RestoreRequest, Authority) (Receipt, error)
	Snapshot(context.Context, Key, SnapshotRequest, Authority) (SnapshotResult, error)
	Export(context.Context, Key, ExportRequest, Authority) (ExportResult, error)
}

// DocumentOperations retains the reviewed Document adapter's exact protocol.
// Create means Document initialization, not the separate agent Page/Document/
// consent creation transaction. Versions lists pinned checkpoints. Export is
// explicitly unsupported; these types do not introduce an export wire format.
type DocumentOperations = Operations[
	documents.Key,
	CreateDocument,
	documents.Commit,
	RestoreDocument,
	SnapshotDocument,
	UnsupportedExportRequest,
	documents.State,
	documents.State,
	documents.Receipt,
	[]map[string]any,
	documents.Checkpoint,
	UnsupportedExportResult,
	documents.Authorize,
]

var _ DocumentOperations = (*DocumentAdapter)(nil)

// SlideOperations preserves native creation receipts rather than manufacturing
// a State with a separate read. Versions is the Store's retained revision list.
type SlideOperations = Operations[
	slides.Key, slides.InitializeRequest, slides.PatchRequest, slides.RestoreRequest,
	UnsupportedSnapshotRequest, UnsupportedExportRequest,
	slides.Receipt, slides.State, slides.Receipt, []slides.Revision,
	UnsupportedSnapshotResult, UnsupportedExportResult, slides.Authorize,
]

var _ SlideOperations = (*SlidesAdapter)(nil)
