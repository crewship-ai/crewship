package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/chatbridge"
)

// Steerer delivers a mid-turn steering message into a chat. The chatbridge
// *Bridge satisfies it. Kept as an interface so the handler is decoupled
// from the bridge's container/orchestrator dependencies and testable with
// a fake.
type Steerer interface {
	Steer(ctx context.Context, chatID, content string) (chatbridge.SteerResult, error)
}

// SteerHandler serves POST /api/v1/chats/{chatId}/steer — mid-turn
// steering. In this slice a steering message is QUEUED for the next turn
// (the chatbridge guards against racing a second run into a live turn);
// live injection into a running turn is a deferred follow-up.
//
// Tenancy gate mirrors MessageReactionsHandler: the route mounts under
// /api/v1/chats/{chatId}/... with no workspace_id, so the handler derives
// the chat's workspace and verifies membership itself (cross-tenant → 404).
type SteerHandler struct {
	db      *sql.DB
	steerer Steerer
	logger  *slog.Logger
}

// NewSteerHandler builds the handler. steerer may be nil (e.g. a server
// booted without a chat bridge); Steer then returns 503 rather than panicking.
func NewSteerHandler(db *sql.DB, steerer Steerer, logger *slog.Logger) *SteerHandler {
	return &SteerHandler{db: db, steerer: steerer, logger: logger}
}

// SetSteerer rewires the steerer after construction. The bridge that
// implements Steerer is built in the server boot sequence, after the
// router's routes are registered, so the route is mounted with a nil
// steerer and this flips it live.
func (h *SteerHandler) SetSteerer(s Steerer) {
	h.steerer = s
}

// ensureChatVisible confirms the authenticated user is a member of the
// chat's workspace. Identical contract to the reactions handler's gate.
func (h *SteerHandler) ensureChatVisible(r *http.Request, chatID string) bool {
	if chatID == "" {
		return false
	}
	user := UserFromContext(r.Context())
	if user == nil {
		return false
	}
	var owner string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT workspace_id FROM chats WHERE id = ?", chatID).Scan(&owner); err != nil {
		return false
	}
	var role string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		owner, user.ID).Scan(&role)
	return err == nil
}

type steerRequest struct {
	Message string `json:"message"`
}

func (h *SteerHandler) Steer(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")

	// 401 BEFORE 404 so a logged-out caller cannot probe chat existence
	// (same ordering as the reactions handler's Add/Remove).
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !h.ensureChatVisible(r, chatID) {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}

	var body steerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		replyError(w, http.StatusBadRequest, "message required")
		return
	}

	if h.steerer == nil {
		replyError(w, http.StatusServiceUnavailable, "steering unavailable: no chat bridge")
		return
	}

	res, err := h.steerer.Steer(r.Context(), chatID, body.Message)
	if err != nil {
		// The only non-infrastructure failure path in the bridge's Steer
		// is the content scan / empty-content guard — surface it as 422
		// (the request was well-formed but the payload was rejected).
		h.logger.Info("steer rejected", "chat_id", chatID, "error", err)
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, res)
}
