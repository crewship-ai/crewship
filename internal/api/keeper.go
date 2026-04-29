package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/provider"
)

// SecretGetter retrieves a plaintext credential value by ID.
// Implemented by the keeper secrets store; can be mocked in tests.
type SecretGetter interface {
	Get(credentialID string) (plainValue string, found bool)
}

// ConversationReader reads recent messages for a chat session.

type ConversationReader interface {
	Read(ctx context.Context, sessionID string, offset, limit int) ([]ConversationMessage, error)
}

// ConversationMessage is a minimal view of a conversation message for Keeper context.

type ConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

const (
	// maxExecuteCommandLength is the max allowed length for the command field.
	maxExecuteCommandLength = 4096
	// maxExecuteOutputBytes caps the output read from a keeper execute command.
	maxExecuteOutputBytes = 512 * 1024 // 512 KB
	// executeTimeout limits the total time for a keeper execute command.
	executeTimeout = 60 * time.Second
)

// KeeperHandler handles credential access requests forwarded by the sidecar.
// All requests require X-Internal-Token authentication.
// KeeperBroadcaster broadcasts keeper events to WebSocket subscribers.

type KeeperBroadcaster interface {
	BroadcastKeeperEvent(workspaceID string, event map[string]any)
}

// KeeperHandler handles credential access requests forwarded by the sidecar.
// It evaluates gatekeeper policies and returns allow/deny decisions.

type KeeperHandler struct {
	db            *sql.DB
	logger        *slog.Logger
	internalToken string
	gatekeeper    gatekeeper.Evaluator
	secrets       SecretGetter
	container     provider.ContainerProvider
	broadcaster   KeeperBroadcaster
	conversations ConversationReader
}

// NewKeeperHandler creates a KeeperHandler with the given gatekeeper evaluator and internal token.

func NewKeeperHandler(db *sql.DB, internalToken string, gk gatekeeper.Evaluator, logger *slog.Logger) *KeeperHandler {
	return &KeeperHandler{
		db:            db,
		logger:        logger,
		internalToken: internalToken,
		gatekeeper:    gk,
	}
}

// WithSecrets attaches a SecretGetter used by HandleExecute to retrieve plaintext values.

func (h *KeeperHandler) WithSecrets(sg SecretGetter) *KeeperHandler {
	h.secrets = sg
	return h
}

// WithContainer attaches a ContainerProvider used by HandleExecute to exec commands.

func (h *KeeperHandler) WithContainer(cp provider.ContainerProvider) *KeeperHandler {
	h.container = cp
	return h
}

// WithBroadcaster attaches a broadcaster for real-time keeper event notifications.

func (h *KeeperHandler) WithBroadcaster(b KeeperBroadcaster) *KeeperHandler {
	h.broadcaster = b
	return h
}

// WithConversations attaches a conversation reader for Keeper context enrichment.

func (h *KeeperHandler) WithConversations(cr ConversationReader) *KeeperHandler {
	h.conversations = cr
	return h
}

func (h *KeeperHandler) GetRequest(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("requestId")
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "requestId required"})
		return
	}

	type requestRow struct {
		ID                string  `json:"id"`
		RequestingAgentID string  `json:"requesting_agent_id"`
		RequestingCrewID  string  `json:"requesting_crew_id"`
		CredentialID      string  `json:"credential_id"`
		Intent            string  `json:"intent"`
		Decision          *string `json:"decision"`
		Reason            *string `json:"reason"`
		RiskScore         *int    `json:"risk_score"`
		CreatedAt         string  `json:"created_at"`
		DecidedAt         *string `json:"decided_at"`
	}

	var row requestRow
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, requesting_agent_id, requesting_crew_id, credential_id,
		       intent, decision, reason, risk_score, created_at, decided_at
		FROM keeper_requests WHERE id = ?`, requestID).Scan(
		&row.ID, &row.RequestingAgentID, &row.RequestingCrewID, &row.CredentialID,
		&row.Intent, &row.Decision, &row.Reason, &row.RiskScore, &row.CreatedAt, &row.DecidedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
			return
		}
		h.logger.Error("keeper: get request", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, row)
}

// reverseString and nullIfEmpty live in keeper_helpers.go.

const keeperConvHistoryLimit = 10

// loadConversationHistory fetches the last N messages from the agent's most recent
// active chat. Returns a formatted string for the Keeper prompt, or "" if unavailable.

func (h *KeeperHandler) loadConversationHistory(ctx context.Context, agentID string) string {
	if h.conversations == nil {
		return ""
	}

	// Find the agent's most recent active chat
	var chatID string
	err := h.db.QueryRowContext(ctx, `
		SELECT id FROM chats
		WHERE agent_id = ? AND status = 'ACTIVE'
		ORDER BY created_at DESC LIMIT 1`, agentID).Scan(&chatID)
	if err != nil {
		return ""
	}

	msgs, err := h.conversations.Read(ctx, chatID, 0, 0)
	if err != nil || len(msgs) == 0 {
		return ""
	}

	// Take last N messages
	start := 0
	if len(msgs) > keeperConvHistoryLimit {
		start = len(msgs) - keeperConvHistoryLimit
	}
	msgs = msgs[start:]

	var sb strings.Builder
	for _, m := range msgs {
		// Skip tool messages (noisy, not useful for intent verification)
		if m.Role == "tool" {
			continue
		}
		content := m.Content
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		fmt.Fprintf(&sb, "[%s]: %s\n", m.Role, content)
	}
	return sb.String()
}
