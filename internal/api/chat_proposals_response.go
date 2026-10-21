package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

// proposalResponse is the functional source envelope. Unlike the generic A1
// union, this route always emits a proposal and never emits a decision.
type proposalResponse struct {
	EntityRefResponse
	Proposal *chatentities.ProposalCard `json:"proposal"`
}

type proposalPage struct {
	ContractVersion  int                `json:"contract_version"`
	Reset            bool               `json:"reset"`
	NextCursor       string             `json:"next_cursor"`
	SnapshotID       string             `json:"snapshot_id"`
	SnapshotComplete bool               `json:"snapshot_complete"`
	HasMore          bool               `json:"has_more"`
	Refs             []proposalResponse `json:"refs"`
}

func proposalWire(card ChatCardResponse) proposalResponse {
	return proposalResponse{EntityRefResponse: card.EntityRefResponse, Proposal: card.Proposal}
}

func writeProposalJSON(w http.ResponseWriter, status int, value any) {
	switch v := value.(type) {
	case ChatCardResponse:
		value = proposalWire(v)
	case ChatCardPage:
		refs := make([]proposalResponse, 0, len(v.Refs))
		for _, ref := range v.Refs {
			refs = append(refs, proposalWire(ref))
		}
		value = proposalPage{v.ContractVersion, v.Reset, v.NextCursor, v.SnapshotID, v.SnapshotComplete, v.HasMore, refs}
	}
	writeJSON(w, status, value)
}

// Keep source projection checks identical to the frozen shared transport while
// serializing the mandatory proposal envelope used by these functional routes.
func (r *Router) proposalProjectionHandler(reader ChatCardReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		audience, err := r.entityRefAudience(req, req.PathValue("chatId"))
		if err != nil {
			if errors.Is(err, errEntityAudienceDenied) {
				replyError(w, http.StatusNotFound, "Card not found")
			} else {
				replyError(w, http.StatusInternalServerError, "Could not verify current access")
			}
			return
		}
		if reader == nil {
			replyError(w, http.StatusServiceUnavailable, "Card source unavailable")
			return
		}
		id := req.PathValue("sourceId")
		if id == "" || len(id) > 200 {
			replyError(w, http.StatusNotFound, "Card not found")
			return
		}
		card, err := reader(req.Context(), *audience, id)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (card.ChatID != audience.ChatID || card.Ref.Entity.WorkspaceID != audience.WorkspaceID || card.Ref.Entity.ID != id) {
			replyError(w, http.StatusNotFound, "Card not found")
			return
		}
		if err != nil || validateE2Card(card.Ref) != nil {
			replyError(w, http.StatusInternalServerError, "Could not read card")
			return
		}
		// Match E1: source lookup must not race a revoked/changed chat audience.
		// Do not use active-run mutation admission here: terminal cards stay readable.
		current, err := r.entityRefAudience(req, card.ChatID)
		if errors.Is(err, errEntityAudienceDenied) {
			replyError(w, http.StatusNotFound, "Card not found")
			return
		}
		if err != nil || *current != *audience {
			replyError(w, http.StatusServiceUnavailable, "Could not verify current access")
			return
		}
		writeProposalJSON(w, http.StatusOK, card.Ref)
	}
}
