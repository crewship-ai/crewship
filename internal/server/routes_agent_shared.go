package server

import (
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/conversation"
)

const (
	sharedTranscriptMaxSourceBytes int64 = 16 << 20
	sharedTranscriptMaxMessages          = 1000
)

// handleSharedChatMessages is a bounded IPC read for the public share route.
// It does not change the ordinary authenticated chat history endpoint.
func (s *Server) handleSharedChatMessages(w http.ResponseWriter, r *http.Request) {
	if s.convStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversation store unavailable"})
		return
	}
	messages, err := s.convStore.ReadShared(r.Context(), r.PathValue("id"),
		sharedTranscriptMaxSourceBytes, sharedTranscriptMaxMessages)
	if errors.Is(err, conversation.ErrSharedLimit) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "shared transcript exceeds limit"})
		return
	}
	if err != nil {
		s.logger.Error("read shared chat messages", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "shared transcript unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": messages})
}
