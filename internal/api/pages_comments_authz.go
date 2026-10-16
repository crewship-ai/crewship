package api

import "context"

// mayComment applies only to authenticated human callers. A comment grant
// grants Page reach and discussion, never editing, producing or dispatching.
// Issuer liveness and crew/workspace subjects use the existing grant resolver.
func (h *PageHandler) mayComment(ctx context.Context, wsID, userID, role string, rec *pageRecord) bool {
	if userID == "" {
		return false
	}
	if h.mayEditSpec(ctx, wsID, userID, role, rec) {
		return true
	}
	viewer, err := h.loadViewer(ctx, wsID, userID)
	if err != nil {
		return false
	}
	grants, err := h.grantsFor(ctx, wsID, rec, viewer)
	if err != nil {
		return false
	}
	for _, grant := range grants {
		if grant.Level == pageGrantComment {
			return true
		}
	}
	return false
}
