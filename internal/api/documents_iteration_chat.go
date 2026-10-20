package api

import (
	"context"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

// PersistDocumentIteration is called only by the trusted in-process bridge after
// the original human turn is synced. A seal alone never authorizes execution.
func (h *PageHandler) PersistDocumentIteration(ctx context.Context, receipt *chataudience.Receipt, message string, intent documents.IterationIntent) error {
	return h.persistDocumentIterationIntent((&http.Request{}).WithContext(ctx), receipt, message, intent)
}
