package api

// `pays_with` on GET /api/v1/agents/{id} — docs/prd/provider-logins.md
// §10.3: which provider login pays for this agent's model. Derived from the
// agent's resolved credential delivery (the same chokepoint the run uses) and
// the adapter's provider, so what the console shows is what the container
// gets, and a Claude agent holding only an OpenAI login is shown paying with
// nothing rather than with a login it cannot use.

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/providerlogin"
)

type agentPaysWith struct {
	CredentialID string     `json:"credential_id,omitempty"`
	Name         string     `json:"name,omitempty"`
	Login        *loginView `json:"login,omitempty"`
	Provider     string     `json:"provider,omitempty"`
	Restricted   bool       `json:"restricted,omitempty"`
}

// loadAgentPaysWith picks the first delivered login of the adapter's
// provider, in delivery order (priority, then source rank — the order the
// orchestrator's own selectors take the first match in).
func loadAgentPaysWith(ctx context.Context, db *sql.DB, logger *slog.Logger, agentID, adapter, llmProvider string) *agentPaysWith {
	want := providerlogin.AdapterProvider(adapter, llmProvider)
	if want == "" {
		return nil
	}
	delivered, _, err := loadDeliveredCredentials(ctx, db, agentID)
	if err != nil {
		if logger != nil {
			logger.Warn("agent pays_with: delivery", "agent_id", agentID, "error", err)
		}
		return nil
	}
	for _, d := range delivered {
		if !isLoginRow(d.Type, d.Provider) || providerlogin.Canonical(d.Provider) != want {
			continue
		}
		if !canRole(RoleFromContext(ctx), "manage") {
			return &agentPaysWith{Provider: want, Restricted: true}
		}
		login, name, err := loadLoginView(ctx, db, logger, d.ID)
		if err != nil || login == nil {
			continue
		}
		return &agentPaysWith{CredentialID: d.ID, Name: name, Login: login}
	}
	return nil
}
