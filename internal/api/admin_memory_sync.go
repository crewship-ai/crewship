package api

// Manual runs for the two daily memory sweeps (issue #1702).
//
// The operator-model sweep (internal/consolidate/user_model_worker.go) arms
// itself for 05:00 UTC and ticks every 24h; the peer-card sweep does the same
// at 04:00. Nothing exposed FirstRunDelay, no route existed and so no CLI
// command did, which left exactly one way to find out whether the sweep
// works: be awake when it fires and go looking for a log line. That is worse
// here than for most schedulers, because everything about the operator model
// is designed to fail silently — nothing is written when the model proposes
// nothing, when every candidate is refused, when the curator slot cannot be
// built, or when the person is below threshold, and all four look the same
// from outside. On dev1 the curator slot had been failing on an invalid API
// key since #1691 merged, and the only evidence was a Warn at 05:00.
//
// This adds two admin routes:
//
//	POST /api/v1/admin/memory/user-model-sync
//	POST /api/v1/admin/memory/peer-card-sync
//
// Each is a thin call to the sweep the worker runs — RunUserModelSync /
// RunPeerCardSync, unchanged — over the same workspace set the worker walks,
// with the same extractor wiring, and it returns the per-workspace summary
// the worker only ever logged. Two optional inputs in a JSON body:
//
//   - workspace_id (id or slug) narrows the run to one workspace. Without it
//     the run is what the scheduled sweep does: every active workspace on
//     the instance. Body only, deliberately: ?workspace_id= on an admin
//     route is the SESSION workspace (RequireWorkspace reads it, and the CLI
//     client injects it on every request), so reading it here as a filter
//     would make "run it for every workspace" impossible to say from the
//     CLI, and would make a `curl ?workspace_id=other` both switch tenant
//     and narrow the sweep with one parameter.
//   - dry_run runs everything up to the write — including the extractor,
//     which is the expensive, model-backed step an operator wants to observe
//     — and reports what would have happened. Nothing lands on disk, in the
//     index tables or in peer_card_audit. Also accepted as ?dry_run=true,
//     for the runbook's curl.
//
// The sweep runs synchronously in the request, on the request's context: it
// is bounded (one extraction per candidate, each on the curator slot's own
// per-call budget) and re-runnable (the merge is idempotent, the write is
// capped), so a cancelled request leaves nothing that the next run does not
// repair. There is no background goroutine to account for.
//
// Gate: roleManage at registration and canRole("manage") here, the same pair
// every other /admin/* mutation carries.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
)

// AdminMemorySyncHandler serves the two manual-run routes.
type AdminMemorySyncHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// basePath is the host-side storage root the sweeps write under — the
	// same cfg.Storage.BasePath the workers get. Empty means the sweeps
	// cannot run at all (the workers refuse to start), and the routes say
	// so with a 503 rather than a sweep whose every candidate fails.
	basePath string
	// userModelExtractor is the production extractor (internal/usermodel)
	// in the server, a fake in tests. Nil falls back to the sweep's own
	// no-op, which is also what the worker does.
	userModelExtractor consolidate.UserModelExtractor
	// peerExtractor mirrors the above for peer cards. The server wires
	// none today — neither does the worker — so a manual run and the
	// 04:00 sweep produce the same (purge + index) result.
	peerExtractor consolidate.PeerExtractor
}

func NewAdminMemorySyncHandler(db *sql.DB, logger *slog.Logger, basePath string) *AdminMemorySyncHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminMemorySyncHandler{db: db, logger: logger, basePath: basePath}
}

// WithUserModelExtractor sets the extractor a user-model run uses.
func (h *AdminMemorySyncHandler) WithUserModelExtractor(x consolidate.UserModelExtractor) *AdminMemorySyncHandler {
	h.userModelExtractor = x
	return h
}

// WithPeerExtractor sets the extractor a peer-card run uses.
func (h *AdminMemorySyncHandler) WithPeerExtractor(x consolidate.PeerExtractor) *AdminMemorySyncHandler {
	h.peerExtractor = x
	return h
}

// The sweep names on the wire. Spelled the way the worker's log lines and
// the audit metadata spell them ("kind":"user_model"), so an operator
// grepping the server log for what a manual run did finds it.
const (
	memorySweepUserModel = "user_model"
	memorySweepPeerCard  = "peer_card"
)

// memorySyncRequest is the optional body. dry_run may also arrive as a
// query parameter; a body value wins when the body sets it. workspace_id is
// body-only — see the file comment for why the query form is not a filter.
type memorySyncRequest struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	DryRun      *bool  `json:"dry_run,omitempty"`
}

// memorySyncWorkspaceSummary is one workspace's outcome — the consolidate
// summary struct, on the wire in the field names the worker's summary log
// line uses.
type memorySyncWorkspaceSummary struct {
	WorkspaceID      string `json:"workspace_id,omitempty"`
	Candidates       int    `json:"candidates"`
	Writes           int    `json:"writes"`
	SkippedThreshold int    `json:"skipped_threshold"`
	SkippedEmpty     int    `json:"skipped_empty"`
	SkippedOptOut    int    `json:"skipped_opt_out"`
	PurgedOptOut     int    `json:"purged_opt_out"`
	Errors           int    `json:"errors"`
	// Error is set when the sweep for this workspace could not run at all
	// (the candidate query failed, say) — as opposed to Errors, which
	// counts candidates that failed inside a sweep that did run. The
	// worker logs this case and moves on to the next workspace; so does
	// this route, but it tells the caller.
	Error string `json:"error,omitempty"`
}

func (s *memorySyncWorkspaceSummary) add(o memorySyncWorkspaceSummary) {
	s.Candidates += o.Candidates
	s.Writes += o.Writes
	s.SkippedThreshold += o.SkippedThreshold
	s.SkippedEmpty += o.SkippedEmpty
	s.SkippedOptOut += o.SkippedOptOut
	s.PurgedOptOut += o.PurgedOptOut
	s.Errors += o.Errors
}

// memorySyncResponse is the run's report.
type memorySyncResponse struct {
	Sweep      string                       `json:"sweep"`
	DryRun     bool                         `json:"dry_run"`
	Workspaces []memorySyncWorkspaceSummary `json:"workspaces"`
	Totals     memorySyncWorkspaceSummary   `json:"totals"`
	DurationMs int64                        `json:"duration_ms"`
}

// UserModelSync runs the operator-model sweep now.
// POST /api/v1/admin/memory/user-model-sync
func (h *AdminMemorySyncHandler) UserModelSync(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, memorySweepUserModel, func(ctx context.Context, wsID string, dryRun bool) (memorySyncWorkspaceSummary, error) {
		sum, err := consolidate.RunUserModelSync(ctx, h.db, h.logger, wsID, consolidate.UserModelSyncOptions{
			OutputBasePath: h.basePath,
			Extractor:      h.userModelExtractor,
			DryRun:         dryRun,
		})
		return memorySyncWorkspaceSummary{
			WorkspaceID:      sum.WorkspaceID,
			Candidates:       sum.Candidates,
			Writes:           sum.Writes,
			SkippedThreshold: sum.SkippedThresh,
			SkippedEmpty:     sum.SkippedEmpty,
			SkippedOptOut:    sum.SkippedOptOut,
			PurgedOptOut:     sum.PurgedOptOut,
			Errors:           sum.Errors,
		}, err
	})
}

// PeerCardSync runs the peer-card sweep now.
// POST /api/v1/admin/memory/peer-card-sync
func (h *AdminMemorySyncHandler) PeerCardSync(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, memorySweepPeerCard, func(ctx context.Context, wsID string, dryRun bool) (memorySyncWorkspaceSummary, error) {
		sum, err := consolidate.RunPeerCardSync(ctx, h.db, h.logger, wsID, consolidate.PeerCardSyncOptions{
			OutputBasePath: h.basePath,
			Extractor:      h.peerExtractor,
			DryRun:         dryRun,
		})
		return memorySyncWorkspaceSummary{
			WorkspaceID:      sum.WorkspaceID,
			Candidates:       sum.Candidates,
			Writes:           sum.Writes,
			SkippedThreshold: sum.SkippedThresh,
			SkippedEmpty:     sum.SkippedEmpty,
			SkippedOptOut:    sum.SkippedOptOut,
			PurgedOptOut:     sum.PurgedOptOut,
			Errors:           sum.Errors,
		}, err
	})
}

// sweepFunc runs one sweep over one workspace.
type sweepFunc func(ctx context.Context, workspaceID string, dryRun bool) (memorySyncWorkspaceSummary, error)

// run is the shared body of the two routes: gate, inputs, workspace set,
// sweep, report.
func (h *AdminMemorySyncHandler) run(w http.ResponseWriter, r *http.Request, sweep string, fn sweepFunc) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "memory sweeps are not available on this server")
		return
	}
	if h.basePath == "" {
		// The workers log "not started: BasePath required" and stay down
		// in this configuration; a route that ran anyway would report one
		// error per candidate and teach the operator nothing.
		replyError(w, http.StatusServiceUnavailable,
			"memory sweeps are not available: this server has no storage base path, so the sweep has nowhere to write")
		return
	}

	req, ok := h.readRequest(w, r)
	if !ok {
		return
	}
	dryRun := req.DryRun != nil && *req.DryRun
	ctx := r.Context()

	var workspaces []string
	if req.WorkspaceID != "" {
		id, err := activeWorkspaceID(ctx, h.db, req.WorkspaceID)
		if err != nil {
			replyInternalError(w, h.logger, "resolve workspace for memory sweep", err)
			return
		}
		if id == "" {
			replyError(w, http.StatusNotFound, fmt.Sprintf("workspace %q not found", req.WorkspaceID))
			return
		}
		workspaces = []string{id}
	} else {
		var err error
		workspaces, err = consolidate.ActiveWorkspaceIDs(ctx, h.db)
		if err != nil {
			replyInternalError(w, h.logger, "list workspaces for memory sweep", err)
			return
		}
	}

	actor := ""
	if u := UserFromContext(ctx); u != nil {
		actor = u.ID
	}
	h.logger.Info("memory sweep: manual run",
		"sweep", sweep, "dry_run", dryRun, "workspaces", len(workspaces),
		"requested_workspace", req.WorkspaceID, "actor", actor)

	started := time.Now()
	resp := memorySyncResponse{
		Sweep:      sweep,
		DryRun:     dryRun,
		Workspaces: make([]memorySyncWorkspaceSummary, 0, len(workspaces)),
	}
	for _, wsID := range workspaces {
		if err := ctx.Err(); err != nil {
			// The caller has gone (the CLI's own timeout, a closed
			// connection). What ran so far is durable and idempotent; what
			// did not run will run at the next tick. Nothing to report to.
			h.logger.Warn("memory sweep: manual run cancelled",
				"sweep", sweep, "done", len(resp.Workspaces), "of", len(workspaces), "err", err)
			return
		}
		sum, err := fn(ctx, wsID, dryRun)
		sum.WorkspaceID = wsID
		if err != nil {
			// Same continue-on-error as the worker: one broken tenant
			// does not stop the sweep for the others. Reported on the
			// row instead of only logged.
			h.logger.Warn("memory sweep: workspace sweep failed",
				"sweep", sweep, "workspace_id", wsID, "err", err)
			sum.Error = err.Error()
		}
		resp.Totals.add(sum)
		resp.Workspaces = append(resp.Workspaces, sum)
	}
	resp.DurationMs = time.Since(started).Milliseconds()

	h.logger.Info("memory sweep: manual run complete",
		"sweep", sweep, "dry_run", dryRun,
		"workspaces", len(resp.Workspaces),
		"candidates", resp.Totals.Candidates,
		"writes", resp.Totals.Writes,
		"skipped_threshold", resp.Totals.SkippedThreshold,
		"skipped_empty", resp.Totals.SkippedEmpty,
		"skipped_opt_out", resp.Totals.SkippedOptOut,
		"purged_opt_out", resp.Totals.PurgedOptOut,
		"errors", resp.Totals.Errors,
		"duration_ms", resp.DurationMs)
	writeJSON(w, http.StatusOK, resp)
}

// readRequest merges ?dry_run= and the (optional) JSON body. A present body
// field wins; an unparseable body or dry_run value is the caller's problem,
// not something to guess at — guessing wrong on dry_run means writing when
// asked not to.
func (h *AdminMemorySyncHandler) readRequest(w http.ResponseWriter, r *http.Request) (memorySyncRequest, bool) {
	var req memorySyncRequest
	if raw := strings.TrimSpace(r.URL.Query().Get("dry_run")); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			replyError(w, http.StatusBadRequest, "dry_run must be true or false")
			return req, false
		}
		req.DryRun = &v
	}

	if r.Body == nil {
		return req, true
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		replyError(w, http.StatusBadRequest, "could not read request body")
		return req, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return req, true
	}
	var body memorySyncRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return req, false
	}
	if strings.TrimSpace(body.WorkspaceID) != "" {
		req.WorkspaceID = strings.TrimSpace(body.WorkspaceID)
	}
	if body.DryRun != nil {
		req.DryRun = body.DryRun
	}
	return req, true
}

// activeWorkspaceID resolves an id or slug to the workspace's id, or ""
// when no such workspace exists or it is soft-deleted — the same predicate
// consolidate.ActiveWorkspaceIDs applies, so naming a workspace cannot
// reach one the unscoped run would skip.
func activeWorkspaceID(ctx context.Context, db *sql.DB, idOrSlug string) (string, error) {
	var id string
	err := db.QueryRowContext(ctx,
		`SELECT id FROM workspaces WHERE (id = ? OR slug = ?) AND deleted_at IS NULL LIMIT 1`,
		idOrSlug, idOrSlug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}
