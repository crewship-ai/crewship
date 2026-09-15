package api

// Agent-scoped credential readiness (#2183): does a credential the runtime
// will actually deliver authenticate this agent's model?
//
// The second member of the readiness family. The crew one
// (crew_credential_readiness.go) answers "which credentials need a CLI the
// container lacks"; this one answers the question #2169 was filed about and
// #2177's overview guard could not: an agent whose only reachable credential
// is a crew's GH_TOKEN binding has a non-empty credential list and no way to
// call its model.
//
// The classification is NOT done here. It is the orchestrator's
// (orchestrator.ModelCredentialReadiness), built from the same selectors the
// run uses — credTypeToProvider, credentialOAuthKind, the adapter's
// AuthDelivery — over the same delivery set (loadDeliveredCredentials, the
// chokepoint every boot path reads). This handler loads the agent, loads the
// set without decrypting anything, asks, and renders. Putting the provider
// tables in a second place — this file, or TypeScript — is how the two guards
// before it went wrong.
//
// Same posture as the crew report: strictly read-only and advisory, and
// "unknown" wherever the runtime has no opinion, because a false "missing"
// on a healthy agent is the inverse of the bug and teaches the operator to
// ignore the report.

import (
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// agentModelCredential is the verdict for the model slot.
type agentModelCredential struct {
	// State is ready, missing or unknown (orchestrator.ModelCredentialState).
	State string `json:"state"`
	// CredentialName, Source and Delivery describe the match when ready.
	// Source uses the vocabulary of `crewship credential resolve`
	// (agent_grant, agent_binding, crew_binding, workspace_binding,
	// crew_link); Delivery says which channel carries it (sidecar,
	// login_env, login_file, env) — so "ready" beside a dummy
	// ANTHROPIC_API_KEY in the env reads as the design it is.
	CredentialName string `json:"credential_name,omitempty"`
	CredentialID   string `json:"credential_id,omitempty"`
	Source         string `json:"source,omitempty"`
	Delivery       string `json:"delivery,omitempty"`
	// Provider is the LLM provider the adapter pays through, empty when the
	// state is unknown for want of one.
	Provider string `json:"provider,omitempty"`
}

type agentCredentialReadinessResponse struct {
	AgentID         string               `json:"agent_id"`
	AgentSlug       string               `json:"agent_slug"`
	Adapter         string               `json:"adapter"`
	ModelCredential agentModelCredential `json:"model_credential"`
	// Notes is operator-facing prose: why an unknown is unknown, what a
	// missing is missing. Never null.
	Notes []string `json:"notes"`
}

// resolveAgentCredentialReadiness computes the report for one agent. The
// agent is assumed to exist in workspaceID (the handler checks); the row's
// adapter, provider and model are read here because they are the inputs.
func resolveAgentCredentialReadiness(r *http.Request, db *sql.DB, workspaceID, agentID string) (*agentCredentialReadinessResponse, error) {
	ctx := r.Context()
	var slug, adapter, llmProvider, llmModel string
	if err := db.QueryRowContext(ctx,
		`SELECT slug, COALESCE(cli_adapter, ''), COALESCE(llm_provider, ''), COALESCE(llm_model, '')
		 FROM agents WHERE id = ? AND deleted_at IS NULL`, agentID).
		Scan(&slug, &adapter, &llmProvider, &llmModel); err != nil {
		return nil, err
	}

	// The delivery set, still encrypted: readiness is decided by type,
	// provider, variable name and handle-only — never by the value, which
	// this endpoint must not open for a report.
	delivered, _, err := loadDeliveredCredentials(ctx, db, agentID)
	if err != nil {
		return nil, err
	}
	creds := make([]orchestrator.Credential, 0, len(delivered))
	byID := make(map[string]deliveredCredential, len(delivered))
	for _, d := range delivered {
		byID[d.ID] = d
		c := orchestrator.Credential{
			ID: d.ID, EnvVarName: d.EnvVar, Priority: d.Priority, Type: d.Type,
			Provider: d.Provider, LeaseExpiresAt: d.LeaseExpiresAt, HandleOnly: d.HandleOnly,
		}
		// A provider login's mode part decides whether it is a seat or a
		// metered key (credentialOAuthKind); the part is cleartext, so it
		// travels. Secret parts are left sealed.
		for _, f := range d.Fields {
			if f.IsSecret {
				continue
			}
			c.Fields = append(c.Fields, orchestrator.CredentialField{Key: f.Key, EnvVar: f.EnvVar, Value: f.Value})
		}
		creds = append(creds, c)
	}

	rep := orchestrator.ModelCredentialReadiness(adapter, llmProvider, llmModel, creds)
	out := &agentCredentialReadinessResponse{
		AgentID:   agentID,
		AgentSlug: slug,
		Adapter:   adapter,
		ModelCredential: agentModelCredential{
			State:    string(rep.State),
			Provider: rep.Provider,
		},
		Notes: rep.Notes,
	}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if rep.State == orchestrator.ModelCredentialReady {
		d := byID[rep.CredentialID]
		out.ModelCredential.CredentialID = d.ID
		out.ModelCredential.Source = bindingSourceLabel(d.Source)
		out.ModelCredential.Delivery = string(rep.Delivery)
		// The name is display metadata the management surface redacts per
		// caller (visibleCredentialNames); the verdict itself is not
		// redacted, because the run outcome it predicts is not either.
		visible, err := visibleCredentialNames(r, db, workspaceID)
		if err != nil {
			return nil, err
		}
		out.ModelCredential.CredentialName = visible[d.ID]
	}
	return out, nil
}

// CredentialReadiness GET /api/v1/agents/{agentId}/credential-readiness
//
// Read-only: reports whether a credential the runtime delivers to this
// agent authenticates its adapter's model provider. Never mutates.
func (h *AgentHandler) CredentialReadiness(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	if agentID == "" {
		replyError(w, http.StatusBadRequest, "agentId is required")
		return
	}

	// Existence + isolation in one query: an agent outside the caller's
	// workspace must 404 rather than have its credential posture described.
	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "credential readiness: check agent exists", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	res, err := resolveAgentCredentialReadiness(r, h.db, workspaceID, agentID)
	if err != nil {
		replyInternalError(w, h.logger, "credential readiness: resolve", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
