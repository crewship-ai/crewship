package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// missingResource is one unmet precondition reported to the caller: a
// datastore or tool the routine DECLARES it requires but the executing crew's
// container does not HAVE. Rendered into the 422 Problem Details
// `missing_resources` array so a client can act on it without scraping prose.
type missingResource struct {
	Kind string `json:"kind"` // "datastore" | "tool"
	Type string `json:"type"` // engine/tool family, e.g. "postgres" | "ansible"
	Name string `json:"name,omitempty"`
}

// declaredResources extracts the routine's DECLARED resources block (the
// datastores + tools an author explicitly lists in `spec.resources`). Returns
// nils when nothing is declared, so the gate's no-op fast path triggers for
// every existing routine that predates the resources block. We deliberately
// take the declared lists rather than DSL.ExtractManifest().Tools, which folds
// in code-step runtimes that are not crew CLIs (see gateMissingResources doc).
func declaredResources(d *pipeline.DSL) ([]pipeline.DatastoreRef, []pipeline.ToolRef) {
	if d == nil || d.Resources == nil {
		return nil, nil
	}
	return d.Resources.Datastores, d.Resources.Tools
}

// gateMissingResources enforces a routine's DECLARED resource preconditions
// (resources.datastores + resources.tools) against what its author crew's
// container actually HAS (ResolveCrewResources). It is the resource sibling of
// gateMissingIntegrations and follows the same contract: it returns true —
// having ALREADY written a 422 Problem Details with a machine-readable
// `missing_resources` array — when the run MUST be blocked; the caller returns
// immediately.
//
// Matching rules:
//   - datastore: satisfied if the crew has any datastore of the same engine
//     Type (case-insensitive). Name is advisory and not matched — a crew may
//     name its postgres service anything; the engine is what the routine needs.
//   - tool: satisfied if the crew has any installed tool whose friendly Type
//     OR Name equals the required tool's Type (case-insensitive). The required
//     ToolRef.Name is the concrete artifact ("deploy.yml"), not something the
//     container "has", so it is reported but not matched.
//
// Semantics (mirrors the integration gate):
//   - empty required datastores+tools → fast path, returns false (no DB work).
//   - crewID == "" or h.db == nil → FAIL-OPEN: nothing to resolve "has"
//     against, so allow the run rather than block on absence of a catalog.
//   - resolver error → FAIL-OPEN: log a warning and ALLOW. A bug in resource
//     resolution must never wedge every run; a forgotten datastore is a soft
//     failure the operator can fix, a hard block on all runs is self-inflicted.
//
// NOTE on scope: this gates the DECLARED resources block only — not the
// derived pipeline.Manifest.Tools, which folds in code-step runtimes (e.g.
// "cel"). Those internal runtimes are not crew CLIs, and gating them would
// block every code routine that declares no resources, breaking the
// "additive / no-op for existing routines" guarantee. The manifest's declared
// Datastores are identical to resources.datastores, so there is no divergence
// on the datastore side.
func (h *PipelineHandler) gateMissingResources(w http.ResponseWriter, r *http.Request, workspaceID, crewID, crewName string, datastores []pipeline.DatastoreRef, tools []pipeline.ToolRef) bool {
	if len(datastores) == 0 && len(tools) == 0 {
		return false // no-op fast path
	}
	if h.db == nil || crewID == "" {
		h.logger.Warn("resource gate: no crew/db to resolve against, allowing run (fail-open)",
			"workspace_id", workspaceID, "crew_id", crewID)
		return false
	}
	res, err := ResolveCrewResources(r.Context(), h.db, crewID)
	if err != nil {
		// FAIL-OPEN — see the doc comment. Never block on a resolver bug.
		h.logger.Warn("resource gate: resolution failed, allowing run (fail-open)",
			"workspace_id", workspaceID, "crew_id", crewID, "error", err)
		return false
	}

	haveDatastore := make(map[string]bool, len(res.Datastores))
	for _, d := range res.Datastores {
		haveDatastore[strings.ToLower(strings.TrimSpace(d.Type))] = true
	}
	haveTool := make(map[string]bool, len(res.Tools)*2)
	for _, t := range res.Tools {
		haveTool[strings.ToLower(strings.TrimSpace(t.Type))] = true
		haveTool[strings.ToLower(strings.TrimSpace(t.Name))] = true
	}

	var missing []missingResource
	for _, d := range datastores {
		typ := strings.TrimSpace(d.Type)
		if typ == "" {
			continue // unfamilied requirement is unmatchable — skip, don't block
		}
		if !haveDatastore[strings.ToLower(typ)] {
			missing = append(missing, missingResource{Kind: "datastore", Type: typ, Name: strings.TrimSpace(d.Name)})
		}
	}
	for _, t := range tools {
		typ := strings.TrimSpace(t.Type)
		if typ == "" {
			continue
		}
		if !haveTool[strings.ToLower(typ)] {
			missing = append(missing, missingResource{Kind: "tool", Type: typ, Name: strings.TrimSpace(t.Name)})
		}
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

	parts := make([]string, 0, len(missing))
	for _, m := range missing {
		parts = append(parts, m.Kind+" "+m.Type)
	}
	detail := fmt.Sprintf("routine needs %s, not available to crew %q", strings.Join(parts, ", "), crewName)

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":              "about:blank",
		"title":             http.StatusText(http.StatusUnprocessableEntity),
		"status":            http.StatusUnprocessableEntity,
		"detail":            detail,
		"instance":          r.URL.Path,
		"missing_resources": missing,
	})
	return true
}
