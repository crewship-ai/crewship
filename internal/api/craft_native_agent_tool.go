package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type craftNativeAgentToolRequest struct {
	Operation   string          `json:"operation"`
	Generation  string          `json:"generation,omitempty"`
	BaseVersion string          `json:"base_version,omitempty"`
	RequestID   string          `json:"request_id,omitempty"`
	Commands    json.RawMessage `json:"commands,omitempty"`
	Query       json.RawMessage `json:"query,omitempty"`
	Summary     string          `json:"summary,omitempty"`
}
type craftNativeAgentToolAttempt struct {
	input             [32]byte
	lease             mooring.EditLease
	base              int64
	hash              string
	revision          string
	attempted         bool
	receipt           *mooring.NativeSaveReceipt
	isProposal        bool
	proposalID        string
	proposalCandidate []byte // retained only while this proposal's commit is unknown
	proposalErr       error
	undoable          bool // the server kept what this change replaced
	// signatureInvalidated: the workbook's XML package signature was kept but no longer matches.
	signatureInvalidated bool
}

const maxNativeAgentChangeRequests = 64

var errNativeAgentChangeRequestLimit = errors.New("native agent change request limit reached")

type craftNativeAgentToolState struct {
	events           []craftNativeAgentToolEvent
	lastReceipt      *mooring.NativeSaveReceipt
	uncertain        bool
	uncertainAttempt *craftNativeAgentToolAttempt
	currentAttempt   *craftNativeAgentToolAttempt
	lease            mooring.EditLease
	requests         map[string]*craftNativeAgentToolAttempt
	// Proposals share requests with applies. Stored proposal keys are bound to
	// this authenticated run, including a replay after an uncertain commit.
	proposals map[string]string
	proposal  *craftNativeAgentProposalRecord
}

// craftNativeAgentProposalRecord binds a proposal to the run session that
// recorded it, the way a save receipt carries its session.
type craftNativeAgentProposalRecord struct{ ID, SessionID string }

// craftNativeAgentProposalOutcome copies this run's latest own proposal. Like
// craftNativeAgentCompletion it reports uncertainty instead of waiting for an
// in-flight tool call.
func craftNativeAgentProposalOutcome(e *craftNativeAgentEntry) (*craftNativeAgentProposalRecord, bool) {
	if e == nil || !e.callMu.TryLock() {
		return nil, true
	}
	defer e.callMu.Unlock()
	if e.tool == nil || e.tool.proposal == nil {
		return nil, false
	}
	copy := *e.tool.proposal
	return &copy, false
}

// The store namespaces request keys by the authenticated run session. Both a
// new result and its durable replay therefore belong to this run, including
// recovery of an unknown proposal commit.
func rememberNativeAgentProposal(e *craftNativeAgentEntry, requestID string, p mooring.NativeProposal) {
	if p.ID == "" {
		return
	}
	if e.tool.proposals == nil {
		e.tool.proposals = make(map[string]string)
	}
	e.tool.proposals[requestID] = p.ID
	e.tool.proposal = &craftNativeAgentProposalRecord{ID: p.ID, SessionID: craftNativeAgentPrincipal(e).SessionID}
}

// Runtime completion observes a copy, never mutates tool authority or receipts.
func craftNativeAgentCompletion(e *craftNativeAgentEntry) (*mooring.NativeSaveReceipt, bool, mooring.EditLease) {
	// Provider exit does not prove an already admitted HTTP apply has settled.
	// Report uncertainty immediately rather than waiting beyond the runtime's
	// inspection context. Its separate SQL retirement fence still stops writes.
	if e == nil || !e.callMu.TryLock() {
		return nil, true, mooring.EditLease{}
	}
	defer e.callMu.Unlock()
	if e.tool == nil {
		return nil, false, mooring.EditLease{}
	}
	var receipt *mooring.NativeSaveReceipt
	if e.tool.lastReceipt != nil && (e.tool.currentAttempt == nil || e.tool.currentAttempt.receipt != nil || e.tool.currentAttempt.isProposal) {
		copy := *e.tool.lastReceipt
		receipt = &copy
	}
	return receipt, e.tool.uncertain, e.tool.lease
}

func parseCraftNativeAgentTool(raw []byte) (craftNativeAgentToolRequest, error) {
	var out craftNativeAgentToolRequest
	if len(raw) == 0 || len(raw) > 128<<10 {
		return out, mooring.ErrInvalid
	}
	canonical, err := mooring.CanonicalJSON(raw)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(canonical, &fields) != nil || fields == nil || json.Unmarshal(canonical, &out) != nil {
		return out, mooring.ErrInvalid
	}
	allowed := map[string]bool{"operation": true}
	switch out.Operation {
	case "read":
		allowed["query"] = true
	case "acquire":
	case "renew", "release":
		allowed["generation"] = true
	case "apply":
		for _, key := range []string{"generation", "base_version", "request_id", "commands"} {
			allowed[key] = true
		}
	case "propose":
		for _, key := range []string{"base_version", "request_id", "commands", "summary"} {
			allowed[key] = true
		}
	default:
		return out, mooring.ErrInvalid
	}
	for key := range fields {
		if !allowed[key] {
			return out, mooring.ErrInvalid
		}
	}
	if out.Operation == "renew" || out.Operation == "release" || out.Operation == "apply" {
		if _, err = craftNativeGeneration(out.Generation); err != nil {
			return out, mooring.ErrInvalid
		}
	}
	if out.Operation == "apply" || out.Operation == "propose" {
		if _, err = craftNativeVersion(out.BaseVersion); err != nil || len(out.RequestID) == 0 || len(out.RequestID) > 128 {
			return out, mooring.ErrInvalid
		}
		for _, c := range out.RequestID {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:", c)) {
				return out, mooring.ErrInvalid
			}
		}
		var commands []json.RawMessage
		if len(out.Commands) > 64<<10 || json.Unmarshal(out.Commands, &commands) != nil || len(commands) == 0 || len(commands) > 64 {
			return out, mooring.ErrInvalid
		}
	}
	if len(out.Summary) > 500 || strings.ContainsRune(out.Summary, 0) {
		return out, mooring.ErrInvalid
	}
	if len(out.Query) > 0 && (len(out.Query) > 64<<10 || bytes.TrimSpace(out.Query)[0] != '{') {
		return out, mooring.ErrInvalid
	}
	return out, nil
}

func (m *craftNativeAgentManager) toolCall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if m == nil || !m.enabled || m.config.AgentApply == nil || m.config.AgentInspect == nil {
		replyError(w, 503, "native Craft agents unavailable")
		return
	}
	bound, e, err := m.bindTool(r)
	if err != nil {
		replyError(w, 403, "confirmed native Craft task required")
		return
	}
	m.serveToolCall(w, r, bound, e)
}

// serveToolCall runs one tool operation for an entry the caller has already
// bound to this request's run credential. The task path binds through
// bindTool; a chat run binds through its own scope (same operations, same
// fences, same lease and compare-and-swap publication).
func (m *craftNativeAgentManager) serveToolCall(w http.ResponseWriter, r *http.Request, bound *http.Request, e *craftNativeAgentEntry) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		replyError(w, 413, "native Craft request too large")
		return
	}
	input, err := parseCraftNativeAgentTool(raw)
	if err != nil {
		replyError(w, 400, "invalid native Craft operation")
		return
	}
	if op := craftAgentToolOperation(input.Operation); !m.human.apps.agentsAllow(r.Context(), e.grant.Application, op) {
		subject := craftapps.Agents
		if m.human.apps.mode(r.Context(), craftapps.Agents).Allows(op) {
			subject = e.grant.Application
		}
		m.human.apps.refuse(w, r.Context(), subject)
		return
	}
	e.callMu.Lock()
	defer func() {
		// Close/retirement never waits behind an engine conversion. Release
		// under mu so it cannot miss this final cleanup handoff.
		m.mu.Lock()
		if e.closed {
			clearNativeProposalCandidatesLocked(e)
		}
		e.callMu.Unlock()
		m.mu.Unlock()
	}()
	if e.tool == nil {
		e.tool = &craftNativeAgentToolState{requests: make(map[string]*craftNativeAgentToolAttempt)}
	}
	event := craftNativeAgentToolEvent{Operation: input.Operation}
	tracked := &nativeAgentStatusWriter{ResponseWriter: w}
	w = tracked
	defer func() {
		if tracked.status == 0 {
			// A panic or abandoned response is not a successful HTTP outcome.
			// If publication began, preserve that exact unknown attempt until
			// its original snapshot is reconciled; never permit a new edit.
			if attempt := e.tool.currentAttempt; attempt != nil && attempt.attempted && attempt.receipt == nil && attempt.proposalID == "" {
				e.tool.uncertain, e.tool.uncertainAttempt = true, attempt
			}
			return
		}
		recordNativeAgentToolEvent(e, input, event, tracked.status)
	}()
	if err = m.toolAuthority(bound.Context(), e); err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	qualified, ok := m.documents.resolveDocument(e.grant.Application, e.grant.Proof.pin)
	if !ok || qualified.profile.Application == "" {
		craftNativeDocumentError(w, missingNativeDocumentProfile(e.grant.Application))
		return
	}
	// Workbook Sheets are supported by the attachment-bound chat adapter.
	if qualified.application.Store.Format == mooring.NativeFormatWorkbook && e.chat == nil {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	profile := qualified.profile
	cfg := qualified.application.Store
	cfg.Enabled = m.human.enabled
	cfg.Leases = m.human.leases
	cfg.Authorize = m.human.authorizeNative
	store := mooring.NewNativeStore(m.human.pages.db, cfg)
	switch input.Operation {
	case "acquire":
		// A proposing task never needs the editor; holding it would only
		// lock the person out of their own shared document.
		if err = m.directMandate(bound.Context(), e); err != nil {
			craftNativeAgentMandateError(w, err)
			return
		}
		lease, err := m.human.leases.Acquire(bound.Context(), e.grant.Scope, craftNativeAgentPrincipal(e))
		if errors.Is(err, mooring.ErrEditLeaseBusy) && e.chat != nil && store.Workbook() {
			// The person whose own message consented holds the Sheet in their editor. The
			// agent writes beside their lease (a co-write grant), never changing it; any other
			// holder stays holder_human.
			if grant, coErr := m.human.leases.CoWrite(bound.Context(), e.grant.Scope, craftNativeAgentPrincipal(e), "user:"+e.chat.authority.HumanUserID); coErr == nil {
				lease, err = grant, nil
			}
		}
		if err != nil {
			if errors.Is(err, mooring.ErrEditLeaseBusy) {
				current, readErr := m.human.leases.Read(bound.Context(), e.grant.Scope, craftNativeAgentPrincipal(e))
				if readErr == nil {
					event.HolderKind = nativeAgentHolderKind(current)
					writeJSON(w, 409, map[string]string{"error": "document already has an editor", "holder_kind": nativeAgentHolderKind(current)})
					return
				}
			}
			craftNativeDocumentError(w, err)
			return
		}
		e.tool.lease = lease
		writeJSON(w, 200, nativeAgentLeaseResult(lease))
	case "renew", "release", "apply":
		// Q16 before the lease check, so a proposing task learns why rather
		// than seeing a stale lease. Reconciling an apply that was already
		// attempted (and renewing to do so) is exempt: it writes nothing, and
		// refusing it would leave an uncertain outcome unresolvable (#5579).
		if input.Operation != "release" && !nativeAgentReconciling(e, input) {
			if err = m.directMandate(bound.Context(), e); err != nil {
				craftNativeAgentMandateError(w, err)
				return
			}
		}
		generation, _ := craftNativeGeneration(input.Generation)
		lease := e.tool.lease
		if lease.Generation != generation || lease.Holder != craftNativeAgentPrincipal(e) || lease.Scope != e.grant.Scope {
			craftNativeDocumentError(w, mooring.ErrEditLeaseStale)
			return
		}
		if input.Operation == "release" {
			if err = m.human.leases.Release(bound.Context(), lease); err != nil {
				craftNativeDocumentError(w, err)
				return
			}
			e.tool.lease = mooring.EditLease{}
			writeJSON(w, 200, map[string]bool{"released": true})
			return
		}
		renewed, err := m.human.leases.RenewAgent(bound.Context(), lease)
		if errors.Is(err, mooring.ErrEditLeaseBusy) {
			// A co-write grant whose person no longer holds the Sheet: someone else edits it now.
			writeJSON(w, 409, map[string]string{"error": "document already has an editor", "holder_kind": "human"})
			return
		}
		if errors.Is(err, mooring.ErrEditLeaseExpired) {
			// The lease expired and another editor took the Sheet since: not a change of the Sheet.
			replyCode(w, 409, CraftSheetReasonLeaseExpired, "the edit lease expired and another editor took the document; acquire it again")
			return
		}
		if err != nil {
			craftNativeDocumentError(w, err)
			return
		}
		e.tool.lease = renewed
		if input.Operation == "renew" {
			writeJSON(w, 200, nativeAgentLeaseResult(renewed))
			return
		}
		var receipt mooring.NativeSaveReceipt
		applyCtx := bound.Context()
		if store.Workbook() && e.chat != nil {
			var cancel context.CancelFunc
			applyCtx, cancel = context.WithTimeout(applyCtx, craftNativeChatRunEngineWindow)
			defer cancel()
		}
		for retry := 0; retry < craftRealtimeRetries; retry++ {
			receipt, err = m.applyTool(applyCtx, e, store, profile, input)
			if !errors.Is(err, errCraftRealtimeAgentMoved) {
				break
			}
		}
		if err != nil {
			if errors.Is(err, errCraftNativeAgentProposalRequired) {
				craftNativeAgentMandateError(w, err)
			} else if errors.Is(err, errCraftNativeConsentStale) {
				replyChatSheetRefusal(w, http.StatusConflict, CraftSheetReasonConsentStale)
			} else if errors.Is(err, ErrCraftNativeSheetBusy) {
				replyNativeSheetBusy(w)
			} else if e.tool.uncertain {
				replyError(w, 503, "original native save outcome unconfirmed; reconcile the exact original request")
			} else if errors.Is(err, errNativeAgentChangeRequestLimit) {
				replyError(w, 429, "this run has reached its change request limit")
			} else if errors.Is(err, mooring.ErrEditLeaseBusy) {
				writeJSON(w, 409, map[string]string{"error": "document already has an editor", "holder_kind": "human"})
			} else if nativeAgentChangeRefused(w, err) {
			} else if errors.Is(err, ErrCraftNativeAgentEngine) {
				replyError(w, 422, "original native engine operation refused")
			} else {
				craftNativeDocumentError(w, err)
			}
			return
		}
		writeJSON(w, 200, receipt)
	case "propose":
		proposal, err := m.proposeTool(bound.Context(), e, store, profile, input)
		if err != nil {
			if e.tool.uncertain {
				replyError(w, 503, "original native change outcome unconfirmed; reconcile the exact original request")
			} else if errors.Is(err, errNativeAgentChangeRequestLimit) {
				replyError(w, 429, "this run has reached its change request limit")
			} else if nativeAgentChangeRefused(w, err) {
			} else if errors.Is(err, ErrCraftNativeAgentEngine) {
				replyError(w, 422, "original native engine operation refused")
			} else {
				craftNativeDocumentError(w, err)
			}
			return
		}
		writeJSON(w, 201, map[string]string{"proposal_id": proposal.ID, "base_version": strconv.FormatInt(proposal.BaseVersion, 10), "state": proposal.State, "note": "The document is unchanged. The person holding the editor will review, accept or reject this proposal."})
	case "read":
		state, err := store.Read(bound.Context(), e.grant.Scope)
		if err != nil {
			craftNativeDocumentError(w, err)
			return
		}
		result := map[string]any{"application": e.grant.Application, "version": strconv.FormatInt(state.LatestVersion, 10), "engine_pin": store.EnginePin()}
		var activity craftSheetPresenceHandle
		if store.Workbook() {
			activity = m.sheetPresence(bound.Context(), e, craftSheetPresenceReadRanges(input.Query))
			defer activity.end()
		}
		if len(input.Query) > 0 {
			if e.chat != nil && e.grant.Application == "gridcraft" {
				if _, err := nativeAgentEngineCommands("gridcraft", input.Query, true); err != nil {
					replyError(w, http.StatusBadRequest, craftSheetInvalidReadQuery)
					return
				}
			}
			native, err := nativeAgentDocumentBytes(state)
			if err != nil {
				craftNativeDocumentError(w, err)
				return
			}
			view, err := m.config.AgentInspect(bound.Context(), profile, native, input.Query)
			if errors.Is(err, ErrCraftNativeSheetBusy) {
				replyNativeSheetBusy(w)
				return
			}
			if err != nil || len(view) > 1<<20 || !json.Valid(view) {
				replyError(w, 422, "original native engine query refused")
				return
			}
			// The authorization is checked again after the external engine read.
			if err = m.toolAuthority(bound.Context(), e); err != nil {
				craftNativeDocumentError(w, err)
				return
			}
			activity.update(craftSheetPresenceResolved(view))
			result["view"] = view
		}
		writeJSON(w, 200, result)
	}
}

// directMandate refuses apply before anything is attempted, so the agent learns
// to propose instead without spending a request ID. The publishing transaction
// checks the same rule again (authorize).
func (m *craftNativeAgentManager) directMandate(ctx context.Context, e *craftNativeAgentEntry) error {
	tx, err := m.human.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return m.checkDirectMandate(ctx, tx, e)
}

// nativeAgentReconciling reports a call that only resolves an earlier,
// already attempted apply: the retry of that exact request, or a renew while
// its outcome is still uncertain.
func nativeAgentReconciling(e *craftNativeAgentEntry, input craftNativeAgentToolRequest) bool {
	if e.tool == nil {
		return false
	}
	switch input.Operation {
	case "apply":
		prior := e.tool.requests[input.RequestID]
		return prior != nil && prior.attempted
	case "renew":
		return e.tool.uncertain
	}
	return false
}

// craftNativeAgentMandateError gives the authorized agent a machine-readable
// reason, so it can switch to craft_propose over the same base version.
func craftNativeAgentMandateError(w http.ResponseWriter, err error) {
	if errors.Is(err, errCraftNativeAgentProposalRequired) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this task may only propose a change; call craft_propose over the same base_version", "reason": "proposal_required"})
		return
	}
	craftNativeDocumentError(w, err)
}

func (m *craftNativeAgentManager) toolAuthority(ctx context.Context, e *craftNativeAgentEntry) error {
	tx, err := m.human.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return m.checkRun(ctx, tx, e)
}
func nativeAgentHolderKind(lease mooring.EditLease) string {
	if strings.HasPrefix(lease.Holder.ActorID, "user:") {
		return "human"
	}
	if strings.HasPrefix(lease.Holder.ActorID, "agent:") {
		return "agent"
	}
	return "unknown"
}
func nativeAgentLeaseResult(lease mooring.EditLease) map[string]string {
	return map[string]string{"generation": strconv.FormatInt(lease.Generation, 10), "expires_at": lease.ExpiresAt.UTC().Format(time.RFC3339Nano), "holder_kind": "agent"}
}
func nativeAgentDocumentBytes(state artifacts.VersionState) ([]byte, error) {
	// A Sheet kept as its workbook file (mooring.NativeFormatWorkbook).
	for name, media := range map[string]string{"workbook.xlsx": mooring.WorkbookMediaTypeXLSX, "workbook.xlsm": mooring.WorkbookMediaTypeXLSM} {
		if file, ok := state.Manifest.Files[name]; ok {
			if len(state.Manifest.Files) != 1 || state.Manifest.EntryPoint != name || file.MediaType != media {
				return nil, mooring.ErrNativeProfile
			}
			return file.Content, nil
		}
	}
	file, ok := state.Manifest.Files["document.json"]
	if !ok || len(state.Manifest.Files) != 1 || state.Manifest.EntryPoint != "document.json" || file.MediaType != "application/json" {
		return nil, mooring.ErrNativeProfile
	}
	return file.Content, nil
}

func (m *craftNativeAgentManager) applyTool(ctx context.Context, e *craftNativeAgentEntry, store *mooring.NativeStore, profile CraftNativeDocumentProfile, input craftNativeAgentToolRequest) (mooring.NativeSaveReceipt, error) {
	var zero mooring.NativeSaveReceipt
	canonical, _ := json.Marshal(input)
	fingerprint := sha256.Sum256(canonical)
	base, _ := craftNativeVersion(input.BaseVersion)
	if prior := e.tool.requests[input.RequestID]; prior != nil {
		if prior.input != fingerprint {
			return zero, artifacts.ErrVersionConflict
		}
		if !prior.attempted {
			return zero, ErrCraftNativeAgentEngine
		}
		// A repeat of a no-op has no successor version to prove.
		if prior.receipt != nil && prior.receipt.Unchanged {
			return *prior.receipt, nil
		}
		receipt, err := store.ReconcileSnapshot(ctx, prior.lease, prior.base, prior.revision, prior.hash)
		if err == nil {
			if prior.receipt == nil {
				e.chat.rememberApply(input, receipt)
			}
			prior.receipt = &receipt
			prior.undoable = prior.undoable && !receipt.Unchanged
			rememberNativeAgentReceipt(e.tool, receipt)
			if e.tool.uncertainAttempt == prior {
				e.tool.uncertainAttempt = nil
				e.tool.uncertain = false
			}
		}
		return receipt, err
	}
	if e.tool.uncertain {
		return zero, mooring.ErrNativeUnconfirmed
	}
	// A co-write is replayed by the person's editor over their work: only commands it can replay.
	if e.tool.lease.CoWrite != (mooring.EditLeasePrincipal{}) {
		if _, replayable := craftNativeAgentResolvedOps(input.Commands); !replayable {
			return zero, mooring.ErrEditLeaseBusy
		}
	}
	if len(e.tool.requests) >= maxNativeAgentChangeRequests {
		return zero, errNativeAgentChangeRequestLimit
	}
	// Only a fresh publication is a direct write: the publishing transaction
	// re-checks the Q16 mandate (authorize). Reconciliation above writes
	// nothing and runs without the marker.
	ctx = context.WithValue(ctx, craftNativeAgentDirectWriteKey{}, true)
	state, err := store.Read(ctx, e.grant.Scope)
	if err != nil {
		return zero, err
	}
	if e.chat != nil {
		consented := e.chat.consentBase()
		moved, err := m.chatRunConsentMoved(ctx, e, consented, state.LatestVersion)
		if err != nil {
			return zero, err
		}
		if moved || base > state.LatestVersion {
			return zero, errCraftNativeConsentStale
		}
		if store.Workbook() && base >= consented {
			// The authorized intervening writes are human edits from this
			// chat. Run the original cell operations on the current head and
			// capture the inverse from that same head.
			base = state.LatestVersion
		}
	}
	if state.LatestVersion != base {
		return zero, artifacts.ErrVersionConflict
	}
	native, err := nativeAgentDocumentBytes(state)
	if err != nil {
		return zero, err
	}
	attempt := &craftNativeAgentToolAttempt{input: fingerprint, lease: e.tool.lease, base: base, revision: "native-agent:" + input.RequestID}
	e.tool.requests[input.RequestID] = attempt
	e.tool.currentAttempt = attempt
	// Renew only while this exact authenticated HTTP apply remains alive. The
	// normal 15s lease and every run/original-human fence remain unchanged.
	window := 65 * time.Second
	if e.chat != nil {
		window = craftNativeChatRunEngineWindow
	}
	renewal, cancel := context.WithTimeout(ctx, window)
	active := renewal
	done := make(chan struct{})
	finish := beginBackgroundWork()
	go func() {
		defer close(done)
		defer finish()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-renewal.Done():
				return
			case <-ticker.C:
				if _, err := m.human.leases.RenewAgent(renewal, attempt.lease); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	limit := nativeAgentCandidateLimit(store)
	upload, err := store.ReserveUpload(active, e.grant.Scope, craftNativeReservation(int64(limit)))
	if err != nil {
		// Refused before the engine ran: nothing was attempted, so the same
		// request ID must stay retryable.
		forgetNativeAgentAttempt(e, input.RequestID, attempt)
		return zero, err
	}
	defer upload.Close()
	active = upload.Context(active)
	record := craftNativeAgentSheetRecord(e)
	undoable := store.Workbook()
	if undoable {
		defer m.sheetPresence(active, e, craftSheetPresenceWriteRanges(input.Commands)).end()
		ops, err := resolveCraftWorkbookOps(input.Commands)
		if err != nil {
			forgetNativeAgentAttempt(e, input.RequestID, attempt)
			return zero, err
		}
		inverse, complete := craftUndoCapture(active, m.config.AgentInspect, profile, native, ops)
		if !complete || !craftVersionOpsFit(ops, inverse) || record == nil {
			forgetNativeAgentAttempt(e, input.RequestID, attempt)
			return zero, errCraftNativeAgentUndoUnavailable
		}
		record = craftNativeAgentWorkbookRecord(e, store, base, input.RequestID, ops, inverse, record)
	}
	candidate, err := m.config.AgentApply(active, profile, native, input.Commands)
	if err != nil {
		if errors.Is(err, ErrCraftNativeSheetBusy) {
			// Nothing ran: the same request ID stays retryable.
			forgetNativeAgentAttempt(e, input.RequestID, attempt)
		}
		return zero, err
	}
	if len(candidate) == 0 || len(candidate) > limit {
		return zero, ErrCraftNativeAgentEngine
	}
	sum := sha256.Sum256(candidate)
	attempt.hash = "sha256:" + hex.EncodeToString(sum[:])
	attempt.attempted = true
	// There is one publication attempt. Any repeat reconciles this exact saved
	// snapshot; it never reruns the original engine or advances the base.
	attempt.undoable = undoable
	receipt, err := store.SaveAttemptRecorded(active, attempt.lease, base, attempt.revision, attempt.hash, candidate, "", record)
	if errors.Is(err, artifacts.ErrVersionConflict) && store.Workbook() && e.chat != nil {
		forgetNativeAgentAttempt(e, input.RequestID, attempt)
		return zero, errCraftRealtimeAgentMoved
	}
	if err != nil {
		e.tool.uncertain = !knownNativeAgentRefusal(err)
		if e.tool.uncertain {
			e.tool.uncertainAttempt = attempt
		}
		return zero, err
	}
	attempt.receipt = &receipt
	attempt.undoable = attempt.undoable && !receipt.Unchanged
	e.chat.rememberApply(input, receipt)
	attempt.signatureInvalidated = store.Workbook() && craftWorkbookSigned(native)
	rememberNativeAgentReceipt(e.tool, receipt)
	e.tool.uncertainAttempt = nil
	e.tool.uncertain = false
	return receipt, nil
}

// forgetNativeAgentAttempt drops a request recorded before storage admission
// when admission refused it, so a retry runs from the start.
func forgetNativeAgentAttempt(e *craftNativeAgentEntry, requestID string, attempt *craftNativeAgentToolAttempt) {
	if e.tool.requests[requestID] == attempt {
		delete(e.tool.requests, requestID)
	}
	if e.tool.currentAttempt == attempt {
		e.tool.currentAttempt = nil
	}
}

// proposeTool prepares a complete candidate over the exact read base without
// touching the lease, so a human holder keeps editing. It is recorded once per
// request ID; the engine output is never published as a version here.
func (m *craftNativeAgentManager) proposeTool(ctx context.Context, e *craftNativeAgentEntry, store *mooring.NativeStore, profile CraftNativeDocumentProfile, input craftNativeAgentToolRequest) (mooring.NativeProposal, error) {
	canonical, _ := json.Marshal(input)
	fingerprint := sha256.Sum256(canonical)
	base, _ := craftNativeVersion(input.BaseVersion)
	if prior := e.tool.requests[input.RequestID]; prior != nil {
		if prior.input != fingerprint {
			return mooring.NativeProposal{}, artifacts.ErrVersionConflict
		}
		if prior.proposalErr != nil {
			return mooring.NativeProposal{}, prior.proposalErr
		}
		candidate := prior.proposalCandidate
		if prior.proposalID != "" {
			p, err := store.ProposalContent(ctx, e.grant.Scope, prior.proposalID)
			if err != nil {
				return mooring.NativeProposal{}, err
			}
			candidate = p.Content
		}
		if !prior.attempted || len(candidate) == 0 {
			return mooring.NativeProposal{}, ErrCraftNativeAgentEngine
		}
		return m.recordProposalAttempt(ctx, e, store, input, prior, candidate)
	}
	if e.tool.uncertain {
		return mooring.NativeProposal{}, mooring.ErrNativeUnconfirmed
	}
	if len(e.tool.requests) >= maxNativeAgentChangeRequests {
		return mooring.NativeProposal{}, errNativeAgentChangeRequestLimit
	}
	state, err := store.Read(ctx, e.grant.Scope)
	if err != nil {
		return mooring.NativeProposal{}, err
	}
	if state.LatestVersion != base {
		return mooring.NativeProposal{}, artifacts.ErrVersionConflict
	}
	native, err := nativeAgentDocumentBytes(state)
	if err != nil {
		return mooring.NativeProposal{}, err
	}
	attempt := &craftNativeAgentToolAttempt{input: fingerprint, base: base, isProposal: true}
	e.tool.requests[input.RequestID] = attempt
	e.tool.currentAttempt = attempt
	proposeWindow := 65 * time.Second
	if e.chat != nil {
		proposeWindow = craftNativeChatRunEngineWindow
	}
	active, cancel := context.WithTimeout(ctx, proposeWindow)
	defer cancel()
	limit := nativeAgentCandidateLimit(store)
	upload, err := store.ReserveUpload(active, e.grant.Scope, craftNativeReservation(int64(limit)))
	if err != nil {
		forgetNativeAgentAttempt(e, input.RequestID, attempt)
		return mooring.NativeProposal{}, err
	}
	defer upload.Close()
	active = upload.Context(active)
	ctx = upload.Context(ctx)
	candidate, err := m.config.AgentApply(active, profile, native, input.Commands)
	if err != nil {
		attempt.proposalErr = err
		return mooring.NativeProposal{}, err
	}
	if len(candidate) == 0 || len(candidate) > limit {
		attempt.proposalErr = ErrCraftNativeAgentEngine
		return mooring.NativeProposal{}, ErrCraftNativeAgentEngine
	}
	// Run authority is checked again after the external engine call.
	if err = m.toolAuthority(ctx, e); err != nil {
		return mooring.NativeProposal{}, err
	}
	attempt.proposalCandidate = append([]byte(nil), candidate...)
	attempt.attempted = true
	return m.recordProposalAttempt(ctx, e, store, input, attempt, attempt.proposalCandidate)
}

func (m *craftNativeAgentManager) recordProposalAttempt(ctx context.Context, e *craftNativeAgentEntry, store *mooring.NativeStore, input craftNativeAgentToolRequest, attempt *craftNativeAgentToolAttempt, candidate []byte) (mooring.NativeProposal, error) {
	proposal, err := store.Propose(ctx, e.grant.Scope, attempt.base, input.RequestID, input.Summary, candidate)
	if err != nil {
		if attempt.proposalID == "" {
			if !knownNativeAgentRefusal(err) {
				e.tool.uncertain, e.tool.uncertainAttempt = true, attempt
			} else if e.tool.uncertainAttempt != attempt {
				attempt.proposalCandidate, attempt.proposalErr = nil, err
			}
		}
		return mooring.NativeProposal{}, err
	}
	attempt.proposalID, attempt.proposalCandidate = proposal.ID, nil
	rememberNativeAgentProposal(e, input.RequestID, proposal)
	if e.tool.uncertainAttempt == attempt {
		e.tool.uncertain, e.tool.uncertainAttempt = false, nil
	}
	return proposal, nil
}

func knownNativeAgentRefusal(err error) bool {
	for _, known := range []error{mooring.ErrInvalid, mooring.ErrNativeValidation, mooring.ErrNativeProfile, mooring.ErrEditLeaseDenied, mooring.ErrEditLeaseStale, mooring.ErrEditLeaseBusy, artifacts.ErrVersionConflict, artifacts.ErrVersionQuota, errCraftNativeAgentDenied} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}

func rememberNativeAgentReceipt(state *craftNativeAgentToolState, receipt mooring.NativeSaveReceipt) {
	version, err := craftNativeVersion(receipt.Version)
	if err != nil {
		return
	}
	if state.lastReceipt != nil {
		previous, err := craftNativeVersion(state.lastReceipt.Version)
		if err != nil || previous >= version {
			return
		}
	}
	copy := receipt
	state.lastReceipt = &copy
}

// Caller holds callMu. Keep receipts and uncertainty for truthful status, but
// closed runs no longer need a potentially large candidate for reconciliation.
func clearNativeProposalCandidatesLocked(e *craftNativeAgentEntry) {
	if e.tool != nil {
		for _, attempt := range e.tool.requests {
			attempt.proposalCandidate = nil
		}
	}
}

// Invoke only outside mu and SQL writer transactions. An active call performs
// the same cleanup when it releases callMu; retirement must never wait for it.
func (m *craftNativeAgentManager) clearClosedProposalCandidates(e *craftNativeAgentEntry) {
	if e == nil || !e.callMu.TryLock() {
		return
	}
	defer e.callMu.Unlock()
	m.mu.Lock()
	closed := e.closed
	m.mu.Unlock()
	if closed {
		clearNativeProposalCandidatesLocked(e)
	}
}

// replyNativeSheetBusy answers a request that found no free engine session.
func replyNativeSheetBusy(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": craftSheetReasonText[CraftSheetReasonBusy], "reason": CraftSheetReasonBusy})
}

// craftNativeAgentSheetRecord audits an agent's saved revision on the saving
// transaction: a Sheet as artifact.sheet_saved, a Word document as
// artifact.document_saved, naming the person it acts for, the agent and its
// run (a chat run's, or the native Craft task's confirmed run).
func craftNativeAgentSheetRecord(e *craftNativeAgentEntry) mooring.NativeRecord {
	if e == nil || e.grant.Application != "gridcraft" && e.grant.Application != "wordcraft" {
		return nil
	}
	scope, app, user, agent, run := e.grant.Scope, e.grant.Application, e.grant.Original.UserID, e.grant.AgentID, e.attempt.runID
	if run == "" {
		run = e.claimedRun
	}
	if run == "" {
		run = e.workID
	}
	if e.chat != nil {
		user, agent, run = e.chat.authority.HumanUserID, e.chat.authority.AgentID, e.chat.authority.RunID
	}
	return func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt, native []byte) error {
		return recordCraftSheetSaved(ctx, tx, craftSheetSaved{WorkspaceID: scope.WorkspaceID, PageID: scope.ArtifactID, Version: r.Version,
			Source: craftSheetSavedByAgent, Application: app, UserID: user, AgentID: agent, RunID: run, Native: native})
	}
}

// nativeAgentCandidateLimit is the largest document an agent change may save: a workbook file
// up to one storage upload less the manifest room (64 MiB - 256 KiB), a native document 32 MiB.
func nativeAgentCandidateLimit(store *mooring.NativeStore) int {
	if store.Workbook() {
		return int(artifacts.MaxStorageUploadBytes) - (256 << 10)
	}
	return 32 << 20
}

// craftNativeAgentResolvedOps is what an agent write applied, as saved with its version: each
// command with its address resolved (the sheet folded in), in order, named exactly as the editor's
// gridcraft:execute takes it (cell.set, range.setValues, edit.clearContents). Not ok when any
// command is not such a write or an address has no sheet; that version then has no operations.
func craftNativeAgentResolvedOps(commands json.RawMessage) (json.RawMessage, bool) {
	ops, err := resolveCraftWorkbookOps(commands)
	return ops, err == nil
}

var errCraftNativeCellSetValue = errors.New("invalid cell.set value")

func resolveCraftWorkbookOps(commands json.RawMessage) (json.RawMessage, error) {
	var entries []struct {
		Command string                     `json:"command"`
		Params  map[string]json.RawMessage `json:"params"`
	}
	if json.Unmarshal(commands, &entries) != nil || len(entries) == 0 {
		return nil, ErrCraftNativeAgentEngine
	}
	type op struct {
		Command string         `json:"command"`
		Params  map[string]any `json:"params"`
	}
	out := make([]op, 0, len(entries))
	for _, e := range entries {
		// The recalculation is an effect of every write, not an operation of its own.
		if e.Command == "formulas.calculateNow" {
			continue
		}
		key := "range"
		if e.Command == "cell.set" {
			key = "cell"
			value := bytes.TrimSpace(e.Params["input"])
			if len(value) == 0 {
				return nil, errCraftNativeCellSetValue
			}
			if value[0] != '"' {
				if nativeAgentEngineValue(value, nativeAgentEngineRules{}) != nil {
					return nil, errCraftNativeCellSetValue
				}
				e.Command, e.Params = "range.setValues", nativeAgentCellSetBlock(e.Params)
				key = "range"
			}
		}
		if raw, err := json.Marshal(e.Params); err == nil {
			lifted, ok := nativeAgentLiftSheet(e.Command, raw)
			if !ok || json.Unmarshal(lifted, &e.Params) != nil {
				return nil, ErrCraftNativeAgentEngine
			}
		}
		address, err := nativeAgentSheetAddress("", e.Params, key)
		// Without its sheet the editor would write the person's active sheet.
		if err != nil {
			return nil, err
		}
		if !strings.Contains(address, "!") && (e.Command == "cell.set" || e.Command == "range.setValues" || e.Command == "range.clear") {
			return nil, errCraftNativeSheetNameRequired
		}
		params := map[string]any{key: address}
		name := e.Command
		switch e.Command {
		case "cell.set":
			params["input"] = e.Params["input"]
		case "range.setValues":
			rng, err := craftResolvedWriteRange(address, e.Params[key], e.Params["values"])
			if err != nil {
				return nil, err
			}
			params[key], params["values"] = rng, e.Params["values"]
		case "range.clear":
			name = "edit.clearContents"
		default:
			return nil, ErrCraftNativeAgentEngine
		}
		out = append(out, op{Command: name, Params: params})
	}
	return json.Marshal(out)
}

// An A1 anchor with a matrix changes the complete rectangle. Reject ragged
// matrices because the workbook journal needs an exact footprint for Undo.
func craftResolvedWriteRange(address string, rangeRaw, valuesRaw json.RawMessage) (string, error) {
	area, ok := nativeAgentEngineRange(rangeRaw)
	var rows [][]json.RawMessage
	if !ok || json.Unmarshal(valuesRaw, &rows) != nil || len(rows) == 0 || len(rows) > nativeAgentWriteRows {
		return "", ErrCraftNativeAgentEngine
	}
	width := len(rows[0])
	if width < 1 || width > nativeAgentWriteCols {
		return "", ErrCraftNativeAgentEngine
	}
	for _, row := range rows {
		if len(row) != width {
			return "", errCraftNativeAgentUndoUnavailable
		}
	}
	if area.cells() > 1 {
		if area.height() != len(rows) || area.width() != width {
			return "", ErrCraftNativeAgentEngine
		}
		return address, nil
	}
	lastCol, lastRow := area.col1+width-1, area.row1+len(rows)-1
	if lastCol > nativeAgentMaxColumns || lastRow > nativeAgentMaxRows {
		return "", ErrCraftNativeAgentEngine
	}
	if width > 1 || len(rows) > 1 {
		address = address[:strings.LastIndex(address, "!")+1] + craftUndoA1(area.col1, area.row1) + ":" + craftUndoA1(lastCol, lastRow)
	}
	return address, nil
}

// craftNativeAgentWorkbookRecord adds to the agent's version record the operations it applied,
// for a Sheet kept as its workbook file.
func craftNativeAgentWorkbookRecord(e *craftNativeAgentEntry, store *mooring.NativeStore, base int64, requestID string, ops, inverse json.RawMessage, record mooring.NativeRecord) mooring.NativeRecord {
	agent := e.grant.AgentID
	var chat, run string
	if e.chat != nil {
		agent, chat, run = e.chat.authority.AgentID, e.chat.authority.ChatID, e.chat.authority.RunID
	}
	pin, scope := store.EnginePin(), e.grant.Scope
	operation := nativeAgentOperationID(e, requestID)
	return func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt, native []byte) error {
		if err := record(ctx, tx, r, native); err != nil {
			return err
		}
		if craftWorkbookSigned(native) {
			if err := recordCraftSignatureBreak(ctx, tx, scope.WorkspaceID, scope.ArtifactID, r.Version); err != nil {
				return err
			}
		}
		return recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: scope.WorkspaceID, PageID: scope.ArtifactID, Version: r.Version, BaseVersion: base,
			ActorKind: "agent", ActorID: agent, ChatID: chat, RunID: run, OperationID: operation, EnginePin: pin, Calculate: "calculated", Replayable: true, Ops: ops, Inverse: inverse})
	}
}

var errCraftRealtimeAgentMoved = errors.Join(artifacts.ErrVersionConflict, errors.New("workbook head moved during agent publication"))

var errCraftNativeAgentUndoUnavailable = errors.New("native Craft agent change requires a complete inverse")

// nativeAgentChangeRefused answers the refusals of an agent change that tell the agent what to do
// instead: a disallowed formula, a write without its sheet, or a save that would lose workbook parts.
func nativeAgentChangeRefused(w http.ResponseWriter, err error) bool {
	var loss *craftsession.FidelityLossError
	switch {
	case errors.Is(err, errCraftNativeAgentUndoUnavailable):
		replyChatSheetRefusal(w, http.StatusConflict, CraftSheetReasonNotUndoable)
	case errors.Is(err, errCraftNativeAgentFormula):
		replyChatSheetRefusal(w, http.StatusBadRequest, CraftSheetReasonFormula)
	case errors.Is(err, errCraftNativeCellSetValue):
		replyChatSheetRefusal(w, http.StatusBadRequest, CraftSheetReasonInvalid)
	case errors.Is(err, errCraftNativeSheetNameRequired):
		writeJSON(w, 400, map[string]string{"error": errCraftNativeSheetNameRequired.Error(), "reason": CraftSheetReasonInvalid})
	case errors.As(err, &loss):
		craftWorkbookFidelityRefusal(w, loss.Parts)
	default:
		return false
	}
	return true
}

// craftNativeReservation is the storage reservation for a document of at most size bytes: its
// size and room for the manifest, never more than one storage upload admits (64 MiB).
func craftNativeReservation(size int64) int64 {
	return min(size+(256<<10), artifacts.MaxStorageUploadBytes)
}

// nativeAgentOperationID names one agent change on the server: the request ID is the model's
// and is unique only within its run, so the run is part of it.
func nativeAgentOperationID(e *craftNativeAgentEntry, requestID string) string {
	run := e.attempt.runID
	if run == "" {
		run = e.claimedRun
	}
	if run == "" {
		run = e.workID
	}
	if e.chat != nil {
		run = e.chat.authority.RunID
	}
	return run + ":" + requestID
}
