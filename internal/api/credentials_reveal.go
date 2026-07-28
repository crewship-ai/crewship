package api

// Credential reveal — the one endpoint in Crewship that hands a stored
// secret back in plaintext. PRD-CREDENTIALS-V2-2026 §2.6, phase P7 (core).
//
// The governing idea from §2.6 is that reveal is not a permission, it is a
// ceremony: no single condition grants it, and an attacker has to break every
// one of them. Each layer below is independently sufficient to refuse, and
// each fails CLOSED — an error inside a check denies, it never falls through.
//
// Shipped here (the v1 core §0 enumerates):
//
//	L0  classification — SEALED is never revealable, by any role
//	L1  workspace default-deny — off until an OWNER turns it on
//	L2  capability, not role — OWNER/ADMIN is necessary, never sufficient
//	L3.3 mandatory, non-trivial reason
//	L4  chained audit as a PRECONDITION — no chained write, no value
//	L9  agents never reveal — interactive human sessions only
//
// Deliberately NOT here, and deferred in §0 with reasons: four-eyes approval
// for RESTRICTED (L3.4), session freshness / step-up re-auth (L3.2),
// one-time tokens with a 30 s window (L3.5), auto-seal on anomaly (L6),
// separation-of-duty warnings (L7). Each is a real layer; adding them to this
// change would have made it unreviewable, and the core carries most of the
// value without them.
//
// The strongest control in §2.6 is not on this page at all: L8 says the UI
// offers "rotate and show the new value" as the PRIMARY action and reveal as
// the secondary one, because most legitimate reasons to reveal are really
// reasons to rotate. A control that is rarely used is a control that keeps
// working. That is P6's surface; POST /credentials/{id}/rotate already
// exists for it.

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Sensitivity classes (L0). Values are stored verbatim in
// credentials.sensitivity, whose CHECK constraint carries the same closed
// set — see migrations/20260728130246_credential_reveal_core.sql. The two
// vocabularies are pinned together by a test, because a class Go accepts but
// the column rejects is a 500, and a class the column accepts but Go does not
// recognise would slip past the SEALED comparison below.
const (
	// SensitivityStandard — dev tokens, read-only keys. Revealable with
	// the full ceremony. The default for every existing row.
	SensitivityStandard = "STANDARD"
	// SensitivityRestricted — production API keys, deploy keys. §2.6
	// earmarks these for four-eyes; until that lands they behave like
	// STANDARD, and the classification is already worth setting because
	// it is what the four-eyes gate will key off.
	SensitivityRestricted = "RESTRICTED"
	// SensitivitySealed — production databases, root credentials,
	// anything an agent created. Never revealable, by anyone, with no
	// escape hatch. Break-glass for a SEALED credential is rotation:
	// mint a new value and let the old one drain through the v70 grace
	// window. An escape hatch that exists is an escape hatch that gets
	// used.
	SensitivitySealed = "SEALED"
)

// sensitivityRank orders the classes for the raise/lower asymmetry in
// SetSensitivity. Higher is stricter.
var sensitivityRank = map[string]int{
	SensitivityStandard:   0,
	SensitivityRestricted: 1,
	SensitivitySealed:     2,
}

// AllSensitivities returns the closed set, strongest last. Used by the CLI's
// flag help and by the vocabulary-drift test.
func AllSensitivities() []string {
	return []string{SensitivityStandard, SensitivityRestricted, SensitivitySealed}
}

// validSensitivity reports whether s is one of the three classes. Rejects
// anything else, including case variants and padded values — the column
// would reject them too, and a 400 naming the allowed set is more useful
// than a constraint violation surfacing as a 500.
func validSensitivity(s string) bool {
	_, ok := sensitivityRank[s]
	return ok
}

// minRevealReasonLen is the floor for L3.3. Twenty characters is roughly a
// short sentence — enough to force "rotating the deploy key for INC-4412"
// rather than "fix". The number is a judgement call, not a derivation: §2.6
// says "minimum length" without picking one. Too low and the field becomes
// decorative; too high and people paste filler, which is worse than a short
// honest reason.
const minRevealReasonLen = 20

// genericReveal reasons rejected outright regardless of length. §2.6 names
// "test" specifically. The list is short and deliberately so — it catches the
// reflexive placeholder, not every possible bad reason. A determined liar
// writes a plausible sentence and no validator stops them; the point is that
// the audit trail should not be full of the word "test" six months from now
// when someone is trying to reconstruct an incident.
var genericRevealReasons = map[string]struct{}{
	"test": {}, "testing": {}, "tests": {}, "debug": {}, "debugging": {},
	"reason": {}, "because": {}, "n/a": {}, "na": {}, "none": {},
	"asdf": {}, "foo": {}, "bar": {}, "x": {}, "-": {}, "tmp": {}, "temp": {},
	"check": {}, "checking": {}, "just": {}, "please": {},
}

// Wire-facing refusal messages. They are distinct per layer on purpose.
//
// The usual argument against that is oracle risk, and it does not apply here:
// every caller who can reach a non-generic message is already MANAGER+ in the
// workspace and can read the credential's classification and the workspace
// policy through the ordinary UI. Nothing below tells them something they
// could not already look up. What distinct messages DO buy is that the
// control gets used correctly — "reveal is disabled for this workspace" sends
// an operator to Settings, while a bare "Forbidden" sends them to Slack.
const (
	msgRevealNotInteractive = "Reveal requires an interactive sign-in session. " +
		"API tokens, agents and sidecars can never reveal a credential — use `crewship credential rotate` instead."
	msgRevealWorkspaceOff = "Reveal is disabled for this workspace. " +
		"An OWNER must enable it in Settings → Access & Secrets first."
	msgRevealNoCapability = "Reveal requires the `credentials:reveal` capability on your membership. " +
		"Being an OWNER or ADMIN is not sufficient — the capability is granted per person."
	msgRevealOutOfScope = "Reveal is limited to credentials in your own crews."
	msgRevealSealed     = "This credential is SEALED and can never be revealed, by any role. " +
		"Rotate it instead — the new value is shown once at creation."
	msgRevealReasonTooShort = "A reason of at least 20 characters is required, and it is recorded in the audit log."
	msgRevealReasonGeneric  = "That reason is too generic to be useful in an audit. Say what you need the value for."
	msgCredentialNotFound   = "Credential not found"
)

// CredentialRevealHandler owns the reveal surface: the reveal itself, the
// workspace switch that gates it, and the classification that overrides both.
//
// It is a separate handler from CredentialHandler rather than more methods on
// it, for two reasons. First, it has a dependency the others do not — a
// journal.SyncEmitter, without which it must refuse to work — and folding a
// hard requirement into a struct where it is optional for everyone else
// invites someone to make it optional here too. Second, keeping the one
// endpoint that returns plaintext in its own file makes it possible to review
// the whole disclosure path by reading a single file.
type CredentialRevealHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// journal is the tamper-evident audit sink. nil is not "audit
	// disabled", it is "reveal disabled": every write path below treats a
	// missing emitter as a 500. noopEmitter would silently accept these
	// entries, so the type is deliberately SyncEmitter — a noop cannot be
	// assigned to it by accident.
	journal journal.SyncEmitter
}

func NewCredentialRevealHandler(db *sql.DB, logger *slog.Logger) *CredentialRevealHandler {
	return &CredentialRevealHandler{db: db, logger: logger}
}

// SetJournal wires the audit sink. Called during router construction once the
// journal writer exists. Passing nil leaves the handler in its fail-closed
// state — every reveal 500s — which is the correct behaviour for a server
// that booted without an audit log.
func (h *CredentialRevealHandler) SetJournal(j journal.SyncEmitter) { h.journal = j }

// errNoAuditSink is the internal marker for "no journal wired". Kept distinct
// from a DB error so the log line says which of the two happened.
var errNoAuditSink = errors.New("credential reveal: no chained audit sink configured")

// emitChained writes an entry through the synchronous path and returns the
// error unchanged. Every caller treats a non-nil return as a hard stop.
//
// This wrapper exists so the nil-emitter check cannot be forgotten at one of
// the three call sites — the failure it prevents (a reveal that succeeds with
// no audit record) is exactly the one §2.6 L4 was written to make impossible.
func (h *CredentialRevealHandler) emitChained(ctx context.Context, e journal.Entry) (string, error) {
	if h.journal == nil {
		return "", errNoAuditSink
	}
	return h.journal.EmitSync(ctx, e)
}

// revealRequest is the POST body. Reason is the only field: everything else
// the audit record needs comes from the authenticated context, so a caller
// cannot mis-attribute their own reveal.
type revealRequest struct {
	Reason string `json:"reason"`
}

// revealResponse deliberately does NOT echo anything derived from the value
// besides the value itself — no length, no prefix, no fingerprint. Those look
// harmless and are how a plaintext ends up in a log aggregator.
type revealResponse struct {
	CredentialID   string `json:"credential_id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Sensitivity    string `json:"sensitivity"`
	Value          string `json:"value"`
	RevealedAt     string `json:"revealed_at"`
	JournalEntryID string `json:"journal_entry_id"`
}

// revealTarget is the row the gate reasons about.
type revealTarget struct {
	id          string
	name        string
	credType    string
	sensitivity string
	encValue    string
}

// Reveal discloses a credential's plaintext value.
// POST /api/v1/credentials/{credentialId}/reveal
//
// Gate order, and why it is this order:
//
//  1. AUTH PATH (L9). First, because it is the only check whose answer
//     does not depend on the target at all — an API token learns nothing
//     from it, not even whether the id exists. Denying here also means the
//     remaining layers never run for a non-human caller, so a bug in one
//     of them cannot be reached from a container.
//  2. WORKSPACE SWITCH (L1). Before anything credential-specific: a
//     tenant that has not opted in has no reveal surface, and asking
//     questions about a specific credential inside one would be answering
//     questions the tenant never enabled.
//  3. ROLE FLOOR + CAPABILITY (L2). Both, in that order. The role floor
//     is MANAGER+ — see the note on revealRoleFloor. Below it, the
//     capability does not help; at or above it, the capability is still
//     required.
//  4. EXISTENCE (tenancy). 404 for a credential in another workspace,
//     never 403: a 403 would confirm the id is real somewhere in the
//     fleet, turning a leaked id into a tenancy oracle.
//  5. CREW SCOPE. 403, not 404 — inside their own tenant the caller can
//     already see this credential in the list, so hiding it would be
//     theatre while breaking the error message.
//  6. CLASSIFICATION (L0). SEALED refuses everyone including OWNER.
//  7. REASON (L3.3). Last of the refusals, so a caller who is not allowed
//     to reveal always gets the same answer no matter what they typed —
//     the reason field cannot be used to probe the layers above it.
//  8. CHAINED AUDIT (L4). The write happens BEFORE the value is read out
//     of the row, and its failure is a 500. Ordering it before the
//     decrypt means there is no code path — none — on which a value can
//     be serialised without a committed audit record preceding it. The
//     cost is that a subsequent decrypt failure leaves an audit entry for
//     a reveal that returned nothing; over-recording is the safe
//     direction for an audit log.
func (h *CredentialRevealHandler) Reveal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(ctx)
	role := RoleFromContext(ctx)
	user := UserFromContext(ctx)

	// 1. L9 — interactive human sessions only.
	if !h.requireInteractiveSession(w, r) {
		return
	}
	callerID := user.ID

	// 2. L1 — workspace default deny.
	enabled, err := revealEnabledForWorkspace(ctx, h.db, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "reveal: read workspace policy", err)
		return
	}
	if !enabled {
		h.denyReveal(w, callerID, role, credID, "workspace_switch_off", msgRevealWorkspaceOff)
		return
	}

	// 3. L2 — role is necessary, capability is what actually grants.
	if !canRole(role, revealRoleFloor) {
		h.denyReveal(w, callerID, role, credID, "below_role_floor", msgRevealNoCapability)
		return
	}
	caps, _, capErr, ok := CapabilitiesForMemberE(ctx, h.db, workspaceID, callerID)
	if capErr != nil {
		// A DB blip must not read as a permission decision in either
		// direction — 500, not 403 and certainly not a grant.
		replyInternalError(w, h.logger, "reveal: capability lookup", capErr)
		return
	}
	if !ok || !HasCapability(caps, CapabilityCredentialReveal) {
		h.denyReveal(w, callerID, role, credID, "missing_capability", msgRevealNoCapability)
		return
	}

	// 4. Tenancy.
	target, err := loadRevealTarget(ctx, h.db, workspaceID, credID)
	if err != nil {
		replyInternalError(w, h.logger, "reveal: load credential", err)
		return
	}
	if target == nil {
		replyError(w, http.StatusNotFound, msgCredentialNotFound)
		return
	}

	// 5. Crew scope.
	inScope, err := revealScopeAllows(ctx, h.db, role, user, workspaceID, credID)
	if err != nil {
		replyInternalError(w, h.logger, "reveal: scope check", err)
		return
	}
	if !inScope {
		h.denyReveal(w, callerID, role, credID, "outside_crew_scope", msgRevealOutOfScope)
		return
	}

	// 6. L0 — SEALED has no path through, for anyone.
	if target.sensitivity == SensitivitySealed {
		h.denyReveal(w, callerID, role, credID, "sealed", msgRevealSealed)
		return
	}

	// 7. L3.3 — a reason that will still mean something in six months.
	var body revealRequest
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if msg := revealReasonError(reason); msg != "" {
		replyError(w, http.StatusBadRequest, msg)
		return
	}

	// 8. L4 — the chained write is the precondition.
	//
	// Payload contents are load-bearing: actor, target, classification,
	// reason, IP. NOT the value, and NOT a hash of it. A digest looks like
	// a harmless fingerprint and is not: journal rows are readable by
	// anyone with journal access and travel in every backup, and a digest
	// of a short secret is offline-crackable. Recording one would make the
	// audit log a second copy of the vault.
	ip := clientIP(r)
	entryID, err := h.emitChained(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryCredentialRevealed,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorUser,
		ActorID:     callerID,
		Summary:     "Credential " + target.name + " revealed to " + callerID,
		Payload: map[string]any{
			"credential_id":   target.id,
			"credential_name": target.name,
			"credential_type": target.credType,
			"classification":  target.sensitivity,
			"reason":          reason,
			"actor_role":      role,
			"ip":              ip,
		},
		Refs: map[string]any{"credential_id": target.id},
	})
	if err != nil {
		// Fail closed. The value stays in the vault; the caller gets a 500
		// with no hint of it. "Return it now and audit later" is the
		// failure this layer exists to prevent.
		h.logger.Error("reveal: chained audit write failed — refusing the reveal",
			"credential_id", credID, "workspace_id", workspaceID, "user_id", callerID, "error", err)
		replyError(w, http.StatusInternalServerError, "Reveal refused: the audit record could not be written.")
		return
	}

	value, err := decryptCredential(target.encValue)
	if err != nil {
		replyInternalError(w, h.logger, "reveal: decrypt credential", err)
		return
	}

	// The flat timeline gets it too — §2.6 L4 says the chain is written
	// "not ONLY" to credential_audit, i.e. both. Best-effort here on
	// purpose: the disclosure has already happened and the tamper-evident
	// record already committed, so failing the request now would report a
	// completed reveal as an error and tell the operator nothing true.
	recordCredentialEventBestEffort(ctx, h.db, h.logger, target.id, AuditEventReveal, "", ip,
		map[string]any{
			"revealed_by":      callerID,
			"actor_role":       role,
			"classification":   target.sensitivity,
			"reason":           reason,
			"journal_entry_id": entryID,
		})

	// L3.6 / L10 — never cached, never stored by an intermediary.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, revealResponse{
		CredentialID:   target.id,
		Name:           target.name,
		Type:           target.credType,
		Sensitivity:    target.sensitivity,
		Value:          value,
		RevealedAt:     time.Now().UTC().Format(time.RFC3339),
		JournalEntryID: entryID,
	})
}

// revealRoleFloor is the canRole action a caller must satisfy before the
// capability is even consulted. "update" is the MANAGER+ tier.
//
// The obvious alternative is "manage" (OWNER/ADMIN only), which is how §2.6's
// L0 table reads. §7 decision #3 is the tie-breaker: MANAGER may hold the
// capability explicitly, "but only in their own scope and never for SEALED".
// Both constraints are enforced below — the crew-scope check and the SEALED
// check — so the floor sits at MANAGER and the narrowing happens where it can
// be seen, rather than by silently excluding a role the PRD names.
const revealRoleFloor = "update"

// requireInteractiveSession enforces L9. Returns false having already written
// the response.
//
// The gate is on the CREDENTIAL SHAPE, not the identity. An agent's request
// carries a real user's id and role — that is how delegation works — so
// asking "who is this?" cannot separate a person from a container. Asking
// "what did they present?" can: only a live, revocable user_sessions-backed
// login counts. A bearer token that survives logout, an X-Internal-Token, and
// a synthesized internal-adapter context all fail, and so does any auth method
// added later that does not explicitly opt in — AuthKindFromContext returns
// "" for anything that did not pass through RequireAuth.
func (h *CredentialRevealHandler) requireInteractiveSession(w http.ResponseWriter, r *http.Request) bool {
	ctx := r.Context()
	user := UserFromContext(ctx)
	if user == nil || user.ID == "" {
		// No identity at all. 403 rather than 401: the route is behind
		// RequireAuth, so reaching here means a synthesized context, and
		// 401 would invite a client to retry with a token that can never
		// work.
		h.denyReveal(w, "", RoleFromContext(ctx), r.PathValue("credentialId"),
			"no_identity", msgRevealNotInteractive)
		return false
	}
	if AuthKindFromContext(ctx) != AuthKindSession {
		h.denyReveal(w, user.ID, RoleFromContext(ctx), r.PathValue("credentialId"),
			"non_interactive_auth", msgRevealNotInteractive)
		return false
	}
	return true
}

// denyReveal writes a 403 and the audit WARN line in one place, so every
// refusal streams into the log with the same (user, role, action, resource)
// quartet the rest of the RBAC surface uses (rbac.go replyForbidden), plus
// the layer that refused. Grepping "credential.reveal" then answers "which
// wall did this hit" without reading the handler.
func (h *CredentialRevealHandler) denyReveal(w http.ResponseWriter, userID, role, credID, layer, msg string) {
	if h.logger != nil {
		h.logger.Warn("rbac: credential reveal denied",
			"user_id", userID,
			"role", role,
			"action", "credential.reveal",
			"resource", "credential:"+credID,
			"layer", layer,
		)
	}
	replyError(w, http.StatusForbidden, msg)
}

// revealReasonError returns the 400 message for an unusable reason, or "".
func revealReasonError(reason string) string {
	if len([]rune(reason)) < minRevealReasonLen {
		return msgRevealReasonTooShort
	}
	// Reject a reason made of one repeated placeholder ("test test test"),
	// which clears the length floor while saying exactly as much as "test".
	fields := strings.Fields(strings.ToLower(reason))
	distinct := map[string]struct{}{}
	for _, f := range fields {
		distinct[strings.Trim(f, ".,;:!?-")] = struct{}{}
	}
	if len(distinct) == 0 {
		return msgRevealReasonTooShort
	}
	allGeneric := true
	for word := range distinct {
		if _, generic := genericRevealReasons[word]; !generic {
			allGeneric = false
			break
		}
	}
	if allGeneric {
		return msgRevealReasonGeneric
	}
	// A single distinct word repeated to length is padding, not a reason.
	if len(distinct) == 1 && len(fields) > 1 {
		return msgRevealReasonGeneric
	}
	return ""
}

// revealEnabledForWorkspace reads the L1 switch. A missing workspace row
// reports false rather than an error: "the tenant does not exist" and "the
// tenant has not enabled reveal" both mean no.
func revealEnabledForWorkspace(ctx context.Context, db *sql.DB, workspaceID string) (bool, error) {
	if db == nil || workspaceID == "" {
		return false, nil
	}
	var enabled int
	err := db.QueryRowContext(ctx,
		`SELECT credential_reveal_enabled FROM workspaces WHERE id = ?`, workspaceID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled == 1, nil
}

// loadRevealTarget fetches the row scoped to the caller's workspace. Returns
// (nil, nil) when there is no such credential in THIS tenant — the caller
// turns that into a 404. Soft-deleted rows are invisible: revoking a leaked
// secret must not leave its plaintext reachable through the one endpoint
// whose purpose is disclosure.
func loadRevealTarget(ctx context.Context, db *sql.DB, workspaceID, credID string) (*revealTarget, error) {
	var t revealTarget
	err := db.QueryRowContext(ctx, `
		SELECT id, name, type, sensitivity, encrypted_value
		FROM credentials
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL
	`, credID, workspaceID).Scan(&t.id, &t.name, &t.credType, &t.sensitivity, &t.encValue)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// revealScopeAllows applies the crew-scoped visibility rule to a reveal.
//
// It reuses credentialVisibilityFilter rather than hand-rolling the join, but
// deliberately feeds it a sub-MANAGER role for MANAGER callers. The filter
// gives MANAGER+ the whole workspace, which is right for METADATA — they own
// rotation, revocation and audit review, and cannot do that job half-blind —
// and wrong for plaintext. §7 decision #3 confines a MANAGER's reveal to
// their own crews, so the reveal path re-derives the narrow branch.
//
// OWNER and ADMIN keep workspace-wide reach: they can grant themselves crew
// membership in one click, so scoping them would be a speed bump that shows
// up in the audit log as a membership change instead of a reveal — strictly
// worse.
func revealScopeAllows(ctx context.Context, db *sql.DB, role string, user *AuthUser, workspaceID, credID string) (bool, error) {
	if canRole(role, "manage") { // OWNER / ADMIN
		return true, nil
	}
	// Force the crew-scoped branch regardless of the caller's real role.
	filter, args := credentialVisibilityFilter("MEMBER", user)
	query := `SELECT 1 FROM credentials c WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL` + filter
	queryArgs := append([]any{credID, workspaceID}, args...)

	var one int
	err := db.QueryRowContext(ctx, query, queryArgs...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// L1 — the workspace reveal switch
// ---------------------------------------------------------------------------

type revealPolicyResponse struct {
	WorkspaceID string `json:"workspace_id"`
	Enabled     bool   `json:"enabled"`
}

type revealPolicyRequest struct {
	Enabled *bool `json:"enabled"`
}

// GetPolicy reports whether reveal is enabled for the caller's workspace.
// GET /api/v1/credentials/reveal-policy
//
// MANAGER+ only. A MANAGER reads it because they have to know the rules they
// work under (§2.6 "Kde se to konfiguruje"); MEMBER and VIEWER do not see it
// at all, because "this tenant has reveal enabled" is target-selection
// information.
func (h *CredentialRevealHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := WorkspaceIDFromContext(ctx)
	role := RoleFromContext(ctx)
	callerID := ""
	if u := UserFromContext(ctx); u != nil {
		callerID = u.ID
	}
	if !requireRoleOrForbid(w, h.logger, callerID, role,
		"credential.reveal_policy.read", "workspace:"+workspaceID, "update") {
		return
	}
	enabled, err := revealEnabledForWorkspace(ctx, h.db, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "reveal policy: read", err)
		return
	}
	writeJSON(w, http.StatusOK, revealPolicyResponse{WorkspaceID: workspaceID, Enabled: enabled})
}

// SetPolicy turns the workspace reveal switch on or off.
// PUT /api/v1/credentials/reveal-policy
//
// OWNER only. §2.6 L1 says "until an OWNER turns it on"; the same section's
// Settings table lists OWNER and ADMIN as editors of that screen. This takes
// the narrow reading: the switch decides whether the tenant has a reveal
// surface at all, an ADMIN who genuinely needs it can be made an OWNER
// deliberately, and widening a permission later is a one-line change while
// narrowing one people have come to rely on is not.
//
// The change is journaled as a PRECONDITION, same as a reveal. An attacker
// who can wedge the audit chain must not be able to open a tenant's reveal
// surface unobserved.
func (h *CredentialRevealHandler) SetPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := WorkspaceIDFromContext(ctx)
	role := RoleFromContext(ctx)
	user := UserFromContext(ctx)
	callerID := ""
	if user != nil {
		callerID = user.ID
	}

	// Not gated on the interactive-session check: this is a settings write,
	// not a disclosure, and an operator automating tenant provisioning from
	// a CLI token has a legitimate reason to set it. What it cannot do is
	// reveal anything — that gate stands on its own.
	if role != "OWNER" {
		if h.logger != nil {
			h.logger.Warn("rbac: access denied",
				"user_id", callerID, "role", role,
				"action", "credential.reveal_policy.write", "resource", "workspace:"+workspaceID)
		}
		replyError(w, http.StatusForbidden,
			"Only a workspace OWNER can change the credential reveal policy.")
		return
	}

	var body revealPolicyRequest
	if err := readJSON(r, &body); err != nil || body.Enabled == nil {
		replyError(w, http.StatusBadRequest, "Body must be {\"enabled\": true|false}")
		return
	}

	previous, err := revealEnabledForWorkspace(ctx, h.db, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "reveal policy: read current", err)
		return
	}

	// Audit first, then write. If the chained record fails, the switch does
	// not move — the same ordering, and the same reason, as the reveal.
	if _, err := h.emitChained(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryCredentialRevealPolicy,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorUser,
		ActorID:     callerID,
		Summary:     "Credential reveal policy changed for workspace " + workspaceID,
		Payload: map[string]any{
			"enabled":  *body.Enabled,
			"previous": previous,
			"ip":       clientIP(r),
		},
	}); err != nil {
		h.logger.Error("reveal policy: chained audit write failed — refusing the change",
			"workspace_id", workspaceID, "user_id", callerID, "error", err)
		replyError(w, http.StatusInternalServerError, "Change refused: the audit record could not be written.")
		return
	}

	enabled := 0
	if *body.Enabled {
		enabled = 1
	}
	if _, err := h.db.ExecContext(ctx,
		`UPDATE workspaces SET credential_reveal_enabled = ? WHERE id = ?`, enabled, workspaceID); err != nil {
		replyInternalError(w, h.logger, "reveal policy: write", err)
		return
	}
	writeJSON(w, http.StatusOK, revealPolicyResponse{WorkspaceID: workspaceID, Enabled: *body.Enabled})
}

// ---------------------------------------------------------------------------
// L0 — classification
// ---------------------------------------------------------------------------

type sensitivityRequest struct {
	Sensitivity string `json:"sensitivity"`
}

type sensitivityResponse struct {
	CredentialID string `json:"credential_id"`
	Sensitivity  string `json:"sensitivity"`
	Previous     string `json:"previous"`
}

// SetSensitivity changes a credential's classification.
// PUT /api/v1/credentials/{credentialId}/sensitivity
//
// The two directions are deliberately asymmetric (§2.6 L0: "raise at any
// time, lower only as an audited action"):
//
//   - RAISING is MANAGER+ and not journaled. It only ever removes reach —
//     after it, strictly fewer people can reveal strictly less. Ceremony
//     there buys nothing and costs something real: if tightening is
//     annoying, people stop tightening.
//   - LOWERING is OWNER/ADMIN and journaled as a precondition. It hands out
//     a key that did not exist a second earlier, and it is the cheap move
//     for an attacker holding an admin session — easier than defeating
//     SEALED head-on.
//
// §2.6 also calls lowering an "approved" action, i.e. four-eyes. That
// approver is deferred with the rest of L3.4; the audited half ships now.
func (h *CredentialRevealHandler) SetSensitivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(ctx)
	role := RoleFromContext(ctx)
	callerID := ""
	if u := UserFromContext(ctx); u != nil {
		callerID = u.ID
	}

	if !requireRoleOrForbid(w, h.logger, callerID, role,
		"credential.sensitivity", "credential:"+credID, "update") {
		return
	}

	var body sensitivityRequest
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if !validSensitivity(body.Sensitivity) {
		replyError(w, http.StatusBadRequest,
			"sensitivity must be one of "+strings.Join(AllSensitivities(), ", "))
		return
	}

	var current string
	err := h.db.QueryRowContext(ctx,
		`SELECT sensitivity FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credID, workspaceID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		// 404 for the same reason reveal does: never confirm that an id
		// exists in someone else's tenant.
		replyError(w, http.StatusNotFound, msgCredentialNotFound)
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "sensitivity: load credential", err)
		return
	}

	if current == body.Sensitivity {
		// No-op. Not journaled — a write that changes nothing is not an
		// event, and treating it as one would let anyone pad the audit log.
		writeJSON(w, http.StatusOK, sensitivityResponse{
			CredentialID: credID, Sensitivity: current, Previous: current,
		})
		return
	}

	lowering := sensitivityRank[body.Sensitivity] < sensitivityRank[current]
	if lowering {
		if !requireRoleOrForbid(w, h.logger, callerID, role,
			"credential.sensitivity.lower", "credential:"+credID, "manage") {
			return
		}
		if _, err := h.emitChained(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			Type:        journal.EntryCredentialSensitivityLowered,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorUser,
			ActorID:     callerID,
			Summary:     "Credential " + credID + " classification lowered from " + current + " to " + body.Sensitivity,
			Payload: map[string]any{
				"credential_id": credID,
				"from":          current,
				"to":            body.Sensitivity,
				"actor_role":    role,
				"ip":            clientIP(r),
			},
			Refs: map[string]any{"credential_id": credID},
		}); err != nil {
			h.logger.Error("sensitivity: chained audit write failed — refusing the change",
				"credential_id", credID, "workspace_id", workspaceID, "user_id", callerID, "error", err)
			replyError(w, http.StatusInternalServerError, "Change refused: the audit record could not be written.")
			return
		}
	}

	if _, err := h.db.ExecContext(ctx,
		`UPDATE credentials SET sensitivity = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`,
		body.Sensitivity, time.Now().UTC().Format(time.RFC3339), credID, workspaceID); err != nil {
		replyInternalError(w, h.logger, "sensitivity: write", err)
		return
	}

	writeJSON(w, http.StatusOK, sensitivityResponse{
		CredentialID: credID, Sensitivity: body.Sensitivity, Previous: current,
	})
}
