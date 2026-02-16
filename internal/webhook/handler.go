package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type WebhookPayload struct {
	Event  string    `json:"event"`
	Source string    `json:"source"`
	Data   any       `json:"data,omitempty"`
	RecvAt time.Time `json:"received_at"`
}

type SecretLookup func(ctx context.Context, teamID, agentID string) (string, error)

type TriggerFunc func(ctx context.Context, teamID, agentID string, payload WebhookPayload) error

type Handler struct {
	logger       *slog.Logger
	lookupSecret SecretLookup
	trigger      TriggerFunc
}

func NewHandler(logger *slog.Logger, lookup SecretLookup, trigger TriggerFunc) *Handler {
	return &Handler{
		logger:       logger,
		lookupSecret: lookup,
		trigger:      trigger,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	teamID := r.PathValue("teamId")
	agentID := r.PathValue("agentId")
	if teamID == "" || agentID == "" {
		http.Error(w, "missing team or agent ID", http.StatusBadRequest)
		return
	}

	providedSecret := r.Header.Get("X-Webhook-Secret")
	if providedSecret == "" {
		h.logger.Warn("webhook missing secret", "team_id", teamID, "agent_id", agentID)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	expectedSecret, err := h.lookupSecret(r.Context(), teamID, agentID)
	if err != nil {
		h.logger.Error("webhook secret lookup failed", "error", err, "team_id", teamID, "agent_id", agentID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if !ValidateSecret(providedSecret, expectedSecret) {
		h.logger.Warn("webhook invalid secret", "team_id", teamID, "agent_id", agentID)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var payload WebhookPayload
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	}
	payload.RecvAt = time.Now().UTC()

	if err := h.trigger(r.Context(), teamID, agentID, payload); err != nil {
		h.logger.Error("webhook trigger failed", "error", err, "team_id", teamID, "agent_id", agentID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.logger.Info("webhook triggered", "team_id", teamID, "agent_id", agentID, "event", payload.Event, "source", payload.Source)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "accepted",
	})
}
