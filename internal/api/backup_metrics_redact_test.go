package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestBackupMetrics_HandlerFiltersToOwnWorkspace exercises the actual
// BackupHandler.Metrics handler end-to-end (not a copy of its filtering
// logic). Pre-fix the handler returned `Snapshot.LockHeld` keyed by every
// workspace ID currently mid-backup, so an instance owner of workspace A
// learnt the IDs of B and C just by scraping. CodeRabbit's first review
// flagged that the previous test asserted on a hand-rolled algorithm
// rather than the handler — so a future regression in the handler would
// pass this test untouched.
//
// We seed lockHeldByWs with three workspaces, build a Metrics request
// with the caller in workspace A, and verify the JSON response carries
// only ws_A — never ws_B / ws_C — even though the underlying counter
// state still holds all three.
func TestBackupMetrics_HandlerFiltersToOwnWorkspace(t *testing.T) {
	// Make the seed user the instance owner so the IsInstanceOwner gate
	// inside Metrics() passes.
	t.Setenv(backup.InstanceOwnerEmailEnv, "owner@example.com")

	// Seed three workspaces holding backup locks. These survive in
	// process state until t.Cleanup releases them.
	backup.ResetMetrics()
	backup.ObserveLockAcquired("ws_A")
	backup.ObserveLockAcquired("ws_B")
	backup.ObserveLockAcquired("ws_C")
	t.Cleanup(func() {
		backup.ObserveLockReleased("ws_A")
		backup.ObserveLockReleased("ws_B")
		backup.ObserveLockReleased("ws_C")
		backup.ResetMetrics()
	})

	h := &BackupHandler{
		logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/backups/metrics", nil)
	ctx := context.WithValue(req.Context(), ctxUser, &AuthUser{Email: "owner@example.com"})
	ctx = context.WithValue(ctx, ctxWorkspaceID, "ws_A")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	h.Metrics(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "owner request must succeed; body=%s", rec.Body.String())

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	heldRaw, ok := got["lock_held_seconds_by_workspace"].(map[string]any)
	require.True(t, ok, "response must carry lock_held_seconds_by_workspace map; got: %v", got)

	// Caller's own workspace is present.
	_, hasA := heldRaw["ws_A"]
	assert.True(t, hasA, "caller's own workspace ID must remain in the map")

	// Foreign workspaces redacted.
	_, hasB := heldRaw["ws_B"]
	_, hasC := heldRaw["ws_C"]
	assert.False(t, hasB, "ws_B must NOT leak across workspace boundary; got: %v", heldRaw)
	assert.False(t, hasC, "ws_C must NOT leak across workspace boundary; got: %v", heldRaw)
}

// TestBackupMetrics_HandlerEmptiesWhenCallerHasNoLock — the caller's
// workspace isn't currently holding a lock; the map should be empty,
// not full of other workspaces' state.
func TestBackupMetrics_HandlerEmptiesWhenCallerHasNoLock(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "owner@example.com")

	backup.ResetMetrics()
	backup.ObserveLockAcquired("ws_B")
	backup.ObserveLockAcquired("ws_C")
	t.Cleanup(func() {
		backup.ObserveLockReleased("ws_B")
		backup.ObserveLockReleased("ws_C")
		backup.ResetMetrics()
	})

	h := &BackupHandler{
		logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/backups/metrics", nil)
	ctx := context.WithValue(req.Context(), ctxUser, &AuthUser{Email: "owner@example.com"})
	ctx = context.WithValue(ctx, ctxWorkspaceID, "ws_A")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	h.Metrics(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	heldRaw, _ := got["lock_held_seconds_by_workspace"].(map[string]any)
	assert.Empty(t, heldRaw, "caller with no active lock must see an empty map, not other workspaces' state; got: %v", heldRaw)
}
