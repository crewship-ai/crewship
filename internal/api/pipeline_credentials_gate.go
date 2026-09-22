package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// gateMissingCredentials enforces a routine's declared credentials_required
// against the credentials its author crew's workspace vault actually holds
// (#1418). It returns true after writing a 422 for confirmed absence (with
// `missing_credentials`) or a 503 for unknown availability when the run MUST
// be blocked; the caller returns immediately. It parallels
// gateMissingIntegrations / gateMissingResources and shares their contract:
//
//   - empty required → fast path, returns false (no DB work).
//   - unavailable DB or probe error → block with 503; availability is unknown.
//   - confirmed absence → block with 422 and the missing credential types.
//
// Declaring a credential is always allowed at persist/save time — a
// definition may name a credential the vault doesn't hold yet. Enforcement
// runs on every dispatch path that would actually resolve secrets: Run,
// InternalRun, RunBatch, AND the TestRun save-preview gate, where it sits
// alongside its sibling gateMissingIntegrations / gateMissingResources
// preconditions. Only the executor's own dry_run mode (which carries no
// persisted status and touches no vault) is exempt. Enforcing here means the
// {{ secrets.* }} resolver never fails deep in a runner with an opaque auth
// error instead of a clear, actionable 422.
func (h *PipelineHandler) gateMissingCredentials(w http.ResponseWriter, r *http.Request, workspaceID, crewID, crewName string, dsl *pipeline.DSL) bool {
	missing, err := h.findMissingCredentials(r.Context(), workspaceID, crewID, dsl)
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, err.Error())
		return true
	}
	if len(missing) == 0 {
		return false
	}

	if crewName == "" {
		crewName = lookupCrewName(r.Context(), h.db, workspaceID, crewID)
	}
	if crewName == "" {
		crewName = crewID
	}
	detail := missingCredentialsDetail(missing, crewName)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":                "about:blank",
		"title":               http.StatusText(http.StatusUnprocessableEntity),
		"status":              http.StatusUnprocessableEntity,
		"detail":              detail,
		"instance":            r.URL.Path,
		"missing_credentials": missing,
	})
	return true
}

var errCredentialCheckUnavailable = errors.New("required credentials could not be checked; retry when the credential store is available")

// findMissingCredentials shares the fail-closed decision with in-process runs.
func (h *PipelineHandler) findMissingCredentials(ctx context.Context, workspaceID, crewID string, dsl *pipeline.DSL) ([]string, error) {
	required := pipeline.RequiredCredentialTypes(dsl)
	if len(required) == 0 {
		return nil, nil // no-op fast path
	}
	if h.db == nil {
		h.logger.Warn("credential gate: no db to probe against, blocking run",
			"workspace_id", workspaceID, "crew_id", crewID)
		return nil, errCredentialCheckUnavailable
	}
	probe := pipeline.NewVaultCredentialProbe(h.db)
	llmProbe := pipeline.NewAnthropicLLMCredentialProbe(h.db)
	scope := pipeline.RunScope{WorkspaceID: workspaceID, AuthorCrewID: crewID}
	var missing []string
	for _, credType := range required {
		ok, err := probe(ctx, scope, credType)
		if err != nil {
			// Unknown availability must not satisfy a declared requirement.
			h.logger.Warn("credential gate: probe failed, blocking run",
				"workspace_id", workspaceID, "crew_id", crewID, "type", credType, "error", err)
			return nil, errCredentialCheckUnavailable
		}
		if ok {
			continue
		}
		// Not resolvable as an exact-type, author-crew-scoped secret. Before
		// blocking, check the OTHER resolution path an agent step uses: an
		// Anthropic LLM requirement (api_key / ai_cli_token) is satisfied by a
		// workspace-wide Anthropic key of EITHER accepted type, exactly as
		// LLMRunner.providerForWorkspace resolves it. The gate must never 422 a
		// run the runner could finish — that was the #1418 gate's blind spot,
		// which 422'd approval-gate-demo whenever the vault held an
		// AI_CLI_TOKEN (or a key pinned to a non-author crew) for an api_key
		// requirement.
		if pipeline.IsAnthropicLLMCredentialType(credType) {
			llmOK, llmErr := llmProbe(ctx, workspaceID)
			if llmErr != nil {
				// Apply the same rule to the provider-aware fallback.
				h.logger.Warn("credential gate: anthropic probe failed, blocking run",
					"workspace_id", workspaceID, "crew_id", crewID, "type", credType, "error", llmErr)
				return nil, errCredentialCheckUnavailable
			}
			if llmOK {
				continue
			}
		}
		missing = append(missing, credType)
	}
	return missing, nil
}

// missingCredentialsDetail is the human sentence both renderings use.
func missingCredentialsDetail(missing []string, crewName string) string {
	if len(missing) > 1 {
		return fmt.Sprintf("routine requires %d credentials not present in the vault for crew %q: %s",
			len(missing), crewName, strings.Join(missing, ", "))
	}
	return fmt.Sprintf("routine requires credential of type %q not present in the vault for crew %q", missing[0], crewName)
}
