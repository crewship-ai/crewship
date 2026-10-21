package artifacts

import (
	"context"

	"github.com/crewship-ai/crewship/internal/documents"
)

// Operations is a compile-time contract over a type's existing requests,
// results and transaction authority. It is not a generic transport dispatcher:
// callers retain the concrete payload, guard, lease and revision types.
// Availability never replaces authorization in the committing transaction.
type Operations[Key, CreateRequest, PatchRequest, RestoreRequest, SnapshotRequest, ExportRequest, State, Receipt, VersionList, SnapshotResult, ExportResult, Authority any] interface {
	Create(context.Context, Key, CreateRequest, Authority) (State, error)
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
	documents.Receipt,
	[]map[string]any,
	documents.Checkpoint,
	UnsupportedExportResult,
	documents.Authorize,
]

var _ DocumentOperations = (*DocumentAdapter)(nil)
