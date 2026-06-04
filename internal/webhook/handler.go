package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// WebhookPayload is the JSON body received from an external webhook call,
// containing the event type, source identifier, and arbitrary data.
type WebhookPayload struct {
	Event  string    `json:"event"`
	Source string    `json:"source"`
	Data   any       `json:"data,omitempty"`
	RecvAt time.Time `json:"received_at"`
}

// SecretLookup retrieves the expected webhook secret for a given crew and agent pair.
type SecretLookup func(ctx context.Context, crewID, agentID string) (string, error)

// TriggerFunc is called after a webhook is validated, passing the crew/agent IDs
// and parsed payload to initiate an agent run.
type TriggerFunc func(ctx context.Context, crewID, agentID string, payload WebhookPayload) error

// Handler is an HTTP handler that validates incoming webhook requests using
// a shared secret and triggers agent runs on success.
type Handler struct {
	logger       *slog.Logger
	lookupSecret SecretLookup
	trigger      TriggerFunc
}

// NewHandler creates a webhook Handler with the given secret lookup and trigger functions.
func NewHandler(logger *slog.Logger, lookup SecretLookup, trigger TriggerFunc) *Handler {
	return &Handler{
		logger:       logger,
		lookupSecret: lookup,
		trigger:      trigger,
	}
}

// ServeHTTP handles incoming webhook POST requests by validating the
// X-Webhook-Secret header, parsing the JSON body, and triggering the agent run.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	crewID := r.PathValue("crewId")
	agentID := r.PathValue("agentId")
	if crewID == "" || agentID == "" {
		http.Error(w, "missing team or agent ID", http.StatusBadRequest)
		return
	}

	// Two auth shapes accepted (in order of preference):
	//
	//   X-Signature        hex HMAC-SHA256 of the raw body using the agent's
	//                      shared secret as the HMAC key. This is the same
	//                      shape the pipeline-webhook route uses, and it's
	//                      the model issue #537 wants the agent route to
	//                      converge on — body integrity is covered and a
	//                      leaked log-line capture of the header is useless
	//                      without the body it signed.
	//
	//   X-Webhook-Secret   plaintext shared secret. Pre-HMAC contract;
	//                      kept for one release as a deprecation window so
	//                      external systems already firing webhooks don't
	//                      break at upgrade. Emits a Deprecation response
	//                      header so callers can see they're on the old
	//                      shape. After the deprecation window we'll
	//                      remove this branch and the plain secret will
	//                      get rejected.
	providedSig := r.Header.Get("X-Signature")
	providedSecret := r.Header.Get("X-Webhook-Secret")
	if providedSig == "" && providedSecret == "" {
		h.logger.Warn("webhook missing signature/secret", "crew_id", crewID, "agent_id", agentID)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	expectedSecret, err := h.lookupSecret(r.Context(), crewID, agentID)
	if err != nil {
		h.logger.Error("webhook secret lookup failed", "error", err, "crew_id", crewID, "agent_id", agentID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Auth check after body read so HMAC validation has the bytes it signs.
	switch {
	case providedSig != "":
		if !ValidateHMAC(body, providedSig, expectedSecret) {
			h.logger.Warn("webhook invalid HMAC signature", "crew_id", crewID, "agent_id", agentID)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	case providedSecret != "":
		if !ValidateSecret(providedSecret, expectedSecret) {
			h.logger.Warn("webhook invalid secret", "crew_id", crewID, "agent_id", agentID)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Deprecation", `version="X-Webhook-Secret"`)
		w.Header().Set("Sunset", "Thu, 31 Dec 2026 23:59:59 GMT")
		h.logger.Warn("webhook accepted on deprecated X-Webhook-Secret path; migrate to X-Signature (HMAC-SHA256 of body)",
			"crew_id", crewID, "agent_id", agentID)
	}

	var payload WebhookPayload
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	}
	payload.RecvAt = time.Now().UTC()

	if err := h.trigger(r.Context(), crewID, agentID, payload); err != nil {
		h.logger.Error("webhook trigger failed", "error", err, "crew_id", crewID, "agent_id", agentID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.logger.Info("webhook triggered", "crew_id", crewID, "agent_id", agentID, "event", payload.Event, "source", payload.Source)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "accepted",
	})
}
