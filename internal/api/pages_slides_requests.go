package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/slides"
)

type SlidesSelectedRequestsResponse struct {
	Items      []slides.SelectedRequestSummary `json:"items"`
	NextCursor string                          `json:"next_cursor"`
	Watermark  int64                           `json:"watermark"`
	HasMore    bool                            `json:"has_more"`
}
type SlidesSelectedRequestResponse struct {
	Request slides.SelectedRequestSummary `json:"request"`
	History []slides.SelectedRequestEvent `json:"history"`
}
type SlidesSelectedAdmissionResponse struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
	Version   int64  `json:"version"`
	Status    string `json:"status"`
	Duplicate bool   `json:"duplicate"`
}
type SlidesSelectedCancelResponse struct {
	MutationID string `json:"mutation_id"`
	RequestID  string `json:"request_id"`
	Version    int64  `json:"version"`
	Status     string `json:"status"`
	Duplicate  bool   `json:"duplicate"`
}

func (h *slideHTTP) ReadSelectedRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := h.key(w, r)
	if !ok {
		return
	}
	authorize := h.selectedHumanAuthority(r, key, false, nil)
	if id := r.PathValue("requestId"); id != "" {
		result, err := h.store.ReadSelectedRequest(r.Context(), key, id, authorize)
		if err != nil {
			slideHTTPError(w, err)
			return
		}
		writeJSON(w, 200, SlidesSelectedRequestResponse{Request: result.Request, History: result.History})
		return
	}
	page := slides.SelectedPageRequest{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			replyError(w, 400, "limit must be between 1 and 100")
			return
		}
		page.Limit = limit
	}
	result, err := h.store.ListSelectedRequests(r.Context(), key, page, authorize)
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	writeJSON(w, 200, SlidesSelectedRequestsResponse{Items: result.Items, NextCursor: result.NextCursor, Watermark: result.Watermark, HasMore: result.HasMore})
}

func (h *slideHTTP) MutateSelectedRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := h.key(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, slides.MaxSelectedRequestBytes))
	if err != nil {
		replyError(w, 400, "bounded selected request JSON required")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/cancel") {
		input, err := slides.DecodeSelectedCancel(raw)
		if err != nil {
			replyError(w, 400, err.Error())
			return
		}
		result, err := h.store.CancelSelectedRequest(r.Context(), key, r.PathValue("requestId"), input, h.selectedHumanAuthority(r, key, true, nil))
		if err != nil {
			slideHTTPError(w, err)
			return
		}
		writeJSON(w, 200, SlidesSelectedCancelResponse{MutationID: result.MutationID, RequestID: result.RequestID, Version: result.Version, Status: result.Status, Duplicate: result.Duplicate})
		return
	}
	if h.pages.artifactDispatch == nil && h.pages.hub == nil {
		replyError(w, 503, "Selected agent runtime unavailable")
		return
	}
	input, err := slides.DecodeSelectedRequest(raw)
	if err != nil {
		replyError(w, 400, err.Error())
		return
	}
	if input.PageID != key.PageID {
		slideHTTPError(w, slides.ErrConflict)
		return
	}
	admission, err := h.captureSelectedAdmission(r, input.ChatID)
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	result, err := h.store.AdmitSelectedRequest(r.Context(), key, generateCUID(), input, admission, h.selectedHumanAuthority(r, key, true, &admission))
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	writeJSON(w, 202, SlidesSelectedAdmissionResponse{ID: result.ID, RequestID: result.RequestID, Version: result.Version, Status: result.Status, Duplicate: result.Duplicate})
}
