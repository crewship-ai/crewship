package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
)

// ChatCardResponse extends the E1 reference envelope without changing E1's
// issue API. New source adapters reuse its immutable provenance and anchoring.
type ChatCardResponse struct {
	EntityRefResponse
	Proposal *chatentities.ProposalCard `json:"proposal,omitempty"`
	Decision *chatentities.DecisionCard `json:"decision,omitempty"`
}

// Lists retain E1 replacement-snapshot semantics and opaque scoped cursors.
// Functional list readers and pagination receipts arrive with source storage.
type ChatCardPage struct {
	ContractVersion  int                `json:"contract_version"`
	Reset            bool               `json:"reset"`
	NextCursor       string             `json:"next_cursor"`
	SnapshotID       string             `json:"snapshot_id"`
	SnapshotComplete bool               `json:"snapshot_complete"`
	HasMore          bool               `json:"has_more"`
	Refs             []ChatCardResponse `json:"refs"`
}
type ChatCardVersionConflict struct {
	Error   string           `json:"error"`
	Current ChatCardResponse `json:"current"`
}

// ChatCardProjection ties a read model to the host-owned source chat. Decision
// adapters read the existing source record; this object never stores answers.
type ChatCardProjection struct {
	ChatID string
	Ref    ChatCardResponse
}

// ChatCardReader must filter by the supplied live viewer/chat/workspace and
// return sql.ErrNoRows for every inaccessible/missing source, including hidden
// items within a proposal. It must not return counts or partial unauthorized data.
type ChatCardReader func(context.Context, chataudience.Receipt, string) (ChatCardProjection, error)

// ChatCardProjectionHandler is the shared read transport for source adapters.
// A1 exposes no HTTP route: A2/C register functional readers together with
// their CLI/OpenAPI/docs. Tests exercise this actual handler with source fixtures.
func (r *Router) ChatCardProjectionHandler(reader ChatCardReader) http.HandlerFunc {
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
		writeJSON(w, http.StatusOK, card.Ref)
	}
}
