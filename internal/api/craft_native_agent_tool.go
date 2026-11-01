package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
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
	application, ok := m.documents.applications[e.grant.Application]
	profile, pinned := m.config.Profiles[e.grant.Application]
	if !ok || !pinned {
		replyError(w, 503, "native Craft profile unavailable")
		return
	}
	cfg := application.Store
	cfg.Enabled = m.human.enabled
	cfg.Leases = m.human.leases
	cfg.Authorize = m.human.authorizeNative
	store := mooring.NewNativeStore(m.human.pages.db, cfg)
	switch input.Operation {
	case "acquire":
		lease, err := m.human.leases.Acquire(bound.Context(), e.grant.Scope, craftNativeAgentPrincipal(e))
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
		renewed, err := m.human.leases.Renew(bound.Context(), lease)
		if err != nil {
			craftNativeDocumentError(w, err)
			return
		}
		e.tool.lease = renewed
		if input.Operation == "renew" {
			writeJSON(w, 200, nativeAgentLeaseResult(renewed))
			return
		}
		receipt, err := m.applyTool(bound.Context(), e, store, profile, input)
		if err != nil {
			if e.tool.uncertain {
				replyError(w, 503, "original native save outcome unconfirmed; reconcile the exact original request")
			} else if errors.Is(err, errNativeAgentChangeRequestLimit) {
				replyError(w, 429, "this run has reached its change request limit")
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
		if len(input.Query) > 0 {
			native, err := nativeAgentDocumentBytes(state)
			if err != nil {
				craftNativeDocumentError(w, err)
				return
			}
			view, err := m.config.AgentInspect(bound.Context(), profile, native, input.Query)
			if err != nil || len(view) > 1<<20 || !json.Valid(view) {
				replyError(w, 422, "original native engine query refused")
				return
			}
			// The authorization is checked again after the external engine read.
			if err = m.toolAuthority(bound.Context(), e); err != nil {
				craftNativeDocumentError(w, err)
				return
			}
			result["view"] = view
		}
		writeJSON(w, 200, result)
	}
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
		receipt, err := store.ReconcileSnapshot(ctx, prior.lease, prior.base, prior.revision, prior.hash)
		if err == nil {
			prior.receipt = &receipt
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
	if len(e.tool.requests) >= maxNativeAgentChangeRequests {
		return zero, errNativeAgentChangeRequestLimit
	}
	state, err := store.Read(ctx, e.grant.Scope)
	if err != nil {
		return zero, err
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
	renewal, cancel := context.WithTimeout(ctx, 65*time.Second)
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
				if _, err := m.human.leases.Renew(renewal, attempt.lease); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	upload, err := store.ReserveUpload(active, e.grant.Scope, (32<<20)+(256<<10))
	if err != nil {
		// Refused before the engine ran: nothing was attempted, so the same
		// request ID must stay retryable.
		forgetNativeAgentAttempt(e, input.RequestID, attempt)
		return zero, err
	}
	defer upload.Close()
	active = upload.Context(active)
	candidate, err := m.config.AgentApply(active, profile, native, input.Commands)
	if err != nil {
		return zero, err
	}
	if len(candidate) == 0 || len(candidate) > 32<<20 {
		return zero, ErrCraftNativeAgentEngine
	}
	sum := sha256.Sum256(candidate)
	attempt.hash = "sha256:" + hex.EncodeToString(sum[:])
	attempt.attempted = true
	// There is one publication attempt. Any repeat reconciles this exact saved
	// snapshot; it never reruns the original engine or advances the base.
	receipt, err := store.SaveSnapshot(active, attempt.lease, base, attempt.revision, attempt.hash, candidate)
	if err != nil {
		e.tool.uncertain = !knownNativeAgentRefusal(err)
		if e.tool.uncertain {
			e.tool.uncertainAttempt = attempt
		}
		return zero, err
	}
	attempt.receipt = &receipt
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
	active, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	upload, err := store.ReserveUpload(active, e.grant.Scope, (32<<20)+(256<<10))
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
	if len(candidate) == 0 || len(candidate) > 32<<20 {
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
	for _, known := range []error{mooring.ErrInvalid, mooring.ErrNativeValidation, mooring.ErrNativeProfile, mooring.ErrEditLeaseDenied, mooring.ErrEditLeaseStale, artifacts.ErrVersionConflict, artifacts.ErrVersionQuota, errCraftNativeAgentDenied} {
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
