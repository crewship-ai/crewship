package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Agent access modes for the other artifact kinds (#5317 step 5.3).
//
// A Design reuses the artifact project's run scope unchanged: the same reach
// kinds (referenced, created, consented, standing, folder), the same
// intersection with the human as an ordinary member, the same per-run read
// limit and the same audit row. Its exact-run consent is the artifact
// project's own (artifact_project_run_consents), so artifactRunReach already
// sees it. The kind's handler checks that its family is enabled, so the
// project family gate of checkScopedArtifactRun does not apply to it.

// scopedRunKindKey marks a flag-on run check made by a kind other than an
// artifact project. Only server code sets it, inside the access transaction.
type scopedRunKindKey struct{}

type scopedRunKind struct {
	// FamilyChecked: the kind's handler has already checked that its own
	// family is enabled for the workspace.
	FamilyChecked bool
}

func withScopedRunKind(ctx context.Context, k scopedRunKind) context.Context {
	return context.WithValue(ctx, scopedRunKindKey{}, k)
}

func scopedRunFamilyChecked(ctx context.Context) bool {
	k, _ := ctx.Value(scopedRunKindKey{}).(scopedRunKind)
	return k.FamilyChecked
}

// designScopedRunAuthority is designAuthority for a flag-on chat run. The
// scope check refuses an unreached Design like a missing one; a write also
// needs the human's own edit right as an ordinary member (the scope check
// already requires that the human owns it and that the reach is attached,
// created or consented).
func (h *PageHandler) designScopedRunAuthority(ctx context.Context, tx *sql.Tx, stamp *designAuthorityStamp, a *artifactRunAuthority, k artifacts.VersionKey, userID string, write bool) (string, error) {
	if a.WorkspaceID != k.WorkspaceID || a.HumanUserID != userID {
		return "", errDesignAccess
	}
	rec, err := h.loadPageForRead(ctx, k.WorkspaceID, "@"+k.PageID)
	if err != nil {
		return "", errArtifactRefNotFound
	}
	actor, err := h.authorizeScopedArtifactRun(withScopedRunKind(ctx, scopedRunKind{FamilyChecked: true}), tx, a, k.WorkspaceID, rec, write, false)
	if err != nil {
		if errors.Is(err, errArtifactProjectAccess) {
			return "", errDesignAccess
		}
		return "", err
	}
	if write {
		edit, err := h.designDelegableEdit(ctx, k.WorkspaceID, a.HumanUserID, rec)
		if err != nil {
			return "", err
		}
		if !edit {
			return "", errDesignAccess
		}
	}
	if err = designCaptureGenerations(ctx, tx, k); err != nil {
		return "", err
	}
	revision, err := designAuthorityRevision(ctx, tx, k.WorkspaceID)
	if err != nil {
		return "", err
	}
	return stamp.admit(actor, revision)
}

// designDelegableEdit: may the human edit this Design as an ordinary member?
// Edit held only through the ADMIN or OWNER role does not count (amendment 2).
func (h *PageHandler) designDelegableEdit(ctx context.Context, workspace, humanID string, rec *pageRecord) (bool, error) {
	viewer, err := h.delegableViewer(ctx, workspace, humanID)
	if err != nil {
		return false, err
	}
	if viewer.Role == "" || canRole(viewer.Role, "manage") {
		return false, nil
	}
	return h.mayEditSpec(withDelegationUser(ctx, humanID), workspace, humanID, viewer.Role, rec), nil
}

// designScopedRunLive decides, for a flag-on run whose write probe failed,
// whether it still reads the latest version (amendment 7): an Editor's run
// reads the latest, a Viewer's or Commenter's the shared version. It is asked
// only after the read authorization admitted the run.
func (h *PageHandler) designScopedRunLive(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, k artifacts.VersionKey) (bool, error) {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	rec, err := h.loadPageForRead(ctx, k.WorkspaceID, "@"+k.PageID)
	if err != nil {
		return false, err
	}
	return h.designDelegableEdit(ctx, k.WorkspaceID, a.HumanUserID, rec)
}

// designScopedRun returns the run when this Design request is a flag-on chat
// run, read through the caller's transaction.
func (h *PageHandler) designScopedRun(ctx context.Context, tx *sql.Tx) *artifactRunAuthority {
	return h.artifactRunModesRun(context.WithValue(ctx, pageAuthReaderKey{}, tx))
}

// designAuditProbe marks a Design request whose authorization only checks
// access (a pre-read before the write's own transaction, or a capability
// probe): it records no agent-run audit row.
func designAuditProbe(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), artifactAuditProbeKey{}, true))
}

func designAuditProbeContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, artifactAuditProbeKey{}, true)
}

// recordConsentedRunAccess audits a flag-on chat run's access to an artifact
// that it reaches only through its kind's own exact-run consent: a GridCraft
// (Sheet) or Mooring mandate, or a Document drafting or iteration consent.
// Those kinds keep their consent path with the flag on, because their writes
// are bound to a mandate's targets, base revision and lease; the access is
// still intersected with the human as an ordinary member and audited in the
// access's own transaction, failing closed. With the flag off it does nothing.
func (h *PageHandler) recordConsentedRunAccess(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, pageID, operation string) error {
	return h.recordKindRunAccess(ctx, tx, a, pageID, runReachConsented, operation)
}

// recordKindRunAccess is recordConsentedRunAccess with an explicit reach: a
// Document an agent creates under its creation consent is recorded as created.
func (h *PageHandler) recordKindRunAccess(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, pageID, reach, operation string) error {
	if tx != nil {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	}
	if a == nil || !h.artifactRunModesOn(ctx, a.WorkspaceID) {
		return nil
	}
	if tx == nil {
		return errors.New("agent run artifact audit needs the access transaction")
	}
	viewer, err := h.delegableViewer(ctx, a.WorkspaceID, a.HumanUserID)
	if err != nil {
		return err
	}
	if ok, err := h.delegableRead(ctx, a.WorkspaceID, pageID, viewer); err != nil {
		return err
	} else if !ok {
		return errArtifactRefNotFound
	}
	return recordAgentRunArtifactAccess(ctx, tx, a, pageID, reach, operation)
}
