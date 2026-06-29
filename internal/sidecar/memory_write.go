package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
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
}

// MemoryWriteResponse is the success envelope (201 Created).
type MemoryWriteResponse struct {
	BytesWritten int    `json:"bytes_written"`
	Path         string `json:"path"`
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
// are bounded per-day by the engine's DailyMaxKB (defaults to 100
// KB) — the handler defers to that via a 100_000-byte cap.
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

	engine, basePath, valid := s.resolveMemoryEngineWithPath(req.Scope)
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

	cfg := memory.WriteConfig{
		MaxBytes:     cap,
		Scrubber:     s.memoryScrubber(),
		ScrubberMode: scrubber.ModeBlock,
	}

	res, err := memory.WriteFile(r.Context(), target, []byte(req.Content), cfg)
	if err != nil {
		s.logger.Error("memory write failed", "error", err, "scope", req.Scope, "file", req.File)
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "write failed"})
		return
	}

	if res.Rejected {
		s.emitJournal(r.Context(), "memory.write_rejected",
			"write rejected: "+res.RejectionKind,
			map[string]any{
				"scope":  req.Scope,
				"file":   req.File,
				"reason": res.RejectionKind,
				"detail": res.RejectionDetail,
				"hits":   len(res.Hits),
			}, nil)
		writeJSONResponse(w, http.StatusUnprocessableEntity, MemoryWriteRejection{
			Rejected: true,
			Kind:     res.RejectionKind,
			Detail:   res.RejectionDetail,
			Hits:     res.Hits,
			Message:  res.RejectionKind + " policy rejected this write",
		})
		return
	}

	// The write itself is durable on disk at this point, so the 201 is
	// honest the moment we return it. The two follow-on side effects —
	// (1) an immediate FTS reindex so search hits the new content
	// without waiting for the watcher debounce, and (2) the
	// memory.updated journal emit — are pushed onto the single-worker,
	// strict-FIFO background executor so a slow SQLite reindex or a
	// laggy IPC round-trip can't delay the response the agent is
	// blocked on. Ordering across turns is preserved: turn N's reindex
	// always lands before turn N+1's (memory_executor.go).
	//
	// We must NOT capture r.Context() here — it is cancelled the instant
	// this handler returns, which is before the off-thread task runs.
	// Capture only stable values and build a fresh bounded context
	// inside the closure.
	scope := req.Scope
	file := req.File
	bytesWritten := res.BytesWritten
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
			}, nil)
	}

	writeJSONResponse(w, http.StatusCreated, MemoryWriteResponse{
		BytesWritten: bytesWritten,
		Path:         target,
	})
}

// memoryReindexTimeout bounds the off-thread reindex + journal emit so a
// hung SQLite operation can't pin the background worker forever (which
// would stall every subsequent turn's reindex behind it). Generous
// enough for a large daily-log reindex, tight enough that a wedged FTS5
// write surfaces as a logged warning rather than a permanent stall.
const memoryReindexTimeout = 30 * time.Second

// resolveMemoryEngineWithPath returns the engine + its base path for
// the given scope so handlers can validate paths and call WriteFile.
// Same valid-scope semantics as resolveMemoryEngine.
func (s *Server) resolveMemoryEngineWithPath(scope string) (*memory.Engine, string, bool) {
	switch scope {
	case "agent", "":
		return s.memoryEngine, s.agentMemoryBase, true
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
	if rel == "" {
		return "", errIllegalPath
	}
	if filepath.IsAbs(rel) {
		return "", errIllegalPath
	}
	joined := filepath.Join(base, rel)
	cleaned := filepath.Clean(joined)
	// Ensure cleaned begins with base + separator (or equals base
	// for the unlikely empty-rel case which we already rejected).
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absCleaned, err := filepath.Abs(cleaned)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absCleaned, absBase+string(filepath.Separator)) && absCleaned != absBase {
		return "", errIllegalPath
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
