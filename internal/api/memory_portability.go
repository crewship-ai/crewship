package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/memport"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// MemoryPortabilityHandler serves the export/import pair behind
// `crewship memory export|import`.
//
// # Where the format work happens
//
// The server reads and writes ONE tree: its own. Recognising a NanoClaw
// or OpenClaw layout happens in the CLI, against files on the
// operator's machine, and the wire carries the already-mapped
// documents. That split keeps a foreign directory layout — the least
// trustworthy input in this feature — off the server entirely, and it
// means the HTTP surface has exactly one shape to validate no matter
// how many source harnesses the CLI learns.
//
// # Authorization
//
// Both directions are OWNER/ADMIN. Export is a read, but it is a read
// of every private note an agent holds, including the operator model's
// peer cards about named people; that is not member-grade data. Import
// is a write into agent memory and is gated the same way.
type MemoryPortabilityHandler struct {
	db             *sql.DB
	logger         *slog.Logger
	outputBasePath string
}

func NewMemoryPortabilityHandler(db *sql.DB, logger *slog.Logger, outputBasePath string) *MemoryPortabilityHandler {
	return &MemoryPortabilityHandler{db: db, logger: logger, outputBasePath: outputBasePath}
}

// memoryDocPayload is one document on the wire. Body is plain markdown,
// not base64: memory is text by construction, and a readable payload is
// the point of the whole feature.
type memoryDocPayload struct {
	Path    string   `json:"path"`
	Tier    string   `json:"tier"`
	Scope   string   `json:"scope,omitempty"`
	Title   string   `json:"title,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Body    string   `json:"body"`
}

type memorySkipPayload struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// resolveMemoryScope turns the request's crew/agent parameters into the
// host directory holding that memory, after proving both belong to the
// caller's workspace. An empty agent slug selects the crew-shared tier.
//
// Returns ("", false) having already written the response when the
// scope is unusable, so callers just return.
func (h *MemoryPortabilityHandler) resolveMemoryScope(w http.ResponseWriter, r *http.Request, crewID, agentSlug string) (string, bool) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return "", false
	}
	if h.outputBasePath == "" {
		replyError(w, http.StatusServiceUnavailable, "storage base path not configured on this instance")
		return "", false
	}
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew_id is required")
		return "", false
	}
	// The path is built from what the DATABASE says exists, never from
	// the string the caller sent — the lookups below re-read the id and
	// the slug and everything downstream uses those copies.
	//
	// safepath.JoinUnder already refuses a traversal component, so this
	// is defence in depth rather than the only guard. It is worth having
	// anyway: it makes the confinement a property of where the value
	// came from instead of a property of a helper three calls away, and
	// it is what lets a reader (and CodeQL, which cannot see through
	// JoinUnder) tell at a glance that no request string reaches a
	// filesystem call.
	var safeCrewID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID).Scan(&safeCrewID)
	if errors.Is(err, sql.ErrNoRows) {
		// 404 rather than 403: a foreign crew id must not be
		// distinguishable from a missing one.
		replyError(w, http.StatusNotFound, "crew not found")
		return "", false
	}
	if err != nil {
		h.logger.Error("memory portability: crew lookup", "err", err)
		replyError(w, http.StatusInternalServerError, "crew lookup failed")
		return "", false
	}

	if agentSlug == "" {
		dir, err := memory.HostCrewMemoryRoot(h.outputBasePath, safeCrewID)
		if err != nil {
			h.logger.Error("memory portability: crew memory root", "err", err)
			replyError(w, http.StatusInternalServerError, "could not resolve crew memory")
			return "", false
		}
		return dir, true
	}

	var safeSlug string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT slug FROM agents WHERE slug = ? AND crew_id = ? AND deleted_at IS NULL`,
		agentSlug, safeCrewID).Scan(&safeSlug)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "agent not found in this crew")
		return "", false
	}
	if err != nil {
		h.logger.Error("memory portability: agent lookup", "err", err)
		replyError(w, http.StatusInternalServerError, "agent lookup failed")
		return "", false
	}
	dir, err := memory.HostAgentMemoryRoot(h.outputBasePath, safeCrewID, safeSlug)
	if err != nil {
		h.logger.Error("memory portability: agent memory root", "err", err)
		replyError(w, http.StatusInternalServerError, "could not resolve agent memory")
		return "", false
	}
	return dir, true
}

// Export serves GET /api/v1/memory/export?crew_id=&agent_slug=.
//
// It returns documents, not a bundle file. Rendering the OKF bundle is
// the CLI's job because the bundle lands on the operator's disk; a
// server that streamed a tarball would be inventing a second on-disk
// format nobody can inspect mid-flight.
func (h *MemoryPortabilityHandler) Export(w http.ResponseWriter, r *http.Request) {
	dir, ok := h.resolveMemoryScope(w, r, r.URL.Query().Get("crew_id"), r.URL.Query().Get("agent_slug"))
	if !ok {
		return
	}

	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		// An agent that has never written memory is not an error; it
		// is an empty export, and saying so beats a 404 the operator
		// has to interpret.
		writeJSON(w, http.StatusOK, map[string]any{
			"format":    string(memport.FormatCrewship),
			"documents": []memoryDocPayload{},
			"skipped":   []memorySkipPayload{},
		})
		return
	}

	// SecureDirFS, not os.DirFS: the tree being read is one the agent
	// owns, so a symlinked .md would otherwise return another crew's
	// memory inside a response scoped to this one.
	scope := memport.ScopeCrew
	if r.URL.Query().Get("agent_slug") != "" {
		scope = memport.ScopeAgent
	}
	plan, err := memport.ReadSource(memport.SecureDirFS(dir), memport.FormatCrewship,
		memport.Options{DefaultScope: scope})
	if err != nil {
		h.logger.Error("memory portability: read", "err", err)
		replyError(w, http.StatusInternalServerError, "could not read memory")
		return
	}

	docs := make([]memoryDocPayload, 0, len(plan.Docs))
	for _, d := range plan.Docs {
		docs = append(docs, memoryDocPayload{
			Path:  d.RelPath,
			Tier:  string(d.Tier),
			Scope: string(d.Scope),
			Title: d.Title,
			Tags:  d.Tags,
			Body:  string(d.Body),
		})
	}
	skipped := make([]memorySkipPayload, 0, len(plan.Skipped))
	for _, s := range plan.Skipped {
		skipped = append(skipped, memorySkipPayload{Source: s.Source, Reason: s.Reason})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"format":    string(plan.Format),
		"documents": docs,
		"skipped":   skipped,
	})
}

// importMaxBodyBytes bounds one import request.
const importMaxBodyBytes = 32 << 20

type memoryImportRequest struct {
	CrewID    string             `json:"crew_id"`
	AgentSlug string             `json:"agent_slug"`
	Documents []memoryDocPayload `json:"documents"`
}

// Import serves POST /api/v1/memory/import.
//
// There is no dry-run flag here on purpose. The plan an operator
// reviews is produced from their own source tree by the CLI before
// anything is sent; a server-side dry run would review a payload that
// has already left the machine where the decision is made.
func (h *MemoryPortabilityHandler) Import(w http.ResponseWriter, r *http.Request) {
	var body memoryImportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, importMaxBodyBytes)).Decode(&body); err != nil {
		// Distinguish "too big" from "malformed": an operator whose
		// bundle exceeded the cap needs to know that is what happened,
		// not go looking for a syntax error that is not there.
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			replyError(w, http.StatusRequestEntityTooLarge,
				"import payload exceeds the size limit for one request; split it")
			return
		}
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Documents) == 0 {
		replyError(w, http.StatusBadRequest, "no documents to import")
		return
	}
	dir, ok := h.resolveMemoryScope(w, r, body.CrewID, body.AgentSlug)
	if !ok {
		return
	}

	docs := make([]memport.Doc, 0, len(body.Documents))
	injected := make([]map[string]any, 0)
	for _, d := range body.Documents {
		tier := memory.Tier(d.Tier)
		if !memory.ValidTier(tier) {
			replyError(w, http.StatusBadRequest, "unknown tier "+d.Tier+" on "+d.Path)
			return
		}
		// The same prompt-injection scan every other memory write runs
		// (audit A10.1, memory/tools.go handleWrite). Foreign memory is
		// the least trustworthy input this feature has; letting it land
		// unscanned means the load-time scan blanks the whole tier at
		// the next run while the payload sits in the FTS index.
		if hit := memory.ScanContent(d.Body); hit != nil {
			injected = append(injected, map[string]any{
				"path":    d.Path,
				"kind":    "prompt_injection",
				"pattern": hit.Category,
			})
			continue
		}
		docs = append(docs, memport.Doc{
			Tier:    tier,
			Scope:   memport.Scope(d.Scope),
			RelPath: d.Path,
			Title:   d.Title,
			Tags:    d.Tags,
			Sources: d.Sources,
			Body:    []byte(d.Body),
		})
	}

	// The write policy is the workspace's, not the importer's: an
	// import that could opt out of the caps and the scrubber would be
	// a way to put anything into memory that the agents' own writes
	// are not allowed to put there.
	cfg, err := h.memoryWriteConfig(r)
	if err != nil {
		h.logger.Error("memory portability: write config", "err", err)
		replyError(w, http.StatusInternalServerError, "could not resolve memory write policy")
		return
	}

	res, err := memport.Apply(r.Context(), dir, docs, cfg)
	if err != nil {
		// Apply only returns an error for something wrong with the
		// TARGET (no root, cannot prepare it) — per-document problems
		// come back in the result. So this is ours, not the caller's,
		// and the message must not carry the host path.
		h.logger.Error("memory portability: apply", "err", err)
		replyError(w, http.StatusInternalServerError, "could not write to this memory directory")
		return
	}

	rejected := make([]map[string]any, 0, len(res.Rejected)+len(injected))
	rejected = append(rejected, injected...)
	for _, rj := range res.Rejected {
		rejected = append(rejected, map[string]any{
			"path":   rj.RelPath,
			"kind":   rj.Kind,
			"detail": rj.Detail,
		})
	}
	failed := make([]map[string]any, 0, len(res.Failed))
	for _, f := range res.Failed {
		failed = append(failed, map[string]any{"path": f.RelPath, "reason": f.Reason})
	}
	// 200 even with failures: some documents were written and the
	// caller has to be told WHICH. A status code cannot say "three of
	// five", and reporting a blanket failure would leave the operator
	// believing memory is untouched when it is not.
	writeJSON(w, http.StatusOK, map[string]any{
		"written":  res.Written,
		"rejected": rejected,
		"failed":   failed,
	})
}

// memoryWriteConfig is the policy an import is written under.
//
// The scrubber runs in BLOCK mode, not redact: a document carrying a
// live credential is refused and named, rather than silently rewritten
// into memory with a hole in it. An operator importing their own notes
// wants to know a token was in there; a redacted copy hides it.
//
// MaxBytes is deliberately left at zero so memport.Apply applies the
// canonical per-file ceiling from memory.CapForPath. One flat cap here
// would either be too tight for daily logs or too loose for AGENT.md.
func (h *MemoryPortabilityHandler) memoryWriteConfig(_ *http.Request) (memory.WriteConfig, error) {
	return memory.WriteConfig{
		Scrubber:     scrubber.New(),
		ScrubberMode: scrubber.ModeBlock,
	}, nil
}
