package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/slides"
)

type SlidesSelectedSnapshotResponse struct {
	Request         slides.SelectedRequestSummary `json:"request"`
	OriginalElement slides.Element                `json:"original_element"`
}
type SlidesSelectedReplacementResponse struct {
	MutationID   string `json:"mutation_id"`
	Revision     int64  `json:"revision"`
	OrderVersion int64  `json:"order_version"`
	ContentHash  string `json:"content_hash"`
	Duplicate    bool   `json:"duplicate"`
}

func (h *slideHTTP) selectedRunAuthority(key slides.Key, id string) slides.Authorize {
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		if h.pages.preStartupSelectedRequest(id) {
			return "", errSlideAccess
		}
		a, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority)
		if !ok || a.WorkspaceID != key.WorkspaceID {
			return "", errSlideAccess
		}
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		ctx = context.WithValue(ctx, slidesSelectedTransportKey{}, slidesSelectedTransport{RequestID: id, PageID: key.PageID})
		record, err := slides.ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil || record.Summary.Restored || record.Summary.OriginalRunID != a.RunID || record.Admission.HumanUserID != a.HumanUserID || record.Admission.AgentID != a.AgentID || record.Admission.ChatID != a.ChatID {
			return "", errSlideAccess
		}
		running := record.Summary.Status == "running" && record.RunID == a.RunID
		ownedReplay := record.Summary.Status == "succeeded" && record.Summary.ResultRevision != nil && record.RunID == ""
		if !running && !ownedReplay {
			return "", errSlideAccess
		}
		if _, err = h.selectedAdmissionLive(ctx, tx, key, record.Admission); err != nil || h.pages.checkArtifactRun(ctx, key.PageID, a) != nil {
			return "", errSlideAccess
		}
		var origin string
		var otherOrigins bool
		if tx.QueryRowContext(ctx, `SELECT COALESCE(slides_request_id,''),request_id IS NOT NULL OR document_request_id IS NOT NULL FROM page_artifact_run_delegations WHERE page_id=? AND run_id=?`, key.PageID, a.RunID).Scan(&origin, &otherOrigins) != nil || origin != id || otherOrigins {
			return "", errSlideAccess
		}
		return "agent:" + a.AgentID + ":" + a.RunID, nil
	}
}

// SelectedSlidesAgent is the sole private transport that sets the trusted
// selected marker. Crew IPC is only the outer authentication boundary; every
// read/write/replay checks the original run bearer and writer-fenced admission.
func (pages *PageHandler) SelectedSlidesAgent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	checked, ok := pages.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	r = checked
	h := &slideHTTP{pages: pages, store: pages.newSlidesStore()}
	key, ok := h.key(w, r)
	if !ok {
		return
	}
	id := r.PathValue("requestId")
	authorize := h.selectedRunAuthority(key, id)
	if r.Method == http.MethodGet {
		result, err := h.store.SelectedSnapshot(r.Context(), key, id, authorize)
		if err != nil {
			slideHTTPError(w, err)
			return
		}
		writeJSON(w, 200, SlidesSelectedSnapshotResponse{Request: result.Request, OriginalElement: result.OriginalElement})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, slides.MaxSelectedReplacementBytes))
	if err != nil {
		replyError(w, 400, "bounded selected replacement required")
		return
	}
	input, err := slides.DecodeSelectedReplacement(raw)
	if err != nil {
		replyError(w, 400, err.Error())
		return
	}
	if input.PageID != key.PageID || input.RequestID != id {
		slideHTTPError(w, slides.ErrConflict)
		return
	}
	a := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	result, err := h.store.ReplaceSelectedText(r.Context(), key, id, a.RunID, input.Replacement, authorize)
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	if !result.Duplicate {
		h.pages.selectedRequestChanged(key.WorkspaceID)
	}
	writeJSON(w, 200, SlidesSelectedReplacementResponse{MutationID: result.MutationID, Revision: result.Revision, OrderVersion: result.OrderVersion, ContentHash: result.ContentHash, Duplicate: result.Duplicate})
}
