package api

import (
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Creation admission must commit the start, immutable human principal and
// consent binding together. An execution ACK never precedes this transaction.
func (h *InternalHandler) admitDocumentCreationStart(r *http.Request, entry journal.Entry, receipt *chataudience.Receipt, message string, intent documents.CreationIntent) (int, error) {
	emitter, ok := h.journal.(journal.TransactionEmitter)
	if !ok {
		return http.StatusServiceUnavailable, errors.New("transactional run journal unavailable")
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, receipt.WorkspaceID, receipt.UserID); err != nil {
		return http.StatusInternalServerError, err
	}
	matched, err := matchRunStartQuery(r.Context(), tx, entry)
	if err != nil {
		if errors.Is(err, errRunStartIdentityConflict) || errors.Is(err, errRunAlreadyTerminal) {
			return http.StatusConflict, err
		}
		return http.StatusInternalServerError, err
	}
	var notify func()
	if !matched {
		notify, err = emitter.AppendTx(r.Context(), tx, entry)
		if err != nil {
			if errors.Is(err, journal.ErrRecoveredStopConflict) {
				return http.StatusConflict, err
			}
			return http.StatusInternalServerError, err
		}
	}
	if err := captureArtifactChatPrincipalQuery(r, tx, entry.TraceID, entry.WorkspaceID, entry.AgentID, entry.CrewID, receipt.ChatID, message, receipt); err != nil {
		return http.StatusForbidden, err
	}
	if err := bindDocumentCreationRunTx(r, h.db, tx, entry.TraceID, message, receipt, intent); err != nil {
		return http.StatusForbidden, err
	}
	if err := tx.Commit(); err != nil {
		return http.StatusInternalServerError, err
	}
	if notify != nil {
		notify()
	}
	return 0, nil
}
