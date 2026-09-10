package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/memory/memdiff"
	"github.com/crewship-ai/crewship/internal/safepath"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// MemoryWriteRequest is the JSON body of POST /memory/write.
//
// File is a relative path under the chosen memory tier; the handler
// validates that the resolved path stays inside the tier's basePath
// (no `..` traversal). Allowed shapes:
//
//	"AGENT.md"
//	"CREW.md"
//	"daily/2026-05-16.md"
//	"pins.md"
//
// Content is the full UTF-8 payload; the writer treats it opaquely
// other than the cap + scrubber checks.
//
// Scope ∈ {"agent", "crew"} selects the underlying memory engine.
// Defaults to "agent" for backward compatibility with the read
// endpoints' convention.
type MemoryWriteRequest struct {
	File    string `json:"file"`
	Content string `json:"content"`
	Scope   string `json:"scope,omitempty"`

	// Mode ∈ {"replace", "append"}. Defaults to "replace", which is what
	// this endpoint has always done — before this field existed EVERY write
	// here was an unconditional whole-file overwrite, with no read of the
	// current content at all. Two agents writing the same file through this
	// endpoint lost one of the two writes with no record that it happened.
	Mode string `json:"mode,omitempty"`

	// OperationID is stable across retries of the same logical write.
	// Recorded on every write; it only DEDUPLICATES where a mutation ledger
	// is reachable, which it is not from inside an agent container — see
	// the ledgerless note in internal/memory/mutate.go. RevisionChecked in
	// the response says which of the two happened.
	OperationID string `json:"operation_id,omitempty"`

	// ExpectedSHA256 is the compare-and-set that works without the ledger:
	// the content_sha256 GET /memory/read returned must still be the hash of
	// the file. A mismatch is 409 Conflict, not a silent overwrite.
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`

	// Removals are the line spans a replace deletes, in the base's
	// normalised 1-based numbering ({start_line, line_count, old_sha256}).
	// Absent means "not declared" and the write is unverified; an EMPTY
	// array means "this replace deletes nothing" and is checked — see
	// buildTarget in internal/memory/mutate.go on why the distinction is
	// load-bearing.
	//
	// NOT omitempty, and that is the whole point: `omitempty` drops an empty
	// slice on the way OUT, so a Go client marshalling this struct with a
	// deliberate "this replace deletes nothing" declaration would send no
	// removals field at all and have it read back as "undeclared" — silently
	// turning the strongest statement the caller can make into the absence of
	// one, and demoting the write out of the guaranteed profile.
	Removals []memdiff.Removal `json:"removals"`

	// ExpectedRevision is §8's ledger CAS. It means something only on the
	// HOST path (memoryHostMutationPath): the revision anchor lives in the
	// host database, which nothing in this container can reach. Supply the
	// value GET /memory/read reported — or omit it and supply ExpectedSHA256
	// instead, in which case this handler derives the revision from the host
	// under that exact hash (see hostExpectedRevision), which is what makes
	// the guaranteed profile usable without a second sidecar route.
	ExpectedRevision int64 `json:"expected_revision,omitempty"`

	// RunID and Generation are I4's fencing pair. The host does not merely
	// record them: it refuses the write unless the run is this agent's, still
	// live, and carrying the current generation, so supplying a run the
	// caller does not own buys nothing. Absent here means the host cannot
	// verify anything and the write takes the declared legacy path.
	RunID      string `json:"run_id,omitempty"`
	Generation int64  `json:"generation,omitempty"`

	// RequireGuaranteed makes the downgrade the caller's decision rather
	// than this handler's. With it set, a write that cannot reach the host
	// ledger is refused (503) instead of being served under the legacy
	// profile; without it the fallback below applies and the response says
	// so. Either way the outcome is declared, never silent.
	RequireGuaranteed bool `json:"require_guaranteed,omitempty"`
}

// MemoryWriteResponse is the success envelope (201 Created).
type MemoryWriteResponse struct {
	BytesWritten int    `json:"bytes_written"`
	Path         string `json:"path"`

	// ContentSHA256 hashes the exact bytes now on disk — the value to pass
	// back as ExpectedSHA256 on the next write of this file.
	ContentSHA256 string `json:"content_sha256"`
	// Revision is §8's monotonic per-key revision, and RevisionChecked says
	// whether it means anything. Inside an agent container there is no
	// handle to the mutation ledger, so Revision is 0 and RevisionChecked is
	// false; a caller must not describe such a write as revision-checked.
	Revision        int64 `json:"revision"`
	RevisionChecked bool  `json:"revision_checked"`
	// MergedFinalLine reports an append onto a file that did not end in a
	// newline: the first appended bytes joined the previous last line rather
	// than starting a new one.
	MergedFinalLine bool `json:"merged_final_line,omitempty"`
	// OperationID echoes the id this write was recorded under, synthesised
	// when the request did not carry one.
	OperationID string `json:"operation_id"`

	// Profile names the guarantee this write was ACTUALLY made under —
	// "guaranteed" when the host ledger performed it, "legacy" when the
	// in-container path did. It is reported rather than assumed so that no
	// caller can describe an unchecked write as checked; RevisionChecked
	// above is the same statement in boolean form and the two always agree.
	Profile string `json:"profile"`

	// Degraded and DegradedReason are the declared fallback. Degraded is
	// true exactly when the caller could have had the guaranteed profile and
	// did not, and the reason names what stopped it. A silent downgrade
	// would be the failure this whole path exists to remove.
	Degraded       bool   `json:"degraded,omitempty"`
	DegradedReason string `json:"degraded_reason,omitempty"`

	// MutationID / BaseRevision / Idempotent are the ledger's own record of
	// this write, populated only on the guaranteed path. Idempotent means the
	// host returned a PRIOR operation's stored result and wrote nothing.
	MutationID   string `json:"mutation_id,omitempty"`
	BaseRevision int64  `json:"base_revision,omitempty"`
	Idempotent   bool   `json:"idempotent,omitempty"`
}

// MemoryWriteConflict is the 409 envelope for the §8 error vocabulary:
// memory_conflict, undeclared_removal, operation_conflict.
type MemoryWriteConflict struct {
	Error         string `json:"error"`
	Code          string `json:"code"`
	CurrentSHA256 string `json:"current_sha256,omitempty"`
	Message       string `json:"message,omitempty"`
}

// MemoryWriteRejection is the 422 envelope when the writer refuses
// the payload because of a cap or scrubber catch.
type MemoryWriteRejection struct {
	Rejected bool           `json:"rejected"`
	Kind     string         `json:"kind"`              // "cap" | "scrubber"
	Detail   map[string]any `json:"detail,omitempty"`  // bytes_attempted, bytes_limit, current_size — kind-specific
	Hits     []scrubber.Hit `json:"hits,omitempty"`    // populated for kind=scrubber
	Message  string         `json:"message,omitempty"` // human-readable summary
}

// memoryWriteCaps maps a relative file path to its byte ceiling.
// AGENT.md / CREW.md get the 4000-char ceiling (the per-tier prompt
// budget the orchestrator allocates each); pins.md gets a larger
// 8000 because it accumulates curated entries forever; daily logs
// are bounded per-day by dailyCap below (30 KB, matching the native
// dispatcher's capDailyBytes in internal/memory/tools.go).
//
// 0 means "no per-call cap" — but the handler still wraps these
// in WriteConfig.MaxBytes so a misconfigured tier is fail-safe.
var memoryWriteCaps = map[string]int{
	"AGENT.md": 4000,
	"CREW.md":  4000,
	"pins.md":  8000,
}

// dailyCap is the byte ceiling applied to anything under daily/.
// PR-A F1 lowered this from 100 KB → 30 KB so a single day's daily
// file no longer consumes ~20% of a 200k context window if loaded
// into the boot snapshot. The new native memory dispatcher
// (internal/memory/tools.go capDailyBytes) holds the same value as
// the single source of truth — keep these in sync until the legacy
// /memory/write HTTP path is retired. Soft-cap warning at 80% lives
// only on the native dispatcher path; the legacy HTTP surface is
// deprecated (agents stopped calling it via curl after PR-Z Z.1).
const dailyCap = 30_000

// maxMemoryWriteRequestBytes caps the inbound JSON body for
// handleMemoryWrite. dailyCap is the largest legitimate `content`
// payload; +16 KB covers JSON framing (field names, escapes, scope/
// allowlist overhead) so the wrapper never rejects valid daily-log
// writes. Anything past this is either a misuse or an attempt to
// pressure the sidecar by allocating a giant body before the cap
// gate would have caught it later.
const maxMemoryWriteRequestBytes int64 = dailyCap + 16_384

// handleMemoryWrite handles POST /memory/write. Returns:
//
//	201 Created   on success (path persisted)
//	400 Bad Request on malformed JSON / missing fields / illegal path
//	403 Forbidden on path-traversal attempts
//	422 Unprocessable Entity on scrubber or cap rejection (structured envelope)
//	503 Service Unavailable on missing memory engine for scope
func (s *Server) handleMemoryWrite(w http.ResponseWriter, r *http.Request) {
	// Cap the request body size BEFORE json.Decoder allocates it.
	// dailyCap (100 KB) is the largest legitimate content; add some
	// JSON framing slack so the limit doesn't reject valid payloads
	// at the byte boundary. Without this guard a localhost client
	// could POST gigabytes and force the sidecar to allocate them
	// in memory just to fail at the cap check later.
	r.Body = http.MaxBytesReader(w, r.Body, maxMemoryWriteRequestBytes)
	var req MemoryWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSONResponse(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": "request body exceeds size limit",
			})
			return
		}
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.File == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "file is required"})
		return
	}
	if req.Scope == "" {
		req.Scope = "agent"
	}

	engine, basePath, valid := s.resolveMemoryEngineWithPath(req.Scope, r)
	if !valid {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid scope: use agent or crew"})
		return
	}
	if engine == nil || basePath == "" {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": req.Scope + " memory engine not available"})
		return
	}

	target, err := safeJoinUnder(basePath, req.File)
	if err != nil {
		// Path traversal / illegal path; do NOT echo back the
		// resolved path — the rejection itself is the signal.
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "illegal file path"})
		return
	}

	// Reject paths outside the known whitelist. memoryWriteCaps is
	// keyed by the EXACT cleaned relative path — not by basename —
	// so a request like "foo/AGENT.md" or "nested/pins.md" is
	// rejected. The earlier basename-only check let nested forgeries
	// through (foo/AGENT.md basenamed to AGENT.md and inherited its
	// cap). For daily/*, require exactly one slash so a path like
	// "daily/x/y.md" is also refused.
	cap, known := memoryFileCap(req.File)
	if !known {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "unsupported file path; allowed: AGENT.md, CREW.md, pins.md, daily/<name>.md",
		})
		return
	}

	if req.Mode == "" {
		req.Mode = "replace"
	}
	op := memory.OpReplace
	switch req.Mode {
	case "replace":
	case "append":
		op = memory.OpAppend
	default:
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "mode must be 'replace' or 'append'",
		})
		return
	}

	operationID := req.OperationID
	if strings.TrimSpace(operationID) == "" {
		var err error
		operationID, err = memory.NewOperationID()
		if err != nil {
			s.logger.Error("memory write: operation id", "error", err)
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "write failed"})
			return
		}
	}

	// ── The §8 guaranteed profile, and the DECLARED fallback ──────────────
	//
	// memory.ProfileGuaranteed refuses any write lacking a caller-supplied
	// operation id, a durable ledger handle, ExpectedRevision + non-nil
	// Removals on a replace, or a wired Authorize. Nothing in this container
	// can supply the second: `grep 'sql.DB' internal/sidecar/*.go` returns
	// nothing, and it cannot be made to return something — the host database
	// is on the other side of the container boundary. So the write is
	// FORWARDED: POST /api/v1/internal/memory/mutation performs it host-side,
	// under the real ledger, on the host end of the same bind mount this
	// process writes through (internal/api/memory_mutation.go).
	//
	// The fallback is a stated contract, not an accident:
	//
	//   host reachable and the request meets the profile
	//       → the write happens under ProfileGuaranteed and the response
	//         reports a real revision with revision_checked: true.
	//   host reachable and REFUSES (409 conflict, 422 policy, 400, 403)
	//       → the refusal is relayed verbatim. This is NOT a fallback: a
	//         conflict the host detected is the contract working, and
	//         retrying it locally under the legacy profile would perform
	//         exactly the lost update the CAS just prevented.
	//   host unreachable, or the request cannot meet the profile
	//       → the write is served by the in-container legacy path AND the
	//         response says so: profile "legacy", revision_checked false,
	//         degraded true, degraded_reason naming what stopped it.
	//
	// Why degrade rather than fail closed by default: the legacy path is
	// exactly today's behaviour, and refusing an agent's memory write because
	// crewshipd is restarting would discard content the agent cannot get back.
	// The response is explicit enough that nothing downstream can describe
	// such a write as revision-checked, which is the property that matters. A
	// caller that would rather lose the write than lose the guarantee sets
	// require_guaranteed and gets a 503 instead.
	degradeReason := s.hostMutationBlocker(r, req, op)
	if degradeReason == "" {
		// Screen with THIS sidecar's scrubber before forwarding. The host runs
		// its own default scrubber inside Mutate, but only this process has the
		// crew's credential literals registered (registerCredentialLiterals in
		// server.go), so skipping this would make the guaranteed path the
		// weaker of the two on the one check that is agent-specific.
		if rejection := s.screenBeforeHostMutation(req.Content); rejection != nil {
			s.emitJournal(r.Context(), "memory.write_rejected",
				"write rejected: "+rejection.Kind,
				map[string]any{
					"scope":        req.Scope,
					"file":         req.File,
					"reason":       rejection.Kind,
					"hits":         len(rejection.Hits),
					"operation_id": operationID,
				}, nil)
			writeJSONResponse(w, http.StatusUnprocessableEntity, *rejection)
			return
		}
		out := s.mutateOnHost(r, req, op, operationID)
		switch {
		case out.relay != nil:
			// The host's own §8 envelope, byte for byte. Re-encoding it here
			// would be a second vocabulary for the same failures.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(out.relay.status)
			_, _ = w.Write(out.relay.body)
			return
		case out.ok:
			s.finishMemoryWrite(w, r, engine, req.Scope, req.File, MemoryWriteResponse{
				BytesWritten:    out.res.BytesWritten,
				Path:            target,
				ContentSHA256:   out.res.ContentSHA256,
				Revision:        out.res.Revision,
				RevisionChecked: out.res.LedgerRecorded,
				OperationID:     operationID,
				Profile:         out.res.Profile,
				MutationID:      out.res.MutationID,
				BaseRevision:    out.res.BaseRevision,
				Idempotent:      out.res.Idempotent,
			})
			return
		default:
			degradeReason = out.degradeReason
		}
	}
	if req.RequireGuaranteed {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "guaranteed memory profile unavailable: " + degradeReason,
			"code":   "memory_profile_unmet",
			"detail": "require_guaranteed was set, so this write was refused rather than served under the legacy profile",
		})
		return
	}

	cfg := memory.WriteConfig{
		MaxBytes:     cap,
		Scrubber:     s.memoryScrubber(),
		ScrubberMode: scrubber.ModeBlock,
	}

	// memory.Mutate, not memory.WriteFile. Three things change, and all three
	// were real defects rather than style:
	//
	//   1. WriteFile checked the cap BEFORE taking the lock (writer.go:99 vs
	//      :183), so the cap was never safe against a concurrent writer on
	//      this path — only on the MCP tool path, which had its own lock.
	//   2. Every write here was a whole-file replace with no read of the
	//      current content, so an "append a line to my daily log" through
	//      this endpoint was expressed as "overwrite the whole file", and a
	//      concurrent write was lost silently. mode=append now means append.
	//   3. There was no CAS of any kind. ExpectedSHA256 gives one that works
	//      without a database, which matters because there is no database
	//      reachable from inside the container.
	//
	// The ledger is nil here on purpose and the response says so: the sidecar
	// runs inside the agent container and holds no handle to the host
	// database. Revisions, operation-id idempotency and crash recovery are
	// therefore NOT in force on this path. See the ledgerless section of
	// internal/memory/mutate.go.
	res, err := memory.Mutate(r.Context(), nil, memory.MutateRequest{
		// Legacy, declared rather than defaulted. The sidecar runs inside the
		// agent container and has no handle on the host ledger, so it cannot
		// offer revisions, operation-id idempotency, crash recovery or
		// run/generation verification. Reaching the guaranteed profile from
		// here needs a host mutation endpoint to call; until that exists this
		// says what it is, and every response carries revision_checked: false.
		Profile:        memory.ProfileLegacy,
		OperationID:    operationID,
		ActorType:      "agent",
		ActorID:        s.memoryAgentSlug,
		Source:         "sidecar:/memory/write",
		Scope:          req.Scope,
		Path:           target,
		AuditPath:      req.Scope + ":" + req.File,
		Op:             op,
		Content:        req.Content,
		ExpectedSHA256: req.ExpectedSHA256,
		Removals:       req.Removals,
		// A replace discards the base, so importing a non-canonical one
		// changes no bytes on disk; it only lets a declared-removal check
		// diff against a normalised base instead of refusing outright.
		Import: op == memory.OpReplace,
		Cfg:    cfg,
	})
	if err != nil {
		if code, ok := memoryConflictCode(err); ok {
			s.emitJournal(r.Context(), "memory.write_rejected",
				"write rejected: "+code,
				map[string]any{
					"scope":        req.Scope,
					"file":         req.File,
					"reason":       code,
					"operation_id": operationID,
				}, nil)
			writeJSONResponse(w, http.StatusConflict, MemoryWriteConflict{
				Error:         code,
				Code:          code,
				CurrentSHA256: res.BaseSHA256,
				Message:       err.Error(),
			})
			return
		}
		if errors.Is(err, memory.ErrNotCanonical) || errors.Is(err, memory.ErrContentTooLarge) {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.logger.Error("memory write failed", "error", err, "scope", req.Scope, "file", req.File)
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "write failed"})
		return
	}

	if res.Rejection != nil {
		s.emitJournal(r.Context(), "memory.write_rejected",
			"write rejected: "+res.Rejection.Kind,
			map[string]any{
				"scope":  req.Scope,
				"file":   req.File,
				"reason": res.Rejection.Kind,
				"detail": res.Rejection.Detail,
				"hits":   len(res.Rejection.Hits),
			}, nil)
		writeJSONResponse(w, http.StatusUnprocessableEntity, MemoryWriteRejection{
			Rejected: true,
			Kind:     res.Rejection.Kind,
			Detail:   res.Rejection.Detail,
			Hits:     res.Rejection.Hits,
			Message:  res.Rejection.Kind + " policy rejected this write",
		})
		return
	}

	// The legacy in-container write. It is what the fallback contract above
	// resolves to, and its result says so: no ledger, no revision, no
	// operation-id idempotency, no run/generation verification.
	s.finishMemoryWrite(w, r, engine, req.Scope, req.File, MemoryWriteResponse{
		BytesWritten:    res.BytesWritten,
		Path:            target,
		ContentSHA256:   res.ContentSHA256,
		Revision:        res.Revision,
		RevisionChecked: res.LedgerRecorded,
		MergedFinalLine: res.MergedFinalLine,
		OperationID:     operationID,
		Profile:         string(memory.ProfileLegacy),
		Degraded:        degradeReason != "",
		DegradedReason:  degradeReason,
	})
}

// finishMemoryWrite is the shared tail of BOTH write paths: the follow-on side
// effects and the 201. Factored out when the host path landed so the guaranteed
// and legacy writes cannot drift on which of them reindexes and which emits —
// a divergence there is invisible until search silently stops finding what an
// agent just wrote.
//
// The write itself is durable on disk at this point (on this side of the bind
// mount for the legacy path, on the host's side for the guaranteed one — same
// inode either way), so the 201 is honest the moment we return it. The two
// side effects — (1) an immediate FTS reindex so search hits the new content
// without waiting for the watcher debounce, and (2) the memory.updated journal
// emit — are pushed onto the single-worker, strict-FIFO background executor so
// a slow SQLite reindex or a laggy IPC round-trip can't delay the response the
// agent is blocked on. Ordering across turns is preserved: turn N's reindex
// always lands before turn N+1's (memory_executor.go).
//
// We must NOT capture r.Context() in the closure — it is cancelled the instant
// this handler returns, which is before the off-thread task runs. Capture only
// stable values and build a fresh bounded context inside.
func (s *Server) finishMemoryWrite(w http.ResponseWriter, r *http.Request, engine *memory.Engine, scope, file string, resp MemoryWriteResponse) {
	bytesWritten := resp.BytesWritten
	enqueued := s.memoryExec != nil && s.memoryExec.submit(func() {
		ctx, cancel := context.WithTimeout(context.Background(), memoryReindexTimeout)
		defer cancel()

		// Reindex on next watcher tick is the normal path; trigger an
		// immediate INCREMENTAL reindex too so search hits the new content
		// without debounce lag — the writer is the in-process consumer here.
		// Only the file that just changed is re-chunked (finding P2): the
		// old full-corpus ReindexContext here cost O(corpus) per write, so N
		// writes against a growing corpus amplified to O(N x corpus).
		if _, err := engine.ReindexPath(ctx, file); err != nil {
			s.logger.Warn("post-write reindex failed", "error", err, "scope", scope)
		}

		// memory.updated is emitted only on the agent-initiated write
		// path (this handler). fsnotify-detected writes by the
		// consolidator or external tools intentionally do NOT emit
		// this entry — the nudge counter in buildNudgeBlock measures
		// agent-initiated activity, and any fsnotify emission would
		// loop back into "the agent just wrote, reset the nudge",
		// preventing the threshold from ever firing.
		s.emitJournal(ctx, "memory.updated",
			"agent wrote "+file,
			map[string]any{
				"scope":         scope,
				"file":          file,
				"bytes_written": bytesWritten,
				"profile":       resp.Profile,
				"revision":      resp.Revision,
			}, nil)
	})
	if !enqueued {
		// Executor is absent (degraded/test) or already shutting down.
		// Fall back to a synchronous reindex + emit so the index and the
		// journal don't silently diverge from disk on this path. This
		// runs on the request goroutine but only when the off-thread
		// path is unavailable, so the common case stays non-blocking.
		if _, err := engine.ReindexPath(r.Context(), file); err != nil {
			s.logger.Warn("post-write reindex failed (sync fallback)", "error", err, "scope", scope)
		}
		s.emitJournal(r.Context(), "memory.updated",
			"agent wrote "+file,
			map[string]any{
				"scope":         scope,
				"file":          file,
				"bytes_written": bytesWritten,
				"profile":       resp.Profile,
				"revision":      resp.Revision,
			}, nil)
	}

	writeJSONResponse(w, http.StatusCreated, resp)
}

// memoryConflictCode maps the §8 error vocabulary onto the string the HTTP
// client branches on. Returns ok=false for anything that is not one of the
// three conflict codes, so an unrelated failure cannot be reported as a
// conflict the client is supposed to retry differently.
func memoryConflictCode(err error) (string, bool) {
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

// memoryReindexTimeout bounds the off-thread reindex + journal emit so a
// hung SQLite operation can't pin the background worker forever (which
// would stall every subsequent turn's reindex behind it). Generous
// enough for a large daily-log reindex, tight enough that a wedged FTS5
// write surfaces as a logged warning rather than a permanent stall.
const memoryReindexTimeout = 30 * time.Second

// resolveMemoryEngineWithPath returns the engine + its base path for
// the given scope so handlers can validate paths and call WriteFile.
// scope="agent" resolves to the ACTING agent's own tier — see
// resolveMemoryEngine and peerMemoryEngineFor (#1301). Same valid-scope
// semantics as resolveMemoryEngine.
func (s *Server) resolveMemoryEngineWithPath(scope string, r *http.Request) (*memory.Engine, string, bool) {
	switch scope {
	case "agent", "":
		slug, err := s.legacyMemoryEffectiveSlug(r)
		var (
			engine   *memory.Engine
			basePath string
		)
		if err == nil {
			engine, basePath, err = s.peerMemoryEngineFor(r.Context(), slug)
		}
		if err != nil {
			s.logger.Error("memory: peer engine unavailable", "error", err)
			return nil, "", true
		}
		return engine, basePath, true
	case "crew":
		return s.crewMemoryEngine, s.crewMemoryBase, true
	default:
		return nil, "", false
	}
}

// memoryScrubber returns the sidecar's shared scrubber instance, or
// nil if scrubbing is disabled (the writer treats nil as "skip
// scrubber stage"). The CREWSHIP_MEMORY_SCRUBBER_MODE env var lets
// operators downgrade to warn-only globally; today the handler
// always uses ModeBlock — the warn-mode plumbing lands with the
// per-workspace override in a follow-up.
func (s *Server) memoryScrubber() *scrubber.Scrubber {
	if s == nil {
		return nil
	}
	return s.scrubber
}

// safeJoinUnder validates that `rel` resolves to a path inside `base`
// after cleaning. Rejects absolute paths, `..` traversal, and any
// other escape attempt with errIllegalPath.
//
// The cleaning is done on the joined path (not on `rel` alone) so a
// caller passing `foo/../../etc/passwd` doesn't slip through by
// normalising "foo/.." away in isolation.
func safeJoinUnder(base, rel string) (string, error) {
	// Lexical confinement: safepath.JoinRel rejects absolute paths, NUL
	// bytes, embedded separators, and any "../" traversal, and guarantees
	// the returned value is base or a descendant of base. This is the
	// single shared guard (also used by the in-process memory dispatcher)
	// so the read/write HTTP surface and the tool surface stay
	// byte-for-byte consistent.
	cleaned, err := safepath.JoinRel(base, rel)
	if err != nil {
		return "", errIllegalPath
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absCleaned, err := filepath.Abs(cleaned)
	if err != nil {
		return "", err
	}

	// Final-component symlink guard (finding MEM, 2026-06 audit). The agent
	// (uid 1001) writes into the same base the sidecar (uid 1002) reads from.
	// filepath.Clean is purely textual, so an agent that plants the requested
	// file ITSELF as a symlink (e.g. AGENT.md -> /sidecar-private/secret) still
	// passes the prefix check above — its parent resolves inside base, only the
	// leaf escapes — and the downstream os.ReadFile/os.WriteFile would then
	// follow it across the UID boundary (confused deputy). lstat (which does
	// NOT follow the link) the leaf and reject any symlink outright: none of
	// the whitelisted memory files is ever legitimately a symlink. lstat also
	// catches a DANGLING symlink that an EvalSymlinks-of-the-full-path check
	// would miss (it returns ENOENT and looks like a fresh first write).
	if fi, lerr := os.Lstat(absCleaned); lerr == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", errIllegalPath
		}
	} else if !os.IsNotExist(lerr) {
		return "", lerr
	}

	// Parent-directory symlink-escape guard: a symlink planted as a PARENT
	// component inside base that points outside also passes the textual prefix
	// check. Resolve the parent dir's real path (the file itself may not exist
	// yet on first write) and verify it stays inside base.
	parent := filepath.Dir(absCleaned)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		// On first write the parent may not exist; fall back to
		// returning the (already prefix-checked) cleaned path. The
		// downstream MkdirAll will create it inside base, so the
		// symlink-escape window is closed for the create case.
		if os.IsNotExist(err) {
			return cleaned, nil
		}
		return "", err
	}
	resolvedBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(resolved, resolvedBase+string(filepath.Separator)) && resolved != resolvedBase {
		return "", errIllegalPath
	}
	return cleaned, nil
}

// isAllowedMemoryFile reports whether rel names one of the whitelisted
// memory files: AGENT.md, CREW.md, pins.md, or daily/<name>.md (a single
// segment under daily/). It mirrors the cap lookup performed in
// handleMemoryWrite so the read and write surfaces share one allowlist
// (finding MEM follow-up: the read path must be no more permissive than the
// write path). Returns the byte cap for the write path; callers that only
// need the allow decision can ignore it.
func memoryFileCap(rel string) (int, bool) {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if c, known := memoryWriteCaps[clean]; known {
		return c, true
	}
	// daily/<name>.md — exactly one segment under daily/, and it must carry
	// the .md suffix so the allowlist stays as tight as the advertised
	// surface (rejects daily/token, daily/.env, etc.).
	if rest, ok := strings.CutPrefix(clean, "daily/"); ok &&
		!strings.Contains(rest, "/") &&
		strings.HasSuffix(rest, ".md") &&
		strings.TrimSuffix(rest, ".md") != "" {
		return dailyCap, true
	}
	return 0, false
}

var errIllegalPath = errors.New("illegal file path")

// ── The host mutation forward ────────────────────────────────────────────
//
// Everything below is the client half of internal/api/memory_mutation.go. It
// exists because the guaranteed profile needs a *sql.DB and this process will
// never have one; the host does, so the write travels rather than the database.

// memoryHostMutationPath and memoryHostCanonicalPath are the two host routes.
// The read is not a convenience next to the write: a client cannot supply
// expected_revision without having been told the revision, and the revision
// anchor lives in the host database. Without the read the guaranteed profile
// would be reachable in principle and unusable in practice.
const (
	memoryHostMutationPath  = "/api/v1/internal/memory/mutation"
	memoryHostCanonicalPath = "/api/v1/internal/memory/canonical"
)

// hostMutationFencing resolves I4's (run_id, generation) pair.
//
// It comes from the REQUEST, and only from the request. That is not a trust
// hole: the host refuses any run that is not this acting agent's own live
// attempt at the current generation, so naming someone else's run buys nothing,
// and naming your own is exactly what the invariant asks for.
//
// It is deliberately not read from the container environment either. A run id
// is per-attempt and a container outlives many attempts, so an environment
// variable set at container start would carry a stale run for every attempt
// after the first — the precise failure I5's fencing term exists to catch, in
// the one place nothing would catch it.
//
// Nothing in this tree supplies it yet: the durable work ledger's dispatch path
// is still being built and no run id reaches an agent (IPCConfig carries agent,
// crew, workspace and chat, and no run). Until a caller passes one, every write
// through this handler degrades with a reason that says exactly that.
func hostMutationFencing(req MemoryWriteRequest) (runID string, generation int64) {
	return strings.TrimSpace(req.RunID), req.Generation
}

// hostMutationBlocker returns why the guaranteed path cannot be attempted for
// this request, or "" when it can. Every branch is a fact known without any
// network I/O, so a request that cannot possibly qualify never pays a round
// trip to find out.
func (s *Server) hostMutationBlocker(r *http.Request, req MemoryWriteRequest, op memory.MutateOp) string {
	if s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.Token == "" {
		return "this sidecar has no host IPC channel, so the mutation ledger is unreachable"
	}
	if _, ok := s.hybridActingSlug(r); !ok {
		return "the acting agent identity could not be resolved, and the host will not accept a write it cannot attribute"
	}
	if runID, _ := hostMutationFencing(req); runID == "" {
		return "the request carries no work-ledger run id, so invariant I4's run and generation cannot be verified"
	}
	if op == memory.OpReplace {
		if req.Removals == nil {
			return "a guaranteed replace must declare its removals; a missing array means undeclared, an empty one means it deletes nothing"
		}
		if req.ExpectedRevision <= 0 && strings.TrimSpace(req.ExpectedSHA256) == "" {
			return "a guaranteed replace must carry expected_revision, or expected_sha256 for this handler to derive it from the host"
		}
	}
	return ""
}

// screenBeforeHostMutation runs this sidecar's scrubber over the content the
// host is about to be handed. The host runs its own default scrubber inside
// Mutate, but only this process has the crew's credential literals registered
// (registerCredentialLiterals), so without this the guaranteed path would be
// weaker than the legacy one on the one check that is per-crew. Returns nil
// when the content passes.
func (s *Server) screenBeforeHostMutation(content string) *MemoryWriteRejection {
	sc := s.memoryScrubber()
	if sc == nil {
		return nil
	}
	res := sc.Validate(content, scrubber.ModeBlock)
	if res.Decision != scrubber.DecisionReject {
		return nil
	}
	return &MemoryWriteRejection{
		Rejected: true,
		Kind:     "scrubber",
		Hits:     res.Hits,
		Message:  "scrubber policy rejected this write",
	}
}

// hostMutationRelay is a host answer this handler forwards verbatim.
type hostMutationRelay struct {
	status int
	body   []byte
}

// hostMutationSuccess is the decoded 200 from the host mutation route. Field
// names track internal/api.MemoryMutationResponse; the two packages cannot
// share the type (internal/api's own tests import internal/sidecar-adjacent
// code and the edge would be a cycle), so the JSON tags are the contract.
type hostMutationSuccess struct {
	Profile        string `json:"profile"`
	Revision       int64  `json:"revision"`
	ContentSHA256  string `json:"content_sha256"`
	LedgerRecorded bool   `json:"ledger_recorded"`
	BytesWritten   int    `json:"bytes_written"`
	MutationID     string `json:"mutation_id"`
	BaseRevision   int64  `json:"base_revision"`
	Idempotent     bool   `json:"idempotent"`
}

// hostMutationOutcome is exactly one of: performed (ok), refused by the host
// (relay), or not performed at all (degradeReason).
type hostMutationOutcome struct {
	ok            bool
	res           hostMutationSuccess
	relay         *hostMutationRelay
	degradeReason string
}

// hostCanonicalRead is the §8 read, host-side: the revision anchor plus the
// hash of the exact bytes on disk.
type hostCanonicalRead struct {
	Exists         bool   `json:"exists"`
	ContentSHA256  string `json:"content_sha256"`
	Revision       int64  `json:"revision"`
	LedgerRecorded bool   `json:"ledger_recorded"`
	Drift          bool   `json:"drift"`
}

// mutateOnHost performs the forward. It never falls back on its own: it
// reports which of the three outcomes happened and the caller applies the
// declared contract.
func (s *Server) mutateOnHost(r *http.Request, req MemoryWriteRequest, op memory.MutateOp, operationID string) hostMutationOutcome {
	expectedRevision := req.ExpectedRevision
	if op == memory.OpReplace && expectedRevision <= 0 {
		// The loop-closing read. The caller told us the hash it diffed
		// against; ask the host which revision that hash IS. If the file has
		// moved on since the caller read it the hashes disagree and this is a
		// conflict here, before any write — the same answer the ledger CAS
		// would give, one round trip earlier.
		canon, out, ok := s.readCanonicalOnHost(r, req)
		if !ok {
			return out
		}
		if canon.ContentSHA256 != strings.TrimSpace(req.ExpectedSHA256) {
			body, _ := json.Marshal(MemoryMutationConflictEnvelope{
				Error:           "memory_conflict",
				Code:            "memory_conflict",
				CurrentRevision: canon.Revision,
				CurrentSHA256:   canon.ContentSHA256,
				Message: "expected_sha256 does not match the canonical content; re-read and propose again " +
					"under a NEW operation id",
			})
			return hostMutationOutcome{relay: &hostMutationRelay{status: http.StatusConflict, body: body}}
		}
		if canon.Revision <= 0 {
			// internal/memory's guaranteed profile requires a NON-ZERO
			// ExpectedRevision on a replace, and revision 0 means no contract
			// write has ever anchored this key. The first write to a key must
			// therefore be an append (which is guaranteed-eligible with
			// revision 0) — this is a property of the committed §8 contract in
			// internal/memory/mutate.go, not something this handler can paper
			// over, so it is reported rather than worked around.
			return hostMutationOutcome{degradeReason: "no revision anchor exists for this key yet; " +
				"the guaranteed profile cannot CAS a first replace — append once to establish revision 1"}
		}
		expectedRevision = canon.Revision
	}

	runID, generation := hostMutationFencing(req)
	body, err := json.Marshal(map[string]any{
		"operation_id":      operationID,
		"scope":             req.Scope,
		"file":              req.File,
		"op":                string(op),
		"content":           req.Content,
		"expected_revision": expectedRevision,
		"expected_sha256":   req.ExpectedSHA256,
		"removals":          req.Removals,
		"run_id":            runID,
		"generation":        generation,
		"source":            "sidecar:/memory/write",
	})
	if err != nil {
		return hostMutationOutcome{degradeReason: "the mutation request could not be encoded: " + err.Error()}
	}

	resp, err := s.memoryHostRequest(r, http.MethodPost, memoryHostMutationPath, body)
	if err != nil {
		return hostMutationOutcome{degradeReason: "the host mutation endpoint is unreachable: " + err.Error()}
	}
	switch {
	case resp.status == http.StatusOK:
		var ok hostMutationSuccess
		if err := json.Unmarshal(resp.body, &ok); err != nil {
			// The host says it wrote; we cannot read what it says. Degrading
			// here would write the same content a second time.
			return hostMutationOutcome{relay: &hostMutationRelay{
				status: http.StatusBadGateway,
				body:   []byte(`{"error":"the host performed the mutation but its response could not be decoded"}`),
			}}
		}
		return hostMutationOutcome{ok: true, res: ok}
	case resp.status == http.StatusNotFound:
		// An older host without the route. Unambiguous, and the only status
		// that means "nothing happened over there".
		return hostMutationOutcome{degradeReason: "this host does not serve " + memoryHostMutationPath +
			" (older crewshipd), so the mutation ledger is out of reach"}
	default:
		// Everything else — 400, 403, 409, 422, 5xx — is relayed. A conflict
		// the host detected is the contract WORKING, and re-running it here
		// under the legacy profile would perform exactly the lost update the
		// CAS just prevented. A 5xx is relayed for the same reason in the
		// other direction: the host may have written, and a legacy retry could
		// append the same content twice.
		return hostMutationOutcome{relay: &hostMutationRelay{status: resp.status, body: resp.body}}
	}
}

// MemoryMutationConflictEnvelope is the §8 conflict shape, matching the host's
// own so a client branches on one vocabulary regardless of which side detected
// the conflict.
type MemoryMutationConflictEnvelope struct {
	Error           string `json:"error"`
	Code            string `json:"code"`
	CurrentRevision int64  `json:"current_revision"`
	CurrentSHA256   string `json:"current_sha256,omitempty"`
	Message         string `json:"message,omitempty"`
}

// readCanonicalOnHost fetches the revision anchor for (scope, file). A failure
// is returned as a degrade outcome rather than an error so the caller's three
// outcomes stay the only three.
func (s *Server) readCanonicalOnHost(r *http.Request, req MemoryWriteRequest) (hostCanonicalRead, hostMutationOutcome, bool) {
	q := url.Values{}
	q.Set("scope", req.Scope)
	q.Set("file", req.File)
	resp, err := s.memoryHostRequest(r, http.MethodGet, memoryHostCanonicalPath+"?"+q.Encode(), nil)
	if err != nil {
		return hostCanonicalRead{}, hostMutationOutcome{
			degradeReason: "the host canonical read is unreachable, so expected_revision cannot be derived: " + err.Error(),
		}, false
	}
	if resp.status == http.StatusNotFound {
		return hostCanonicalRead{}, hostMutationOutcome{
			degradeReason: "this host does not serve " + memoryHostCanonicalPath + " (older crewshipd)",
		}, false
	}
	if resp.status != http.StatusOK {
		return hostCanonicalRead{}, hostMutationOutcome{
			relay: &hostMutationRelay{status: resp.status, body: resp.body},
		}, false
	}
	var canon hostCanonicalRead
	if err := json.Unmarshal(resp.body, &canon); err != nil {
		return hostCanonicalRead{}, hostMutationOutcome{
			degradeReason: "the host canonical read could not be decoded: " + err.Error(),
		}, false
	}
	return canon, hostMutationOutcome{}, true
}

// memoryHostIPCTimeout bounds one host round trip. Shorter than the 15 s the
// other IPC forwards use: this one sits directly in front of an agent's memory
// write, and a slow host should degrade to the legacy path (which the response
// declares) rather than stall the turn.
const memoryHostIPCTimeout = 10 * time.Second

// memoryHostResponse is a raw host answer.
type memoryHostResponse struct {
	status int
	body   []byte
}

// memoryHostRequest is the IPC call, carrying BOTH credentials the host route
// needs: X-Internal-Token (which authenticates this sidecar) and
// X-Acting-Agent-Slug (which narrows it to one agent). The slug is derived
// exclusively from the token-resolved acting identity — never from the URL or
// the payload — and the host re-proves it inside the token's own binding, so
// the header can only narrow this sidecar's authority, never widen it.
//
// It does not reuse ipcRequestJSON (pipelines.go) because that helper sends
// only the internal token, and a mutation the host cannot attribute to an
// agent is one it must refuse.
func (s *Server) memoryHostRequest(r *http.Request, method, path string, body []byte) (*memoryHostResponse, error) {
	if s.ipc == nil {
		return nil, errors.New("IPC not configured")
	}
	slug, ok := s.hybridActingSlug(r)
	if !ok {
		return nil, errors.New("acting agent identity could not be resolved")
	}

	ctx, cancel := context.WithTimeout(r.Context(), memoryHostIPCTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, s.ipc.BaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)
	httpReq.Header.Set(actingAgentSlugHeader, slug)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call: %w", err)
	}
	defer resp.Body.Close()
	// Bounded like every other IPC read here: these payloads are small
	// structured JSON, and the cap stops a runaway peer.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return &memoryHostResponse{status: resp.StatusCode, body: raw}, nil
}
