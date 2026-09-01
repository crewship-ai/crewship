package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/harbormaster"
)

// approvals_retention_days shares its PATCH surface with the audit pair
// (workspaces_audit_retention_test.go) and, after #2233's second round,
// shares their "0 means keep forever" semantics too — a reversal from this
// column's first version, which treated 0 the same as unset. That reversal
// exists because this flag sits on the SAME `workspace update` command as
// credential_audit_retention_days / audit_log_retention_days: an operator
// who types 0 out of habit with its neighbours must get "never delete",
// not a silent 90-day window they never asked for.

func TestWorkspaceUpdate_ApprovalsRetentionWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		want       *int // nil means "column left untouched"
	}{
		{
			name:       "a finite window",
			body:       `{"approvals_retention_days":45}`,
			wantStatus: http.StatusOK,
			want:       intPtr(45),
		},
		{
			name:       "zero is accepted and means keep forever, matching its sibling flags",
			body:       `{"approvals_retention_days":0}`,
			wantStatus: http.StatusOK,
			want:       intPtr(0),
		},
		{
			name:       "omitted leaves the column untouched",
			body:       `{"name":"Renamed Workspace"}`,
			wantStatus: http.StatusOK,
			want:       nil,
		},
		{
			name:       "a negative window is refused",
			body:       `{"approvals_retention_days":-1}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			// harbormaster.MaxApprovalsRetentionDays itself must still be
			// accepted — only values PAST the int64-nanosecond overflow
			// boundary are refused.
			name:       "the maximum window is accepted",
			body:       `{"approvals_retention_days":106751}`,
			wantStatus: http.StatusOK,
			want:       intPtr(106751),
		},
		{
			// time.Duration is int64 nanoseconds: retentionDays*24h
			// overflows past 106751 days and wraps NEGATIVE, which would
			// move the sweep's cutoff into the future and delete every
			// terminal row regardless of age. Refuse it here rather than
			// let it reach the sweeper.
			name:       "a window past the int64-nanosecond overflow boundary is refused",
			body:       `{"approvals_retention_days":106752}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			h := &WorkspaceHandler{db: db, logger: slog.Default()}

			rr := patchWorkspaceRetention(t, h, wsID, tc.body)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}

			var resp workspaceResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v (body=%s)", err, rr.Body.String())
			}
			assertIntPtr(t, "approvals_retention_days (response)", resp.ApprovalsRetentionDays, tc.want)

			var col *int
			if err := db.QueryRow(
				`SELECT approvals_retention_days FROM workspaces WHERE id = ?`, wsID).Scan(&col); err != nil {
				t.Fatalf("read back: %v", err)
			}
			assertIntPtr(t, "approvals_retention_days (column)", col, tc.want)
		})
	}
}

// TestWorkspaceUpdate_ApprovalsZeroWindowActuallyStopsTheSweep closes the
// loop the same way TestWorkspaceUpdate_ZeroWindowActuallyStopsTheSweep does
// for the audit pair: the API accepting 0 is only meaningful if the sweeper
// then honours it.
func TestWorkspaceUpdate_ApprovalsZeroWindowActuallyStopsTheSweep(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := &WorkspaceHandler{db: db, logger: slog.Default()}

	if _, err := db.Exec(
		`INSERT INTO approvals_queue (id, workspace_id, requested_by, kind, reason, status, decided_at)
		 VALUES ('ap-zerowin', ?, 'u1', 'tool_call', 'because', 'approved', datetime('now', '-400 days'))`,
		wsID); err != nil {
		t.Fatalf("seed approval row: %v", err)
	}

	rr := patchWorkspaceRetention(t, h, wsID, `{"approvals_retention_days":0}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", rr.Code, rr.Body.String())
	}

	if err := harbormaster.SweepAllWorkspacesApprovalsRetention(context.Background(), db, slog.Default()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM approvals_queue WHERE id = 'ap-zerowin'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("ap-zerowin rows = %d, want 1 — the operator set 0, which means keep forever, and a 400-day-old row was deleted anyway", n)
	}
}
