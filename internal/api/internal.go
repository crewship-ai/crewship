package api

import (
	"crypto/subtle"
	"database/sql"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

type mcpCredEntry struct {
	ID       string `json:"id"`
	EnvVar   string `json:"env_var"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
}

// InternalHandler provides endpoints called by the sidecar over the Unix socket using X-Internal-Token auth.
type InternalHandler struct {
	db            *sql.DB
	logger        *slog.Logger
	internalToken string
	keeperEnabled atomic.Bool
	hub           *ws.Hub
	journal       journal.Emitter
}

// NewInternalHandler creates an InternalHandler with the given database, internal token, and logger.
// Callers that want journal emits wire them after construction with SetJournal;
// the default is a no-op so tests stay simple.
func NewInternalHandler(db *sql.DB, internalToken string, logger *slog.Logger) *InternalHandler {
	return &InternalHandler{db: db, internalToken: internalToken, logger: logger, journal: noopEmitter{}}
}

// SetHub attaches a WebSocket hub for broadcasting events from internal endpoints.
func (h *InternalHandler) SetHub(hub *ws.Hub) {
	h.hub = hub
}

// SetJournal wires a journal emitter. nil maps to the no-op so callers
// don't have to branch on whether the server wired one.
func (h *InternalHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// SetKeeperEnabled toggles whether Keeper is advertised as enabled in the agent config.
func (h *InternalHandler) SetKeeperEnabled(enabled bool) {
	h.keeperEnabled.Store(enabled)
}

func (h *InternalHandler) requireInternal(next http.Handler) http.Handler {
	if h.internalToken == "" {
		h.logger.Error("internal token is empty -- all internal API calls will be rejected")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Internal-Token")
		// Always run constant-time comparison to avoid timing sidechannels.
		// Pad empty strings to a fixed sentinel so the comparison still runs
		// in constant time even when token or internalToken is empty.
		expected := h.internalToken
		if expected == "" {
			expected = "\x00empty-sentinel\x00"
		}
		actual := token
		if actual == "" {
			actual = "\x00different-sentinel\x00"
		}
		if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
