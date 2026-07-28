package api

// Credential ↔ tool readiness. Answers one question for a crew: which of
// the credentials it can use are for a CLI that isn't in its container?
//
// The failure mode. The sandbox runtime image
// (docker/crewship-sandbox/Dockerfile) ships git, curl and jq. `gh`,
// `aws`, `az`, `gcloud`, `kubectl`, `docker`, `terraform`, `node`/`npm`
// and `ansible` arrive ONLY when the crew's devcontainer config declares
// the matching feature. Nothing linked "this workspace has a GitHub
// credential" to "this crew needs the github-cli feature", so a user
// could add a perfectly valid PAT, watch the vault go green, and still
// get `gh: command not found` from the agent — with no surface anywhere
// connecting the two facts. This one reports the link.
//
// Strictly read-only and advisory. It does not install anything, does
// not touch the crew's devcontainer config, and does not trigger a
// rebuild: adding the feature is a separate, user-confirmed action
// (`crewship crew config`). A provider we have no opinion about reports
// nothing rather than guessing, for the same reason the env-var map is a
// suggestion and not a gate — a false "you're missing X" costs the user
// a pointless rebuild and teaches them to ignore the report.
//
// Two existing maps meet here and must agree:
//   - credprovider.RequiredFeature: provider → devcontainer feature ref
//   - featureToolNames (crew_resources.go): feature id → CLI binary name
//
// Matching happens on the TOOL name, not the feature ref, because more
// than one feature installs the same binary (docker-in-docker vs
// docker-outside-of-docker) and mise can put a tool on PATH with no
// devcontainer feature at all. Comparing refs would tell a user with a
// working `docker` to install docker a second time.

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/credprovider"
)

// credentialToolGap is one credential whose CLI the crew's container does
// not have. Feature/FeatureID name the fix; the caller applies it (or
// not) itself.
type credentialToolGap struct {
	CredentialID   string `json:"credential_id"`
	CredentialName string `json:"credential_name"`
	Provider       string `json:"provider"`
	// Tool is the binary the credential is meant to be read by, e.g. "gh".
	Tool string `json:"tool"`
	// Feature is the devcontainer feature ref that installs Tool, and
	// FeatureID its short id — the form `crew config` writes and the
	// form crew_resources.go reads back, respectively.
	Feature   string `json:"feature"`
	FeatureID string `json:"feature_id"`
}

type crewCredentialReadinessResponse struct {
	CrewID   string `json:"crew_id"`
	CrewSlug string `json:"crew_slug"`
	// Tools is what the crew's container has today (devcontainer features
	// + mise), so the caller can explain what IS present without a second
	// round-trip to /capabilities.
	Tools []string `json:"tools"`
	// Checked counts the credentials that carry a known tool requirement.
	// Gaps is a subset; checked-len(gaps) is how many are already served.
	Checked int                 `json:"checked"`
	Gaps    []credentialToolGap `json:"gaps"`
}

// resolveCrewCredentialReadiness computes the report for one crew.
// workspaceID is required and not optional: a WORKSPACE-scoped credential
// has no crew link by definition, so without the tenant predicate the
// scope='WORKSPACE' branch below would match every workspace's rows.
func resolveCrewCredentialReadiness(ctx context.Context, db *sql.DB, workspaceID, crewID string) (*crewCredentialReadinessResponse, error) {
	out := &crewCredentialReadinessResponse{
		CrewID: crewID,
		Tools:  []string{},
		Gaps:   []credentialToolGap{},
	}
	if strings.TrimSpace(crewID) == "" || strings.TrimSpace(workspaceID) == "" {
		return out, nil
	}

	// What the container has. Lenient by contract — a malformed config
	// contributes no tools rather than erroring, so the report degrades
	// to "you're missing everything" instead of to nothing at all.
	res, err := ResolveCrewResources(ctx, db, crewID)
	if err != nil {
		return nil, err
	}
	have := make(map[string]struct{}, len(res.Tools))
	for _, tc := range res.Tools {
		have[tc.Type] = struct{}{}
		out.Tools = append(out.Tools, tc.Type)
	}

	// Which credentials this crew can actually use — the four delivery
	// sources, matching loadDeliveredCredentials: workspace-wide, directly
	// crew-scoped (credential_crews), assigned to one of the crew's agents,
	// or reachable through a binding scoped to the crew / one of its agents /
	// the workspace. A sibling crew's CREW-scoped credential with no binding
	// into this crew matches none and is correctly absent.
	//
	// Lease gate (#1373): a per-agent grant may carry a TTL. Once it
	// lapses the container no longer holds the credential, so it must
	// stop generating work for the user. credential_crews grants have no
	// lease column and are unaffected.
	leaseNow := time.Now().UTC().Format(time.RFC3339)
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.name, c.provider
		FROM credentials c
		WHERE c.workspace_id = ?
		  AND c.deleted_at IS NULL
		  AND c.status != 'REVOKED'
		  AND (
			c.scope = 'WORKSPACE'
			OR EXISTS (SELECT 1 FROM credential_crews cc
			           WHERE cc.credential_id = c.id AND cc.crew_id = ?)
			OR EXISTS (SELECT 1 FROM agent_credentials ac
			           JOIN agents a ON a.id = ac.agent_id
			           WHERE ac.credential_id = c.id
			             AND a.crew_id = ? AND a.deleted_at IS NULL
			             AND (ac.expires_at IS NULL OR ac.expires_at > ?))
			-- Bindings, the fourth delivery source. A credential reachable ONLY
			-- through a CREW/AGENT binding — not workspace-scoped, no
			-- credential_crews link, no per-agent grant — is still delivered to
			-- this crew's containers, so its missing tool is still a real gap.
			-- Omitting this made the readiness report exactly the false green it
			-- exists to prevent: the vault looks fine, the agent gets
			-- command-not-found.
			OR EXISTS (SELECT 1 FROM credential_bindings b
			           WHERE b.credential_id = c.id AND b.workspace_id = ?
			             AND (   b.scope = 'WORKSPACE'
			                  OR (b.scope = 'CREW'  AND b.crew_id = ?)
			                  OR (b.scope = 'AGENT' AND b.agent_id IN (
			                        SELECT id FROM agents WHERE crew_id = ? AND deleted_at IS NULL))))
		  )
		ORDER BY c.name, c.id`,
		workspaceID, crewID, crewID, leaseNow, workspaceID, crewID, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id, name, provider string
		if err := rows.Scan(&id, &name, &provider); err != nil {
			return nil, err
		}
		ref := credprovider.RequiredFeature(provider)
		if ref == "" {
			continue // no opinion → say nothing
		}
		fid := featureID(ref)
		tool := featureToolName(fid)
		out.Checked++
		if _, ok := have[tool]; ok {
			continue
		}
		out.Gaps = append(out.Gaps, credentialToolGap{
			CredentialID:   id,
			CredentialName: name,
			Provider:       provider,
			Tool:           tool,
			Feature:        ref,
			FeatureID:      fid,
		})
	}
	return out, rows.Err()
}

// CredentialReadiness GET /api/v1/crews/{crewId}/credential-readiness
//
// Read-only: reports which of the crew's credentials need a devcontainer
// feature the crew has not declared. Never mutates config and never
// triggers a rebuild.
func (h *CrewHandler) CredentialReadiness(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	// Resolve + isolation-check in one query, same as Capabilities: a
	// crew outside the caller's workspace must 404 rather than have its
	// credential inventory described.
	var crewSlug string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID).Scan(&crewSlug)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	if err != nil {
		h.logger.Error("credential readiness: resolve crew", "error", err, "crew_id", crewID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	res, err := resolveCrewCredentialReadiness(r.Context(), h.db, workspaceID, crewID)
	if err != nil {
		h.logger.Error("credential readiness: resolve", "error", err, "crew_id", crewID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	res.CrewSlug = crewSlug
	writeJSON(w, http.StatusOK, res)
}
