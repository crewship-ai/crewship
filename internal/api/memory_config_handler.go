package api

// Memory config admin endpoint — Iter 6 of the memory-hardening
// series. Iter 4 wired the per-workspace retention sweep that
// reads workspaces.memory_config.versions_retention_days, but
// nothing wrote that column via API — operators had to edit
// SQLite by hand, which is both error-prone and unauditable.
// This handler closes the gap with GET + PATCH endpoints, with
// per-field validation and an audit-trail journal emit on every
// change.
//
// Endpoints:
//   GET   /api/v1/admin/memory/config   — read current config
//   PATCH /api/v1/admin/memory/config   — partial update
//
// Auth: authed + wsCtx + manage role (mirrors memory_stats).
//
// Response shape (stable; pinned by the contract test):
//
//   {
//     "workspace_id": "...",
//     "versions_retention_days": 30,           // resolved value
//                                              // (falls back to
//                                              // DefaultRetentionDays
//                                              // when unset)
//     "is_default": true,                      // true = no row /
//                                              // no key / fell
//                                              // back to default
//     "raw_config": null                       // the literal JSON
//                                              // stored on the row
//                                              // ("" if NULL), so
//                                              // operators can see
//                                              // what's actually
//                                              // in the column
//   }
//
// PATCH body shape (partial; only the keys you want to change):
//
//   { "versions_retention_days": 7 }
//
// Validation:
//   - versions_retention_days must be a positive integer in
//     [1, MaxRetentionDays]. Zero / negative / non-integer →
//     400 Bad Request. The upper bound (3650 days = 10 years)
//     is a soft sanity cap; operators wanting longer retention
//     should edit the column directly so the decision is
//     deliberate.
//   - Unknown top-level keys in the PATCH body are passed
//     through to the stored JSON document (forward compat for
//     future fields like compaction_hour_override or
//     consolidate_min_entries_override). The audit emit logs
//     the unknown keys at notice level so operators can spot
//     a typo.
//
// Audit:
//   - Every successful PATCH emits memory.config_updated with
//     payload {workspace_id, changed: {key: {from, to}}, actor}.
//     A PATCH that produces no diff (e.g. resetting to the same
//     value) is a no-op: 200 OK with the current shape, NO
//     journal entry. Idempotency over volume — the audit trail
//     should reflect real change, not request count.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// MaxRetentionDays caps the upper bound for versions_retention_days
// on the PATCH path. 3650 days = 10 years; longer windows risk
// the audit trail outliving the workspace's compliance horizon
// and require a deliberate SQL edit. Documented in the response
// 400 error message so operators see the cap before retrying.
const MaxRetentionDays = 3650

type MemoryConfigHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

func NewMemoryConfigHandler(db *sql.DB, logger *slog.Logger) *MemoryConfigHandler {
	return &MemoryConfigHandler{db: db, logger: logger, journal: noopEmitter{}}
}

// SetJournal wires the production emitter. Mirrors ProposedHandler.SetJournal.
func (h *MemoryConfigHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

type memoryConfigResponse struct {
	WorkspaceID           string  `json:"workspace_id"`
	VersionsRetentionDays int     `json:"versions_retention_days"`
	IsDefault             bool    `json:"is_default"`
	RawConfig             *string `json:"raw_config"`
}

// Get serves GET /api/v1/admin/memory/config.
func (h *MemoryConfigHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return
	}

	resp, err := h.loadConfig(ctx, workspaceID)
	if err != nil {
		h.logger.Error("memory config: load", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Patch serves PATCH /api/v1/admin/memory/config.
//
// The handler reads the existing JSON, merges the patch body's
// keys, validates, writes the result back, and emits an audit
// event when at least one key actually changed value.
func (h *MemoryConfigHandler) Patch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	user := UserFromContext(ctx)
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return
	}

	// Decode the patch body into an opaque map so unknown keys
	// pass through to the merged document. A `json.Number` decoder
	// would preserve integer-vs-float ambiguity, but the validator
	// below tolerates float64 (which is what map[string]any
	// produces by default) and reports cleaner type errors that
	// way.
	var patch map[string]any
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // doesn't fire on map[string]any but keeps us honest if we tighten later
	if err := dec.Decode(&patch); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if patch == nil {
		replyError(w, http.StatusBadRequest, "request body required")
		return
	}

	// Read the current document so we know which keys actually
	// changed (audit-trail accuracy) and so unspecified keys are
	// preserved (PATCH semantics, not PUT).
	current, currentRaw, err := h.loadConfigDoc(ctx, workspaceID)
	if err != nil {
		h.logger.Error("memory config: load before patch", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "load failed")
		return
	}

	merged := make(map[string]any, len(current)+len(patch))
	for k, v := range current {
		merged[k] = v
	}
	changes := map[string]map[string]any{}
	for k, v := range patch {
		if existing, ok := current[k]; ok && jsonEqual(existing, v) {
			continue
		}
		merged[k] = v
		var from any
		if existing, ok := current[k]; ok {
			from = existing
		}
		changes[k] = map[string]any{"from": from, "to": v}
	}

	if msg, ok := validateMemoryConfig(merged); !ok {
		replyError(w, http.StatusBadRequest, msg)
		return
	}

	if len(changes) == 0 {
		// No-op PATCH: don't write, don't audit. 200 with the
		// current shape so the client can refresh its view.
		resp, err := h.buildResponse(workspaceID, merged, currentRaw)
		if err != nil {
			h.logger.Error("memory config: serialise current", "workspace_id", workspaceID, "error", err)
			replyError(w, http.StatusInternalServerError, "serialise failed")
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		// json.Marshal of a map[string]any with JSON-roundtripped
		// values cannot fail in practice — but propagate just in
		// case a future change adds a non-marshallable type.
		h.logger.Error("memory config: marshal merged", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "marshal failed")
		return
	}
	if _, err := h.db.ExecContext(ctx,
		`UPDATE workspaces SET memory_config = ? WHERE id = ?`,
		string(mergedBytes), workspaceID); err != nil {
		h.logger.Error("memory config: update", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "update failed")
		return
	}

	// Emit the audit event AFTER the UPDATE commits so a crash
	// mid-emit doesn't leave a journal entry claiming a change
	// that didn't happen.
	var actorID string
	if user != nil {
		actorID = user.ID
	}
	if _, emitErr := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryMemoryConfigUpdated,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary:     fmt.Sprintf("memory config updated (%d field(s) changed)", len(changes)),
		Payload: map[string]any{
			"workspace_id": workspaceID,
			"changes":      changes,
		},
	}); emitErr != nil {
		// Best-effort — the column is already updated. Log so
		// operators can spot if audit emission is broken.
		h.logger.Warn("memory config: audit emit failed",
			"workspace_id", workspaceID, "error", emitErr)
	}

	mergedStr := string(mergedBytes)
	resp, err := h.buildResponse(workspaceID, merged, &mergedStr)
	if err != nil {
		h.logger.Error("memory config: serialise merged", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "serialise failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// loadConfig is the read path — returns the response shape
// directly. Separated from loadConfigDoc so the GET handler
// stays a one-liner.
func (h *MemoryConfigHandler) loadConfig(ctx context.Context, workspaceID string) (memoryConfigResponse, error) {
	doc, raw, err := h.loadConfigDoc(ctx, workspaceID)
	if err != nil {
		return memoryConfigResponse{}, err
	}
	return h.buildResponse(workspaceID, doc, raw)
}

// loadConfigDoc returns the parsed config map AND the raw JSON
// string (or nil when the column is NULL / empty). The raw
// string is needed by the PATCH path to preserve byte-level
// fidelity in the response when the operator can compare the
// stored doc against what they sent.
func (h *MemoryConfigHandler) loadConfigDoc(ctx context.Context, workspaceID string) (map[string]any, *string, error) {
	var raw sql.NullString
	err := h.db.QueryRowContext(ctx,
		`SELECT memory_config FROM workspaces WHERE id = ?`, workspaceID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		// Workspace doesn't exist — caller already authed via
		// wsCtx so this should be unreachable in practice, but
		// return a clean error so the caller can 404 if it cares.
		return nil, nil, fmt.Errorf("workspace %s: not found", workspaceID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("memory config: select: %w", err)
	}
	if !raw.Valid || raw.String == "" {
		return map[string]any{}, nil, nil
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw.String), &doc); err != nil {
		// Malformed JSON in the column is a data-integrity bug,
		// not a request-path failure — surface to the operator
		// rather than silently returning defaults (which would
		// hide the corruption).
		return nil, nil, fmt.Errorf("memory config: stored JSON malformed: %w", err)
	}
	rawStr := raw.String
	return doc, &rawStr, nil
}

// buildResponse fills the response shape from the parsed doc.
// `raw` is the literal JSON stored on the row (or nil when the
// column is empty).
func (h *MemoryConfigHandler) buildResponse(workspaceID string, doc map[string]any, raw *string) (memoryConfigResponse, error) {
	resp := memoryConfigResponse{
		WorkspaceID: workspaceID,
		RawConfig:   raw,
	}
	if v, ok := doc["versions_retention_days"]; ok {
		if n, ok := jsonIntValue(v); ok && n > 0 {
			resp.VersionsRetentionDays = n
			resp.IsDefault = false
			return resp, nil
		}
		// Present-but-invalid value: fall back to default and flag
		// the row as is_default=true so the operator UI knows the
		// stored value isn't being honoured.
	}
	resp.VersionsRetentionDays = memory.DefaultRetentionDays
	resp.IsDefault = true
	return resp, nil
}

// validateMemoryConfig enforces the per-field constraints on the
// merged document. Returns (errorMessage, false) on failure,
// ("", true) on success. Kept as a pure function so a future
// CLI surface can call it without HTTP plumbing.
func validateMemoryConfig(doc map[string]any) (string, bool) {
	if v, ok := doc["versions_retention_days"]; ok {
		n, intOK := jsonIntValue(v)
		if !intOK {
			return "versions_retention_days must be an integer", false
		}
		if n < 1 {
			return fmt.Sprintf("versions_retention_days must be >= 1, got %d", n), false
		}
		if n > MaxRetentionDays {
			return fmt.Sprintf("versions_retention_days must be <= %d (10 years); longer windows require a deliberate SQL edit", MaxRetentionDays), false
		}
	}
	return "", true
}

// jsonIntValue converts a JSON-decoded value (which arrives as
// float64 for numbers via map[string]any) into an int when the
// value is non-fractional and in range. Returns (0, false) for
// anything else (string, bool, fractional float, etc.).
func jsonIntValue(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		// Reject fractional values — versions_retention_days is
		// whole days, no half-day semantics.
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}

// jsonEqual reports whether two JSON-decoded values are equal
// after coercion through the standard Marshal round trip. Used
// by the PATCH handler to suppress no-op writes / audit emits.
//
// This is intentionally NOT reflect.DeepEqual: a float64(7) and
// a json.Number("7") are equal-on-the-wire but DeepEqual sees
// them as different. Marshal-compare normalises both.
func jsonEqual(a, b any) bool {
	aj, aerr := json.Marshal(a)
	bj, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return false
	}
	return string(aj) == string(bj)
}
