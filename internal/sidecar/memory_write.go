package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

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

// dailyCap is the byte ceiling applied to anything under daily/. The
// engine's per-file daily cap default is 100 KB; the writer mirrors
// that.
const dailyCap = 100_000

// handleMemoryWrite handles POST /memory/write. Returns:
//
//	201 Created   on success (path persisted)
//	400 Bad Request on malformed JSON / missing fields / illegal path
//	403 Forbidden on path-traversal attempts
//	422 Unprocessable Entity on scrubber or cap rejection (structured envelope)
//	503 Service Unavailable on missing memory engine for scope
func (s *Server) handleMemoryWrite(w http.ResponseWriter, r *http.Request) {
	var req MemoryWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	// Reject paths outside the known whitelist. memoryWriteCaps holds
	// the three canonical basenames (AGENT.md / CREW.md / pins.md); any
	// path under daily/ gets dailyCap; everything else is refused at
	// the boundary. The prior fallback of cap=0 was an unbounded write
	// path — any in-base path the caller could construct was accepted
	// with no byte ceiling, defeating the whole reason the caps map
	// exists.
	rel := filepath.ToSlash(filepath.Clean(req.File))
	cap, known := memoryWriteCaps[filepath.Base(rel)]
	if !known {
		if strings.HasPrefix(rel, "daily/") {
			cap = dailyCap
		} else {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{
				"error": "unsupported file path; allowed: AGENT.md, CREW.md, pins.md, daily/*",
			})
			return
		}
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

	// Reindex on next watcher tick is the normal path; trigger an
	// immediate reindex too so search hits the new content without
	// debounce lag — the writer is the in-process consumer here.
	if err := engine.ReindexContext(r.Context()); err != nil {
		s.logger.Warn("post-write reindex failed", "error", err, "scope", req.Scope)
	}

	// memory.updated is emitted only on the agent-initiated write
	// path (this handler). fsnotify-detected writes by the
	// consolidator or external tools intentionally do NOT emit
	// this entry — the nudge counter in buildNudgeBlock measures
	// agent-initiated activity, and any fsnotify emission would
	// loop back into "the agent just wrote, reset the nudge",
	// preventing the threshold from ever firing.
	s.emitJournal(r.Context(), "memory.updated",
		"agent wrote "+req.File,
		map[string]any{
			"scope":         req.Scope,
			"file":          req.File,
			"bytes_written": res.BytesWritten,
		}, nil)

	writeJSONResponse(w, http.StatusCreated, MemoryWriteResponse{
		BytesWritten: res.BytesWritten,
		Path:         target,
	})
}

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
	return cleaned, nil
}

var errIllegalPath = errors.New("illegal file path")

// memoryWriteRequestSilentTimeout is reserved for a future
// per-tier write deadline. Today the handler relies on r.Context()
// from the HTTP server's timeout; this var is here so the deadline
// landing point is obvious to future maintainers.
var _ = context.Background
