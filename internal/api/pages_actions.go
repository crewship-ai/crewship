package api

// Pages — action dispatch (docs/prd/pages.md §8b, the v1 slice §12 calls
// "actions, which is what the surface is for").
//
// ── The shape IS the security property ──────────────────────────────────────
//
//	POST /api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}
//	{"inputs": { … }}
//
// The body carries ONLY the collected inputs. The server resolves actionId
// against the STORED page spec and dispatches the routine named there. §8b.2:
// "a compromised client, an injected narrative panel, an agent — cannot name a
// routine at click time, because the wire format has no field for one." That is
// literally true here: dispatchRequest has one field, and it is a map validated
// against the action's own declaration. The allow-list is not a check somebody
// remembered to write; it is the only path that exists.
//
// ── 202, never a held connection ────────────────────────────────────────────
//
// POST …/pipelines/{slug}/run is synchronous and holds the connection for the
// whole run (pipelines_exec.go:193-224). A page button on a ten-minute routine
// would hang, and no frontend surface in this repo has a long-request strategy.
// So this endpoint ENQUEUES into pending_runs — the deferred path that already
// exists and already returns 202 (pipeline_deferred.go:92-98) — with fire_at =
// now, and PendingRunDispatcher fires it on its next tick. The receipt is a
// pending id, and the run that follows is watched through the surfaces §8b.4
// lists, all of which already exist.
//
// ── The idempotency question §8b.3 leaves open, closed ──────────────────────
//
// §8b.3 asks whether IdempotencyStore.LookupOrReserve compares PARAMETERS for a
// replayed key. It does not: the primary key is (workspace_id, pipeline_id,
// idempotency_key) and the stored value is a single run id
// (internal/pipeline/idempotency.go:117-150). A replayed key carrying different
// inputs would therefore resolve to the first run and the caller would be told
// its second, different click had succeeded. Stripe's rule is that this must be
// REJECTED, not silently accepted, and that is what happens here — implemented
// with the store as it is, no migration:
//
//	bare key  = page-action:<page>:<panel>:<action>:<client key>
//	            reserved with the value "fp_<sha256 of the RESOLVED inputs>"
//	full key  = bare key + ":" + <same fingerprint>
//	            reserved with the value of the pending id
//
// A replay whose inputs differ collides on the BARE key against a different
// fingerprint → 409. A replay whose inputs match collides on the FULL key and
// gets the original pending id back → one dispatch. The fingerprint is taken
// over the RESOLVED inputs — defaults filled, types coerced, fixed params
// applied — so "same parameters" is decided on what the routine will actually
// receive rather than on how the client happened to serialise it.
//
// ── Authorisation: seeing a panel is not operating it ───────────────────────
//
// Dispatching requires BOTH halves, and they refuse differently on purpose:
//
//  1. The caller must be able to SEE the panel — canSeePanel, i.e. membership of
//     the owning crew or a workspace manage role (§7.1 rule 2). A caller who
//     would receive a sealed placeholder gets 404, not 403: the action does not
//     exist for them, and a 403 would confirm that it exists for somebody else.
//     §11b decision 14 already refuses to leak a panel's contents; leaking its
//     action list through a status code would give it back.
//
//  2. The caller must hold what the ROUTINE itself requires. Running a routine
//     is MANAGER+ (pipelines_exec.go:71, `requireRole(w, r, "create")`) and only
//     an `active` routine is runnable (pipeline_governance.go:245). Both are
//     re-applied here. A page button that ran a routine its clicker could not
//     run directly would be a privilege-escalation path wearing a label, and the
//     way to not have one is to ask the same question the routine asks.
//
// Producer authority is deliberately NOT consulted: an action runs a routine, it
// does not write the panel. Those are different verbs on different objects
// (§7.1 rule 4 governs the second one).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// maxActionBodyBytes caps a dispatch body. An action collects at most
// MaxInputsPerAction fields, each bounded by MaxInputValueBytes; this is that
// budget with room for the JSON around it, and it is far below the 1 MiB the
// pipeline exec surface allows because a button is not an upload.
const maxActionBodyBytes = 64 * 1024

// actionRetryAfterSeconds is the Retry-After a 429 carries. Five seconds
// matches what the synchronous run path answers a concurrency rejection with
// (pipelines_exec.go:213), and matches PendingRunDispatcher's tick, so a client
// that honours it wakes up roughly when the queue has moved.
const actionRetryAfterSeconds = 5

// dispatchRequest is the ENTIRE wire format of a click.
//
// One field. There is no `routine`, no `pipeline`, no `verb`, no `params`, and
// none of them is coming — §8b.2 is a statement about this struct. A body that
// carries any of those names is decoded into nothing and the declared routine
// runs, because the field it would have to land in does not exist.
//
// `inputs` itself is strict rather than lenient: PanelAction.ResolveInputs
// refuses a key the action did not declare. Leniency belongs at the envelope
// (an older server tolerating a newer client's field), not inside the one map a
// caller controls.
type dispatchRequest struct {
	Inputs map[string]any `json:"inputs"`
}

// actionWire is one declared action as the API serves it. It is the spec's own
// shape minus nothing: everything on it was authored by a human editing the
// page, so there is nothing here to withhold from somebody already entitled to
// see the panel.
type actionWire struct {
	ID      string                    `json:"id"`
	Kind    string                    `json:"kind"`
	Label   string                    `json:"label"`
	Style   string                    `json:"style"`
	Confirm *pages.PanelActionConfirm `json:"confirm,omitempty"`
	Routine string                    `json:"routine,omitempty"`
	Params  map[string]any            `json:"params,omitempty"`
	Inputs  []actionInputWire         `json:"inputs,omitempty"`
	Target  []string                  `json:"target,omitempty"`
	Ref     *pages.PanelEntityRef     `json:"ref,omitempty"`
}

// actionInputWire is one collected parameter as the form renderer reads it.
// `type` is always populated — the client must never have to reproduce the
// default (§8b.4: one field switch, not two).
type actionInputWire struct {
	Name     string   `json:"name"`
	Label    string   `json:"label,omitempty"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
}

func actionToWire(a *pages.PanelAction) actionWire {
	out := actionWire{
		ID:      a.ID,
		Kind:    string(a.Kind),
		Label:   a.Label,
		Style:   string(a.EffectiveStyle()),
		Confirm: a.Confirm,
		Routine: a.Routine,
		Params:  a.Params,
		Target:  a.Target,
		Ref:     a.Ref,
	}
	for i := range a.Inputs {
		in := &a.Inputs[i]
		out.Inputs = append(out.Inputs, actionInputWire{
			Name:     in.Name,
			Label:    in.Label,
			Type:     in.EffectiveType(),
			Required: in.Required,
			Default:  in.Default,
			Options:  in.Options,
		})
	}
	return out
}

// dispatchReceipt is what a 202 carries.
//
// It names the routine that RAN, which is not redundant: the whole point of
// §8b.2 is that the server chose it, so the receipt is where the caller finds
// out what its click actually did.
type dispatchReceipt struct {
	Status    string `json:"status"` // SCHEDULED | DEDUPED
	PendingID string `json:"pending_id"`
	FireAt    string `json:"fire_at,omitempty"`
	Deduped   bool   `json:"deduped"`
	Coalesced bool   `json:"coalesced"`
	Page      string `json:"page"`
	Panel     string `json:"panel"`
	Action    string `json:"action"`
	Routine   string `json:"routine"`
}

// ── Resolution ─────────────────────────────────────────────────────────────

// resolvedAction is everything the dispatch path needed to look up, resolved
// once so no later step can re-derive it differently.
type resolvedAction struct {
	page   *pageRecord
	panel  *panelRecord
	spec   *pages.PanelSpec
	action *pages.PanelAction
}

// errActionNotVisible is the "as far as you are concerned this does not exist"
// outcome. It is deliberately indistinguishable at the wire from a panel id
// that was never authored.
var errActionNotVisible = errors.New("pages: panel not visible to this caller")

// resolvePanelForCaller loads the page and the named panel, and refuses with
// errActionNotVisible when the caller may not see it.
//
// sql.ErrNoRows means "no such page or panel". Both that and
// errActionNotVisible become the same 404 at the handler, which is the point:
// the two are not distinguishable from outside, so a sealed panel cannot be
// enumerated by watching status codes.
func (h *PageHandler) resolvePanelForCaller(ctx context.Context, wsID, userID, slug, panelID string) (*pageRecord, *panelRecord, error) {
	rec, err := h.loadPage(ctx, wsID, slug)
	if err != nil {
		return nil, nil, err
	}
	panels, err := h.loadPanels(ctx, wsID, rec.ID)
	if err != nil {
		return nil, nil, err
	}
	var panel *panelRecord
	for _, p := range panels {
		if p.PanelID == panelID {
			panel = p
			break
		}
	}
	if panel == nil {
		return nil, nil, sql.ErrNoRows
	}
	viewer, err := h.loadViewer(ctx, wsID, userID)
	if err != nil {
		return nil, nil, err
	}
	if !h.canSeePanel(viewer, panel) {
		return nil, nil, errActionNotVisible
	}
	return rec, panel, nil
}

// storedPanelSpec reads the panel's declaration out of pages.spec_json.
//
// The STORED spec, never the page_panels row and never the request: §8b.2 says
// the allow-list is what the author saved, and page_panels does not carry the
// action list at all. There is exactly one reader of it (this function) so that
// "resolve the click against the stored spec" has one implementation.
func (h *PageHandler) storedPanelSpec(ctx context.Context, pageID, panelID string) (*pages.PanelSpec, error) {
	var specJSON string
	if err := h.db.QueryRowContext(ctx, `SELECT spec_json FROM pages WHERE id = ?`, pageID).Scan(&specJSON); err != nil {
		return nil, err
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(specJSON), &doc); err != nil {
		return nil, fmt.Errorf("stored page spec does not decode: %w", err)
	}
	spec, ok := doc.FindPanel(panelID)
	if !ok {
		return nil, sql.ErrNoRows
	}
	return spec, nil
}

// ── 1. List — GET /api/v1/pages/{slug}/panels/{panelId}/actions ────────────

// ListPanelActions returns the actions a panel declares.
//
// It exists so a client never has to guess what a panel offers, and so the CLI
// can show an operator the ids they may dispatch. It reads the same stored spec
// the dispatch endpoint resolves against — a listing that came from anywhere
// else could offer a button the dispatcher would then refuse.
func (h *PageHandler) ListPanelActions(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug, panelID := r.PathValue("slug"), r.PathValue("panelId")

	rec, _, err := h.resolvePanelForCaller(r.Context(), wsID, user.ID, slug, panelID)
	if err != nil {
		h.replyPanelLookupError(w, err, slug, panelID)
		return
	}
	spec, err := h.storedPanelSpec(r.Context(), rec.ID, panelID)
	if err != nil {
		h.replyPanelLookupError(w, err, slug, panelID)
		return
	}
	out := make([]actionWire, 0, len(spec.Actions))
	for i := range spec.Actions {
		out = append(out, actionToWire(&spec.Actions[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":    rec.Slug,
		"panel":   panelID,
		"actions": out,
	})
}

// replyPanelLookupError collapses "no such page", "no such panel" and "not
// yours to see" onto one 404. See resolvePanelForCaller.
func (h *PageHandler) replyPanelLookupError(w http.ResponseWriter, err error, slug, panelID string) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, errActionNotVisible) {
		replyError(w, http.StatusNotFound, fmt.Sprintf("no panel %q on page %q", panelID, slug))
		return
	}
	replyInternalError(w, h.logger, "resolve page panel", err)
}

// ── 2. Dispatch ────────────────────────────────────────────────────────────

// DispatchAction runs the routine the named action declares.
// POST /api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}
//
// Returns 202 with a pending id. Never 200, never the run's result, never a
// held connection.
func (h *PageHandler) DispatchAction(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "db not wired")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug, panelID, actionID := r.PathValue("slug"), r.PathValue("panelId"), r.PathValue("actionId")

	res, ok := h.resolveDispatch(w, r, wsID, user, slug, panelID, actionID)
	if !ok {
		return
	}

	// The routine, read from the SPEC. This is the line §8b.2 is about.
	routineSlug := res.action.Routine
	pipelineID, pipelineSlug, status, err := h.lookupActionRoutine(r.Context(), wsID, routineSlug)
	if errors.Is(err, sql.ErrNoRows) {
		// The spec named it and the authoring gate resolved it, so this is
		// §10b.4's ground moving under a stored page rather than a bad spec.
		// 409, not 404: the ACTION exists, its target no longer does.
		replyError(w, http.StatusConflict, fmt.Sprintf(
			"action %q runs routine/%s, which no longer exists in this workspace", actionID, routineSlug))
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve page action routine", err)
		return
	}
	if !h.gateActionRoutineStatus(w, routineSlug, status) {
		return
	}

	inputs, ok := h.collectActionInputs(w, r, res.action)
	if !ok {
		return
	}

	// Idempotency before the in-flight gate, deliberately: a genuine replay of a
	// click that is still running must get its original receipt back, not the
	// 429 that a SECOND, different click would correctly get.
	keys, receipt, done := h.actionIdempotency(w, r, wsID, pipelineID, res, inputs)
	if done {
		return
	}
	if receipt != nil {
		writeJSON(w, http.StatusAccepted, *receipt)
		return
	}

	// "Already running" (§8b.3's concurrency_key answer, at enqueue time). The
	// synchronous run path learns this from the in-process RunRegistry when the
	// executor acquires a slot; an enqueueing path never reaches that, so the
	// question is asked of the queue instead — and the same partial unique index
	// on (pipeline_id, debounce_key) that backs the check also makes a genuinely
	// concurrent pair coalesce rather than fire twice.
	debounceKey := pageActionDebounceKey(res.page.ID, panelID, actionID)
	if pendingID, busy, err := h.actionInFlight(r.Context(), pipelineID, debounceKey); err != nil {
		replyInternalError(w, h.logger, "check page action in flight", err)
		h.forgetActionKeys(r.Context(), wsID, pipelineID, keys)
		return
	} else if busy {
		h.forgetActionKeys(r.Context(), wsID, pipelineID, keys)
		w.Header().Set("Retry-After", fmt.Sprintf("%d", actionRetryAfterSeconds))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":               "this action is already running",
			"reason":              "another dispatch of this action is still queued or in flight",
			"pending_id":          pendingID,
			"retry_after_seconds": actionRetryAfterSeconds,
		})
		return
	}

	now := h.evaluator().Now().UTC()
	inputsJSON, err := json.Marshal(inputs)
	if err != nil {
		replyInternalError(w, h.logger, "marshal page action inputs", err)
		h.forgetActionKeys(r.Context(), wsID, pipelineID, keys)
		return
	}
	pendingID, coalesced, err := pipeline.NewPendingRunStore(h.db).Enqueue(r.Context(), pipeline.PendingRun{
		ID:           keys.pendingID,
		WorkspaceID:  wsID,
		PipelineID:   pipelineID,
		PipelineSlug: pipelineSlug,
		InputsJSON:   string(inputsJSON),
		MetadataJSON: marshalMetadata(map[string]any{
			"source":     "page_action",
			"page":       res.page.Slug,
			"page_id":    res.page.ID,
			"panel":      panelID,
			"action":     actionID,
			"clicked_by": user.ID,
		}),
		DebounceKey: debounceKey,
		// Now, not a window: a button is not a debounce. The key is here for the
		// uniqueness the index gives it, and fire_at says "on the next tick".
		FireAt:         now,
		InvokingUserID: user.ID,
		TriggeredVia:   pipeline.TriggeredViaManual,
	})
	if err != nil {
		h.forgetActionKeys(r.Context(), wsID, pipelineID, keys)
		replyInternalError(w, h.logger, "enqueue page action run", err)
		return
	}
	if coalesced && pendingID != keys.pendingID {
		// A concurrent identical click won the insert. Its receipt is the honest
		// one, but our reservation points at an id that was never used, so drop
		// the reservation rather than leave a replay resolving to nothing.
		h.forgetActionKeys(r.Context(), wsID, pipelineID, keys)
	}

	h.journalActionDispatch(r.Context(), wsID, user, res, routineSlug, pendingID)
	broadcastWorkspaceEvent(h.hub, wsID, "page.action.dispatched", map[string]any{
		"page_id": res.page.ID, "slug": res.page.Slug,
		"panel": panelID, "action": actionID, "pending_id": pendingID,
	})
	writeJSON(w, http.StatusAccepted, dispatchReceipt{
		Status:    "SCHEDULED",
		PendingID: pendingID,
		FireAt:    now.Format(time.RFC3339Nano),
		Coalesced: coalesced,
		Page:      res.page.Slug,
		Panel:     panelID,
		Action:    actionID,
		Routine:   routineSlug,
	})
}

// resolveDispatch runs every gate that decides whether this caller may dispatch
// this action, in the order the doc comment argues for. It writes the refusal
// itself and reports whether the caller may proceed.
func (h *PageHandler) resolveDispatch(w http.ResponseWriter, r *http.Request,
	wsID string, user *AuthUser, slug, panelID, actionID string) (*resolvedAction, bool) {

	rec, panel, err := h.resolvePanelForCaller(r.Context(), wsID, user.ID, slug, panelID)
	if err != nil {
		h.replyPanelLookupError(w, err, slug, panelID)
		return nil, false
	}
	spec, err := h.storedPanelSpec(r.Context(), rec.ID, panelID)
	if err != nil {
		h.replyPanelLookupError(w, err, slug, panelID)
		return nil, false
	}
	action, ok := spec.FindAction(actionID)
	if !ok {
		// 404, not 403. An action id that is not in the stored spec does not
		// exist for this panel — there is no thing to be forbidden from, and a
		// 403 would imply there is.
		replyError(w, http.StatusNotFound, fmt.Sprintf(
			"panel %q on page %q declares no action %q", panelID, slug, actionID))
		return nil, false
	}
	if action.Kind != pages.ActionCall {
		// A link navigates, a toggle is local client state, a custom action
		// resolves to a handler in our own client. None of them reaches the
		// server, and dispatching one as a call is the client asking the server
		// to do something the vocabulary says it does not do (§8b.1).
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"action %q is a %s, not a call; a %s never reaches the server and cannot be dispatched",
			actionID, action.Kind, action.Kind))
		return nil, false
	}
	// Half two of the authorisation rule: what the routine itself requires.
	// Re-applied in the handler as well as in the middleware because the
	// middleware is not what a handler test exercises, and a gate that only one
	// of the two paths enforces is a gate that can be removed by accident.
	if !requireRole(w, r, "create") {
		return nil, false
	}
	return &resolvedAction{page: rec, panel: panel, spec: spec, action: action}, true
}

// lookupActionRoutine resolves the routine slug the SPEC names.
func (h *PageHandler) lookupActionRoutine(ctx context.Context, wsID, routineSlug string) (id, slug, status string, err error) {
	err = h.db.QueryRowContext(ctx, `
		SELECT id, slug, COALESCE(status, '')
		FROM pipelines
		WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, routineSlug).Scan(&id, &slug, &status)
	return id, slug, status, err
}

// gateActionRoutineStatus mirrors PipelineHandler.gateRoutineStatus
// (pipeline_governance.go:245): a proposed routine is awaiting approval and a
// disabled one is an admin's airbag, and a page button is not a way around
// either. Reports whether the dispatch may proceed.
func (h *PageHandler) gateActionRoutineStatus(w http.ResponseWriter, routineSlug, status string) bool {
	switch status {
	case "", "active":
		return true
	case "proposed":
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":   "routine is awaiting approval",
			"routine": routineSlug,
			"status":  "proposed",
			"hint":    "a MANAGER must approve this routine before a page button can run it",
		})
	case "disabled":
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":   "routine is disabled",
			"routine": routineSlug,
			"status":  "disabled",
			"hint":    "an OWNER or ADMIN must re-enable this routine before a page button can run it",
		})
	default:
		// Fail closed, same as the run path: a future status must not become
		// runnable by omission.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":   "routine is not active",
			"routine": routineSlug,
			"status":  status,
		})
	}
	return false
}

// collectActionInputs reads the body and validates it against the action's own
// declaration. Writes the refusal itself.
func (h *PageHandler) collectActionInputs(w http.ResponseWriter, r *http.Request, action *pages.PanelAction) (map[string]any, bool) {
	var body dispatchRequest
	if r.ContentLength != 0 {
		raw, ok := readCapped(w, r, maxActionBodyBytes, "action body")
		if !ok {
			return nil, false
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				replyError(w, http.StatusBadRequest, "invalid JSON body")
				return nil, false
			}
		}
	}
	inputs, err := action.ResolveInputs(body.Inputs)
	if err != nil {
		var ve *pages.ValidationError
		if errors.As(err, &ve) {
			replyError(w, http.StatusBadRequest, ve.Detail)
			return nil, false
		}
		replyError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return inputs, true
}

// ── Idempotency ────────────────────────────────────────────────────────────

// actionKeys are the two reservations one click makes. Empty when the caller
// sent no Idempotency-Key, in which case nothing is reserved and nothing is
// forgotten — the in-flight gate is then the only double-click protection, and
// it is enough for a button.
type actionKeys struct {
	bare      string
	full      string
	pendingID string
}

func (k actionKeys) enabled() bool { return k.bare != "" }

// actionIdempotency applies the two-key construction described at the top of
// this file.
//
// Returns (keys, receipt, done): a non-nil receipt is a replay the caller should
// be handed as-is; done means a refusal has already been written.
func (h *PageHandler) actionIdempotency(w http.ResponseWriter, r *http.Request,
	wsID, pipelineID string, res *resolvedAction, inputs map[string]any) (actionKeys, *dispatchReceipt, bool) {

	keys := actionKeys{pendingID: "pnd_" + generateCUID()}
	clientKey := r.Header.Get("Idempotency-Key")
	if clientKey == "" {
		return keys, nil, false
	}
	fingerprint := inputsFingerprint(inputs)
	keys.bare = fmt.Sprintf("page-action:%s:%s:%s:%s", res.page.ID, res.panel.PanelID, res.action.ID, clientKey)
	keys.full = keys.bare + ":" + fingerprint

	store := pipeline.NewIdempotencyStore(h.db)
	ttl := pipeline.DefaultIdempotencyTTL

	// The Stripe rule. The bare key holds the fingerprint of whatever inputs
	// this key was first used with; a second click reusing it with different
	// inputs is a client bug or a replay attack, and both want to be told.
	storedFP, isNew, err := store.LookupOrReserve(r.Context(), wsID, keys.bare, "fp_"+fingerprint, pipelineID, ttl)
	if err != nil {
		replyInternalError(w, h.logger, "reserve page action idempotency key", err)
		return keys, nil, true
	}
	if !isNew && storedFP != "fp_"+fingerprint {
		replyError(w, http.StatusConflict,
			"this Idempotency-Key was already used for this action with different inputs; "+
				"a replayed key must carry the same parameters, so use a fresh key for a different click")
		return keys, nil, true
	}

	// The full key is the dedupe proper: same key AND same inputs resolves to
	// the pending id the first click reserved.
	resolvedID, isNew, err := store.LookupOrReserve(r.Context(), wsID, keys.full, keys.pendingID, pipelineID, ttl)
	if err != nil {
		replyInternalError(w, h.logger, "reserve page action idempotency key", err)
		return keys, nil, true
	}
	if !isNew {
		return keys, &dispatchReceipt{
			Status:    "DEDUPED",
			PendingID: resolvedID,
			Deduped:   true,
			Page:      res.page.Slug,
			Panel:     res.panel.PanelID,
			Action:    res.action.ID,
			Routine:   res.action.Routine,
		}, false
	}
	return keys, nil, false
}

// forgetActionKeys releases both reservations so a legitimate retry after a
// refusal is treated as a fresh request rather than resolving to a dispatch
// that never happened. Same reason IdempotencyStore.Forget exists for the 429
// on the synchronous path.
func (h *PageHandler) forgetActionKeys(ctx context.Context, wsID, pipelineID string, keys actionKeys) {
	if !keys.enabled() {
		return
	}
	store := pipeline.NewIdempotencyStore(h.db)
	for _, k := range []string{keys.full, keys.bare} {
		if err := store.Forget(ctx, wsID, pipelineID, k); err != nil && h.logger != nil {
			h.logger.Warn("pages: idempotency key not released", "error", err)
		}
	}
}

// inputsFingerprint hashes the RESOLVED inputs.
//
// encoding/json sorts map keys, so the same map always produces the same bytes
// regardless of the order the client sent them in — which is what makes "same
// parameters" a property of the values rather than of the serialisation. A
// marshal error is impossible for a map that came out of ResolveInputs (its
// values are strings, numbers, bools and the author's own params, which round-
// tripped through JSON to get into the spec), and if it somehow happens the
// fingerprint falls back to a constant that simply makes the key coarser rather
// than failing a click.
func inputsFingerprint(inputs map[string]any) string {
	b, err := json.Marshal(inputs)
	if err != nil {
		b = []byte("{}")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// ── The queue ──────────────────────────────────────────────────────────────

// pageActionDebounceKey identifies one action's slot in pending_runs. It is
// derived entirely server-side from the page id, the panel id and the action id
// — a caller cannot choose it and therefore cannot collide with, or coalesce
// into, somebody else's dispatch.
func pageActionDebounceKey(pageID, panelID, actionID string) string {
	return "page-action:" + pageID + ":" + panelID + ":" + actionID
}

// actionInFlight reports whether a dispatch of this action is still queued.
func (h *PageHandler) actionInFlight(ctx context.Context, pipelineID, debounceKey string) (string, bool, error) {
	var pendingID string
	err := h.db.QueryRowContext(ctx, `
		SELECT id FROM pending_runs
		WHERE pipeline_id = ? AND debounce_key = ? AND status = 'pending'`, pipelineID, debounceKey).Scan(&pendingID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return pendingID, true, nil
}

// ── Audit ──────────────────────────────────────────────────────────────────

// journalActionDispatch records who ran what. Best-effort with respect to the
// response — the run is queued whether or not the journal accepted the entry —
// but a failure is logged, for the same reason the grant path logs one: an
// action nobody can audit is not a control.
func (h *PageHandler) journalActionDispatch(ctx context.Context, wsID string, actor *AuthUser,
	res *resolvedAction, routineSlug, pendingID string) {
	if h.journal == nil {
		return
	}
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		Type:        journal.EntryPageActionDispatched,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actor.ID,
		Summary: fmt.Sprintf("%s ran %s on %s/%s (routine/%s)",
			actor.Email, res.action.Label, res.page.Slug, res.panel.PanelID, routineSlug),
		Payload: map[string]any{
			"page":          res.page.Slug,
			"page_id":       res.page.ID,
			"panel":         res.panel.PanelID,
			"action":        res.action.ID,
			"routine":       routineSlug,
			"pending_id":    pendingID,
			"actor_user_id": actor.ID,
		},
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: action dispatch was not journalled",
			"page", res.page.Slug, "action", res.action.ID, "error", err)
	}
}

// ── The authoring gate's action half (§10b.1) ──────────────────────────────

// resolveActionRoutines is the second half of the authoring gate applied to
// actions: every routine a `call` names must EXIST, exactly as every declared
// producer must (resolveReferences in pages_handler.go, which calls this).
//
// Without it a page saves clean and its buttons 409 on the first click, which is
// the failure §10b.1 exists to prevent — "the page would render a grid of dead
// panels and nobody would know why", with a button the same argument is
// stronger because the operator only finds out mid-incident.
//
// Reports whether the document may be stored; writes the refusal itself.
func (h *PageHandler) resolveActionRoutines(w http.ResponseWriter, r *http.Request, wsID string, doc *pages.Document) bool {
	for i := range doc.Spec.Panels {
		p := &doc.Spec.Panels[i]
		for j := range p.Actions {
			a := &p.Actions[j]
			if a.Kind != pages.ActionCall {
				continue
			}
			var one int
			err := h.db.QueryRowContext(r.Context(),
				`SELECT 1 FROM pipelines WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`,
				wsID, a.Routine).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				replyError(w, http.StatusBadRequest, fmt.Sprintf(
					"panel %q action %q runs routine/%s, and no such routine exists here — "+
						"the spec is the allow-list a click resolves against (§8b.2), so it cannot name "+
						"a routine nobody answers to", p.ID, a.ID, a.Routine))
				return false
			}
			if err != nil {
				replyInternalError(w, h.logger, "resolve page action routine", err)
				return false
			}
		}
	}
	return true
}
