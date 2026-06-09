package api

import (
	"context"
	"net/http"
	"testing"
)

// stubModelValidator implements ModelValidator with a fixed valid set.
type stubModelValidator struct {
	valid map[string]bool
	ok    bool
}

func (s stubModelValidator) providerModelIDs(context.Context, string, string) (map[string]bool, bool) {
	return s.valid, s.ok
}

func TestAgentUpdate_BogusModelRejected(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	h.SetModelValidator(stubModelValidator{valid: map[string]bool{"claude-opus-4-8": true}, ok: true})
	seedAgentRow(t, h.db, "ag-model", wsID, "", "Mira", "mira", "AGENT")

	// llm_provider supplied in body; bogus model must 400.
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-model",
		`{"llm_provider":"ANTHROPIC","llm_model":"claude-made-up-9"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bogus model update = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

func TestAgentUpdate_ValidModelAccepted(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	h.SetModelValidator(stubModelValidator{valid: map[string]bool{"claude-opus-4-8": true}, ok: true})
	seedAgentRow(t, h.db, "ag-ok", wsID, "", "Vee", "vee", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-ok",
		`{"llm_provider":"ANTHROPIC","llm_model":"claude-opus-4-8"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid model update = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
}

func TestAgentUpdate_ModelProviderFromDB(t *testing.T) {
	// No llm_provider in body — the handler must read the agent's stored
	// provider to validate the model.
	h, userID, wsID := covAUHandler(t)
	h.SetModelValidator(stubModelValidator{valid: map[string]bool{"gpt-4o": true}, ok: true})
	seedAgentRow(t, h.db, "ag-db", wsID, "", "Otto", "otto", "AGENT")
	if _, err := h.db.Exec(`UPDATE agents SET llm_provider = 'OPENAI' WHERE id = 'ag-db'`); err != nil {
		t.Fatalf("set provider: %v", err)
	}

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-db", `{"llm_model":"not-a-real-model"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bogus model (provider from DB) = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

func TestAgentUpdate_ModelUndeterminablePassesThrough(t *testing.T) {
	// ok=false means the set is unknowable — the handler must NOT reject.
	h, userID, wsID := covAUHandler(t)
	h.SetModelValidator(stubModelValidator{ok: false})
	seedAgentRow(t, h.db, "ag-pass", wsID, "", "Pax", "pax", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-pass",
		`{"llm_provider":"OLLAMA","llm_model":"whatever-local"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("undeterminable model = %d, want 200 (passthrough) (%s)", rr.Code, rr.Body.String())
	}
}

func TestAgentUpdate_NoValidatorPassesThrough(t *testing.T) {
	// Legacy router with no validator wired: llm_model passes through.
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-legacy", wsID, "", "Lee", "lee", "AGENT")

	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-legacy",
		`{"llm_provider":"ANTHROPIC","llm_model":"anything-goes"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("no-validator model update = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
}
