package api

// Host-side memory mutation for agent containers — the endpoint that makes
// memory.ProfileGuaranteed reachable at all.
//
// Contract: docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §8, with
// §3's invariant I4 (a mutation records — and here, verifies — the run and
// generation it belongs to).
//
// # Why this route exists
//
// internal/memory.Mutate has two profiles. ProfileGuaranteed REFUSES a write
// that lacks a caller-supplied operation id, a durable ledger handle,
// ExpectedRevision + non-nil Removals on a replace, or a wired Authorize — it
// does not downgrade. Before this file NOTHING in the tree could satisfy it,
// because the only two agent-facing writers (the sidecar's POST /memory/write
// and the MCP memory.write dispatcher) both run INSIDE the agent container and
// hold no *sql.DB: `grep 'sql.DB' internal/sidecar/*.go` returned nothing. The
// ledgerless note in internal/memory/mutate.go says so in as many words —
// "without an IPC round trip through an API endpoint that does not exist".
//
// This is that endpoint. The sidecar forwards the write over the existing IPC
// channel; the host performs it against the real ledger, on the HOST side of
// the same bind mount the container writes through (memory.HostAgentMemoryRoot
// / memory.HostCrewMemoryRoot resolve the host twin of the container path —
// writing the container literal from a host process is #1663).
//
// # The trust model, which is the hybrid-search one
//
// X-Internal-Token authenticates the SIDECAR (one shared identity per crew
// container); X-Acting-Agent-Slug narrows it to one agent, and is re-proved
// here against the token's own workspace/crew binding exactly as
// MemoryHybridSearchHandler.SearchInternal does. The header can only ever
// narrow the token's authority, never widen it.
//
// The mutation also requires the original per-run bearer capability, verified
// with the host-derived crew run key and matched against workspace/agent/run.
// A live sibling run of the same agent does not authorize this caller.
//
// # Authorize is real, not a placeholder
//
// The guaranteed profile refuses a nil Authorize, so the cheap way to satisfy
// the gate is a closure that returns nil. That would pass the gate while
// checking nothing, which is worse than an admitted gap. authorizeRun instead
// reads the durable work ledger (work_items / work_attempts, migration
// 20260910200255_durable_work_ledger.sql) and refuses unless the named run is
// this agent's, in this workspace, still live, and carrying the CURRENT
// generation on both the attempt and the item. internal/memory must not import
// internal/work, which is why it arrives as a closure; this file queries the
// two tables directly rather than through work.Store so that it does not couple
// to a package under active construction on the same branch.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/memory/memdiff"
	"github.com/crewship-ai/crewship/internal/safepath"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// MemoryMutationHandler serves the two internal memory-contract routes. Same
// (db, logger) shape as every other internal handler; storagePath is the host
// root the container provider bind-mounts per crew (cfg.Storage.BasePath) and
// blobRoot is the content-addressed store the durable intent parks target
// content in — the SAME store memory_versions uses, so a recovered write and a
// recorded version share their blobs.
type MemoryMutationHandler struct {
	masterToken string
	db          *sql.DB
	logger      *slog.Logger
	storagePath string
	blobRoot    string
	scrub       *scrubber.Scrubber
}

// NewMemoryMutationHandler builds the handler. Either path being empty leaves
// the routes registered but answering 503: a memory write that cannot resolve
// the real bind-mount source must not land somewhere no container reads (#1663),
// and a guaranteed write with nowhere to park its durable intent is not a
// guaranteed write.
func NewMemoryMutationHandler(db *sql.DB, storagePath, blobRoot string, logger *slog.Logger, masterToken string) *MemoryMutationHandler {
	return &MemoryMutationHandler{
		masterToken: masterToken,
		db:          db,
		logger:      logger,
		storagePath: storagePath,
		blobRoot:    blobRoot,
		scrub:       scrubber.New(),
	}
}

// memoryMutationCaps is the byte ceiling per whitelisted file, and it is the
// host-side twin of memoryWriteCaps in internal/sidecar/memory_write.go. The
// two cannot be one table: internal/api must not import internal/sidecar (the
// sidecar package's own tests import internal/api, so the edge would be a
// cycle). Keep them in sync — the sidecar pre-checks with its table and this
// side enforces with this one, so a divergence shows up as a write the sidecar
// accepted and the host refused, not as an unbounded file.
var memoryMutationCaps = map[string]int{
	"AGENT.md": 4000,
	"CREW.md":  4000,
	"pins.md":  8000,
}

// memoryMutationDailyCap mirrors internal/sidecar.dailyCap and
// internal/memory/tools.go capDailyBytes.
const memoryMutationDailyCap = 30_000

// maxMemoryMutationRequestBytes caps the inbound body. The largest legitimate
// content is one daily log; the slack covers JSON framing plus a removals
// declaration, which on a full-file rewrite carries one entry per removed span.
const maxMemoryMutationRequestBytes int64 = memoryMutationDailyCap + 128*1024

// memoryMutationFileCap resolves the cap for a whitelisted relative path, or
// ok=false. Same allowlist the sidecar advertises: AGENT.md, CREW.md, pins.md,
// and exactly one .md segment under daily/.
func memoryMutationFileCap(rel string) (int, bool) {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if c, known := memoryMutationCaps[clean]; known {
		return c, true
	}
	if rest, ok := strings.CutPrefix(clean, "daily/"); ok &&
		!strings.Contains(rest, "/") &&
		strings.HasSuffix(rest, ".md") &&
		strings.TrimSuffix(rest, ".md") != "" {
		return memoryMutationDailyCap, true
	}
	return 0, false
}

// MemoryMutationRequest is the body of POST /api/v1/internal/memory/mutation.
//
// It carries everything ProfileGuaranteed requires and nothing it does not.
// The acting agent is NOT in here — it comes from the token plus
// X-Acting-Agent-Slug, because a field an agent container can set is not an
// identity.
type MemoryMutationRequest struct {
	// OperationID is the idempotency key, stable across retries of the same
	// logical write. Required: a synthesised one is not idempotent across a
	// retry, which is exactly what the guaranteed profile refuses.
	OperationID string `json:"operation_id"`

	// Scope ∈ {agent, crew} picks the tier; File is the tier-relative path.
	Scope string `json:"scope"`
	File  string `json:"file"`

	// Op ∈ {append, replace}.
	Op string `json:"op"`

	Content string `json:"content"`

	// ExpectedRevision is the revision the client diffed against. Required on
	// a replace (a replace without CAS can silently lose a concurrent write);
	// 0 on an append means unconditional.
	ExpectedRevision int64 `json:"expected_revision"`

	// ExpectedSHA256 is the optional belt-and-braces on-disk CAS.
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`

	// Removals are the spans this replace deletes, in the base revision's
	// normalised 1-based line numbering. On a replace this must be present —
	// an empty array is a positive statement that nothing is removed; a
	// MISSING array is "undeclared" and the guaranteed profile refuses it.
	Removals []memdiff.Removal `json:"removals"`

	// RunID and Generation are I4's fencing pair. They are verified against
	// the durable work ledger, not merely recorded — see authorizeRun. A
	// caller may supply a run it does not own or a stale generation; both are
	// refused, so forging these buys nothing the caller did not already have.
	RunID      string `json:"run_id"`
	Generation int64  `json:"generation"`

	// Source is free-form provenance for the audit trail.
	Source string `json:"source,omitempty"`
}

// MemoryMutationResponse is the 200 envelope.
//
// Revision, ContentSHA256 and LedgerRecorded are mandatory in the sense that
// matters: they are always populated, so a caller can never describe an
// unchecked write as checked. Profile echoes the guarantee the write was
// actually made under.
type MemoryMutationResponse struct {
	Profile          string `json:"profile"`
	Revision         int64  `json:"revision"`
	ContentSHA256    string `json:"content_sha256"`
	LedgerRecorded   bool   `json:"ledger_recorded"`
	BytesWritten     int    `json:"bytes_written"`
	OperationID      string `json:"operation_id"`
	MutationID       string `json:"mutation_id"`
	AuditPath        string `json:"audit_path"`
	BaseRevision     int64  `json:"base_revision"`
	BaseSHA256       string `json:"base_sha256"`
	Idempotent       bool   `json:"idempotent"`
	RemovalsVerified bool   `json:"removals_verified"`
	MergedFinalLine  bool   `json:"merged_final_line,omitempty"`
	AdoptedBase      bool   `json:"adopted_base,omitempty"`
	Recovered        int    `json:"recovered,omitempty"`
}

// MemoryMutationConflict is the 409 envelope carrying §8's error vocabulary:
// memory_conflict, undeclared_removal, operation_conflict, protected_removal.
// CurrentRevision / CurrentSHA256 are what the caller re-reads against before
// proposing again under a NEW operation id.
type MemoryMutationConflict struct {
	Error           string `json:"error"`
	Code            string `json:"code"`
	CurrentRevision int64  `json:"current_revision"`
	CurrentSHA256   string `json:"current_sha256,omitempty"`
	Message         string `json:"message,omitempty"`
}

// MemoryMutationRejection is the 422 envelope for a POLICY refusal (cap,
// scrubber, verifier). Not an error: the file was not changed and nothing
// failed.
type MemoryMutationRejection struct {
	Rejected bool           `json:"rejected"`
	Kind     string         `json:"kind"`
	Detail   map[string]any `json:"detail,omitempty"`
	Hits     []scrubber.Hit `json:"hits,omitempty"`
	Message  string         `json:"message,omitempty"`
}

// MemoryCanonicalReadResponse is the body of GET
// /api/v1/internal/memory/canonical.
//
// This route is the other half of the guaranteed profile, not a convenience: a
// client cannot supply expected_revision without having been told the revision,
// and memory.ReadCanonical is the only thing that knows it. The sidecar's own
// GET /memory/read reads the file with no ledger and therefore reports
// revision 0, which is unusable as a CAS anchor.
type MemoryCanonicalReadResponse struct {
	Exists         bool   `json:"exists"`
	Content        string `json:"content"`
	ContentSHA256  string `json:"content_sha256"`
	Bytes          int    `json:"bytes"`
	Revision       int64  `json:"revision"`
	LedgerRecorded bool   `json:"ledger_recorded"`
	Canonical      bool   `json:"canonical"`
	Drift          bool   `json:"drift"`
	AnchorSHA256   string `json:"anchor_sha256,omitempty"`
	AuditPath      string `json:"audit_path"`
	Scope          string `json:"scope"`
	Tier           string `json:"tier,omitempty"`

	Provenance MemoryCanonicalProvenance `json:"provenance"`
}

// MemoryCanonicalProvenance is §3's provenance block for the last confirmed
// mutation of a key.
type MemoryCanonicalProvenance struct {
	MutationID  string `json:"mutation_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	Source      string `json:"source,omitempty"`
	ActorType   string `json:"actor_type,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	Generation  int64  `json:"generation,omitempty"`
	Op          string `json:"op,omitempty"`
	RecordedAt  string `json:"recorded_at,omitempty"`
	ConfirmedAt string `json:"confirmed_at,omitempty"`
}

// memoryMutationTarget is one resolved (agent, scope, file) triple: where the
// bytes live on the host, and the workspace-unique key the ledger anchors them
// under.
type memoryMutationTarget struct {
	agentID   string
	agentSlug string
	crewID    string
	scope     string
	path      string
	auditPath string
	ledgerKey string
	tier      memory.Tier
	byteCap   int
}

// Mutate serves POST /api/v1/internal/memory/mutation behind requireInternal.
func (h *MemoryMutationHandler) Mutate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMemoryMutationRequestBytes)

	wsID, slug, ok := h.actingIdentity(w, r)
	if !ok {
		return
	}

	var req MemoryMutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			replyError(w, http.StatusRequestEntityTooLarge, "request body exceeds size limit")
			return
		}
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// The guaranteed profile's own preconditions, refused HERE with a message
	// naming the field rather than deep inside Mutate with a message about a
	// Go struct. Mutate re-checks every one of them; this is the HTTP-shaped
	// statement of the same contract, not a substitute for it.
	if strings.TrimSpace(req.OperationID) == "" {
		replyError(w, http.StatusBadRequest, "operation_id required: the guaranteed profile has no idempotency without a caller-supplied one")
		return
	}
	var op memory.MutateOp
	switch req.Op {
	case string(memory.OpAppend):
		op = memory.OpAppend
	case string(memory.OpReplace):
		op = memory.OpReplace
		if req.Removals == nil {
			replyError(w, http.StatusBadRequest, "removals required on a replace: a missing array means undeclared, an empty one means 'this replace deletes nothing'")
			return
		}
	default:
		replyError(w, http.StatusBadRequest, `op must be "append" or "replace"`)
		return
	}
	if strings.TrimSpace(req.RunID) == "" {
		replyError(w, http.StatusBadRequest, "run_id required: without it there is no run whose generation can be verified, and the guaranteed profile refuses an unverifiable write")
		return
	}

	target, ok := h.resolveTarget(w, r, wsID, slug, req.Scope, req.File)
	if !ok {
		return
	}
	// The authenticated capability, not the JSON body, supplies run identity.
	// A crew IPC token authenticates the transport but cannot stand in for this.
	auth := strings.Fields(r.Header.Get("Authorization"))
	if len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") || h.masterToken == "" {
		replyError(w, http.StatusForbidden, "authenticated run capability required")
		return
	}
	runKey := internaltoken.DeriveAgentRunKey(h.masterToken, wsID, target.crewID)
	capWS, capAgent, capRun, valid := internaltoken.ValidateAgentRunToken(runKey, auth[1])
	if !valid || capWS != wsID || capAgent != target.agentID || capRun != req.RunID {
		replyError(w, http.StatusForbidden, "run capability does not authorize this mutation")
		return
	}
	if h.blobRoot == "" {
		replyError(w, http.StatusServiceUnavailable, "memory versions blob root is not configured; the durable intent has nowhere to park the target content")
		return
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "sidecar:/memory/write"
	}

	res, err := memory.Mutate(r.Context(), h.db, memory.MutateRequest{
		// The whole point of the route. Nothing downgrades this: a caller that
		// cannot meet the contract is refused above, or by Mutate itself.
		Profile:     memory.ProfileGuaranteed,
		OperationID: req.OperationID,
		WorkspaceID: wsID,
		ActorType:   "agent",
		ActorID:     target.agentID,
		RunID:       req.RunID,
		Generation:  req.Generation,
		Source:      source,
		Tier:        target.tier,
		Scope:       target.ledgerKey,
		Key:         req.File,
		Path:        target.path,
		StorageRoot: h.storagePath,
		AuditPath:   target.auditPath,
		Op:          op,
		Content:     req.Content,

		ExpectedRevision: req.ExpectedRevision,
		ExpectedSHA256:   req.ExpectedSHA256,
		Removals:         req.Removals,

		// A replace discards the base, so importing a non-canonical one
		// changes no bytes on disk; it only lets the declared-removal check
		// diff against a normalised base instead of refusing outright.
		Import: op == memory.OpReplace,

		// I4. Not a closure that returns nil — see authorizeRun.
		Authorize: h.authorizeRun(wsID, target.agentID, req.RunID, req.Generation),

		BlobRoot: h.blobRoot,
		Cfg: memory.WriteConfig{
			MaxBytes:     target.byteCap,
			Scrubber:     h.scrub,
			ScrubberMode: scrubber.ModeBlock,
		},
	})
	if err != nil {
		h.replyMutateError(w, r, target, res, err)
		return
	}
	if res.Rejection != nil {
		writeJSON(w, http.StatusUnprocessableEntity, MemoryMutationRejection{
			Rejected: true,
			Kind:     res.Rejection.Kind,
			Detail:   res.Rejection.Detail,
			Hits:     res.Rejection.Hits,
			Message:  res.Rejection.Kind + " policy rejected this write",
		})
		return
	}

	writeJSON(w, http.StatusOK, MemoryMutationResponse{
		Profile:          string(memory.ProfileGuaranteed),
		Revision:         res.Revision,
		ContentSHA256:    res.ContentSHA256,
		LedgerRecorded:   res.LedgerRecorded,
		BytesWritten:     res.BytesWritten,
		OperationID:      req.OperationID,
		MutationID:       res.MutationID,
		AuditPath:        target.auditPath,
		BaseRevision:     res.BaseRevision,
		BaseSHA256:       res.BaseSHA256,
		Idempotent:       res.Idempotent,
		RemovalsVerified: res.RemovalsVerified,
		MergedFinalLine:  res.MergedFinalLine,
		AdoptedBase:      res.AdoptedBase,
		Recovered:        res.Recovered,
	})
}

// Read serves GET /api/v1/internal/memory/canonical behind requireInternal.
// Query: ?scope=agent|crew&file=<tier-relative path>.
func (h *MemoryMutationHandler) Read(w http.ResponseWriter, r *http.Request) {
	wsID, slug, ok := h.actingIdentity(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	target, ok := h.resolveTarget(w, r, wsID, slug, q.Get("scope"), q.Get("file"))
	if !ok {
		return
	}

	res, err := memory.ReadCanonical(r.Context(), h.db, memory.ReadRequest{
		WorkspaceID: wsID,
		Path:        target.path,
		StorageRoot: h.storagePath,
		AuditPath:   target.auditPath,
	})
	if err != nil {
		replyInternalError(w, h.logger, "internal memory canonical read", err)
		return
	}

	scope := res.Scope
	if scope == "" {
		scope = target.ledgerKey
	}
	tier := string(res.Tier)
	if tier == "" {
		tier = string(target.tier)
	}
	writeJSON(w, http.StatusOK, MemoryCanonicalReadResponse{
		Exists:         res.Exists,
		Content:        string(res.Content),
		ContentSHA256:  res.ContentSHA256,
		Bytes:          res.Bytes,
		Revision:       res.Revision,
		LedgerRecorded: res.LedgerRecorded,
		Canonical:      res.Canonical,
		Drift:          res.Drift,
		AnchorSHA256:   res.AnchorSHA256,
		AuditPath:      target.auditPath,
		Scope:          scope,
		Tier:           tier,
		Provenance: MemoryCanonicalProvenance{
			MutationID:  res.Provenance.MutationID,
			OperationID: res.Provenance.OperationID,
			Source:      res.Provenance.Source,
			ActorType:   res.Provenance.ActorType,
			ActorID:     res.Provenance.ActorID,
			RunID:       res.Provenance.RunID,
			Generation:  res.Provenance.Generation,
			Op:          res.Provenance.Op,
			RecordedAt:  res.Provenance.RecordedAt,
			ConfirmedAt: res.Provenance.ConfirmedAt,
		},
	})
}

// actingIdentity resolves (workspace, acting slug) from the internal token and
// X-Acting-Agent-Slug, writing the refusal itself when it cannot. Same posture
// as SearchInternal: a master (unbound) token has no workspace to resolve the
// slug inside, so it is refused rather than widened.
func (h *MemoryMutationHandler) actingIdentity(w http.ResponseWriter, r *http.Request) (wsID, slug string, ok bool) {
	wsID = InternalTokenWorkspaceFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusForbidden, "workspace-bound internal token required")
		return "", "", false
	}
	slug = strings.TrimSpace(r.Header.Get(actingAgentSlugHeader))
	if slug == "" {
		replyError(w, http.StatusForbidden, "acting agent identity required")
		return "", "", false
	}
	return wsID, slug, true
}

// resolveTarget proves the acting agent is inside the token's binding, then
// resolves the HOST path and the workspace-unique ledger key for (scope, file).
// It writes its own refusal and returns ok=false.
func (h *MemoryMutationHandler) resolveTarget(w http.ResponseWriter, r *http.Request, wsID, slug, scope, file string) (memoryMutationTarget, bool) {
	var t memoryMutationTarget

	var agentCrew sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, crew_id FROM agents WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`,
		wsID, slug).Scan(&t.agentID, &agentCrew)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusForbidden, "acting agent is not in the workspace bound to the internal token")
		return t, false
	}
	if err != nil {
		// A transient DB failure is not an authorization decision — 500 so
		// retry/backoff works and a real 403 stays meaningful in monitoring.
		replyInternalError(w, h.logger, "resolve acting agent for internal memory mutation", err)
		return t, false
	}
	if boundCrew := InternalTokenCrewFromContext(r.Context()); boundCrew != "" && agentCrew.String != boundCrew {
		replyError(w, http.StatusForbidden, "acting agent is not in the crew bound to the internal token")
		return t, false
	}
	t.agentSlug = slug
	t.crewID = agentCrew.String

	if scope == "" {
		scope = "agent"
	}
	if scope != "agent" && scope != "crew" {
		replyError(w, http.StatusBadRequest, "invalid scope: use agent or crew")
		return t, false
	}
	t.scope = scope

	byteCap, known := memoryMutationFileCap(file)
	if !known {
		replyError(w, http.StatusBadRequest,
			"unsupported file path; allowed: AGENT.md, CREW.md, pins.md, daily/<name>.md")
		return t, false
	}
	t.byteCap = byteCap

	if h.storagePath == "" {
		// #1663: without the host root this process would write the
		// container-absolute literal at the host filesystem root, outside
		// every bind source, where no container will ever read it.
		replyError(w, http.StatusServiceUnavailable, "storage base path is not configured; the host cannot resolve the crew bind-mount source")
		return t, false
	}
	if t.crewID == "" {
		replyError(w, http.StatusForbidden, "acting agent has no crew; its memory root cannot be resolved on the host")
		return t, false
	}

	var root string
	switch scope {
	case "agent":
		root, err = memory.HostAgentMemoryRoot(h.storagePath, t.crewID, slug)
		t.ledgerKey = "agent:" + slug
		// Workspace-unique: two agents in one workspace both own an
		// "AGENT.md", and only the scoped form separates them.
		t.auditPath = "agent:" + slug + "/" + filepath.ToSlash(filepath.Clean(file))
	case "crew":
		root, err = memory.HostCrewMemoryRoot(h.storagePath, t.crewID)
		t.ledgerKey = "crew:" + t.crewID
		t.auditPath = "crew:" + t.crewID + "/" + filepath.ToSlash(filepath.Clean(file))
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve host memory root", err)
		return t, false
	}
	// safepath.JoinRel refuses absolute paths, NUL bytes and any "../"
	// traversal, and guarantees the result is root or a descendant of it —
	// the same shared guard the sidecar's safeJoinUnder builds on.
	full, err := safepath.JoinRel(root, file)
	if err != nil {
		replyError(w, http.StatusForbidden, "illegal file path")
		return t, false
	}
	// Keep the configured host boundary explicit after resolving the narrower
	// agent/crew root. This check dominates every filesystem sink in Mutate,
	// including parent-directory creation before its under-lock authorization.
	storageRoot, err := filepath.Abs(h.storagePath)
	if err != nil {
		replyInternalError(w, h.logger, "resolve absolute storage root", err)
		return t, false
	}
	storagePrefix := filepath.Clean(storageRoot)
	if !strings.HasSuffix(storagePrefix, string(filepath.Separator)) {
		storagePrefix += string(filepath.Separator)
	}
	full, err = filepath.Abs(full)
	if err != nil || !strings.HasPrefix(full, storagePrefix) {
		replyError(w, http.StatusForbidden, "memory path escapes host storage")
		return t, false
	}
	t.path = full

	switch {
	case scope == "crew":
		t.tier = memory.TierCrew
	case filepath.ToSlash(filepath.Clean(file)) == "pins.md":
		t.tier = memory.TierPins
	default:
		t.tier = memory.TierAgent
	}
	return t, true
}

// replyMutateError maps a Mutate failure onto the wire.
//
// The §8 vocabulary is a 409 the client acts on (re-read, propose again under a
// NEW operation id, at most twice); a profile failure is a 400 naming what was
// missing, because it is a client bug that no retry fixes; an authorization
// failure is a 403. Everything else is a 500 with the detail in the log, not in
// the body.
func (h *MemoryMutationHandler) replyMutateError(w http.ResponseWriter, r *http.Request, t memoryMutationTarget, res memory.MutateResult, err error) {
	if code, ok := memoryMutationConflictCode(err); ok {
		writeJSON(w, http.StatusConflict, MemoryMutationConflict{
			Error:           code,
			Code:            code,
			CurrentRevision: res.BaseRevision,
			CurrentSHA256:   res.BaseSHA256,
			Message:         err.Error(),
		})
		return
	}
	switch {
	case errors.Is(err, errMemoryRunNotCurrent):
		replyError(w, http.StatusForbidden, err.Error())
		return
	case errors.Is(err, memory.ErrProfileUnmet),
		errors.Is(err, memory.ErrLedgerRequired),
		errors.Is(err, memory.ErrNotCanonical),
		errors.Is(err, memory.ErrContentTooLarge):
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.logger != nil {
		h.logger.Error("internal memory mutation failed",
			"error", err, "audit_path", t.auditPath, "agent_id", t.agentID)
	}
	replyError(w, http.StatusInternalServerError, "memory mutation failed")
}

// memoryMutationConflictCode maps §8's error vocabulary onto the string the
// client branches on. ok=false for anything that is not one of the four, so an
// unrelated failure can never be reported as a conflict the client is supposed
// to retry differently. Twin of memoryConflictCode in internal/sidecar.
func memoryMutationConflictCode(err error) (string, bool) {
	switch {
	case errors.Is(err, memory.ErrMemoryConflict):
		return "memory_conflict", true
	case errors.Is(err, memory.ErrUndeclaredRemoval):
		return "undeclared_removal", true
	case errors.Is(err, memory.ErrOperationConflict):
		return "operation_conflict", true
	case errors.Is(err, memory.ErrProtectedRemoval):
		return "protected_removal", true
	}
	return "", false
}

// errMemoryRunNotCurrent is what authorizeRun refuses with. It is one sentinel
// for every I4 failure because the client's move is the same in all of them —
// stop writing under this run — and because distinguishing "no such run" from
// "someone else's run" on the wire would answer a probe.
var errMemoryRunNotCurrent = errors.New("memory mutation refused: run is not the current live attempt for this agent")

// authorizeRun is invariant I4, and it is the reason this endpoint is not just
// the sidecar's write with a database attached.
//
// The guaranteed profile refuses a nil Authorize. Satisfying that with a
// closure that returns nil would pass the gate while checking nothing — worse
// than an admitted gap, because the gate then certifies an unchecked write.
// This closure instead proves, under Mutate's own per-file lock:
//
//   - the run names a real attempt in the durable work ledger;
//   - the work item is in THIS workspace and belongs to THIS acting agent (the
//     one the internal token's binding plus X-Acting-Agent-Slug resolved, never
//     one the body claimed);
//   - the attempt has not ended, and the item is still in a live state;
//   - the attempt's generation is the item's CURRENT generation, and equals the
//     one the caller declared. generation is bumped on every claim, so a worker
//     that lost its lease carries a stale term and is refused here rather than
//     overwriting the attempt that replaced it (I5).
//
// It queries work_items / work_attempts directly rather than through
// work.Store: internal/memory must not import internal/work (hence the
// closure), and this file must not couple to a package being rewritten on the
// same branch. The tables are fixed by migration
// 20260910200255_durable_work_ledger.sql.
func (h *MemoryMutationHandler) authorizeRun(wsID, agentID, runID string, generation int64) func(context.Context) error {
	return func(ctx context.Context) error {
		var (
			itemWorkspace  string
			itemAgent      string
			itemState      string
			itemGeneration int64
			attemptGen     int64
			endedAt        sql.NullString
		)
		err := h.db.QueryRowContext(ctx, `
			SELECT wi.workspace_id, wi.agent_id, wi.state, wi.generation,
			       wa.generation, wa.ended_at
			  FROM work_attempts wa
			  JOIN work_items wi ON wi.id = wa.work_id
			 WHERE wa.run_id = ?`, runID).
			Scan(&itemWorkspace, &itemAgent, &itemState, &itemGeneration, &attemptGen, &endedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: run %q is not in the work ledger", errMemoryRunNotCurrent, runID)
		}
		if err != nil {
			// Not an authorization decision. Fail the write rather than
			// letting an unavailable ledger read as permission.
			return fmt.Errorf("verify run generation: %w", err)
		}
		if itemWorkspace != wsID || itemAgent != agentID {
			return fmt.Errorf("%w: run %q belongs to another agent or workspace", errMemoryRunNotCurrent, runID)
		}
		if endedAt.Valid && endedAt.String != "" {
			return fmt.Errorf("%w: attempt %q has already ended", errMemoryRunNotCurrent, runID)
		}
		if !memoryRunStateIsLive(itemState) {
			return fmt.Errorf("%w: work item is %s", errMemoryRunNotCurrent, itemState)
		}
		if attemptGen != itemGeneration {
			return fmt.Errorf("%w: attempt carries generation %d but the work item is at %d",
				errMemoryRunNotCurrent, attemptGen, itemGeneration)
		}
		if generation != itemGeneration {
			return fmt.Errorf("%w: request declared generation %d but the live term is %d",
				errMemoryRunNotCurrent, generation, itemGeneration)
		}
		return nil
	}
}

// memoryRunStateIsLive is the set of work_items states in which an attempt may
// still write. `waiting` is included because it gives back its execution slot
// while the turn is still open; every terminal state and every pre-start state
// is excluded, as is needs_reconciliation — an item whose runtime may or may
// not be alive is precisely the one that must not commit new memory.
func memoryRunStateIsLive(state string) bool {
	switch state {
	case "starting", "running", "waiting":
		return true
	}
	return false
}
