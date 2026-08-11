package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The audit retention windows are only useful if an operator can set them
// through a supported surface. CLAUDE.md is explicit that everything goes
// through the CLI and never a DB shell, so a column nobody can write is a
// feature that does not exist.
//
// These tests cover the PATCH surface, and specifically the thing that makes
// these two columns different from run_retention_days: 0 is a legal, meaningful
// value here, and it must survive the round trip rather than being rejected or
// coerced into "use the default".

func patchWorkspaceRetention(t *testing.T, h *WorkspaceHandler, wsID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+wsID, strings.NewReader(body))
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req.WithContext(ctx))
	return rr
}

func TestWorkspaceUpdate_AuditRetentionWindows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		// wantCred / wantLog are pointers so "absent" is distinguishable from
		// zero — which is the entire point of these columns.
		wantCred *int
		wantLog  *int
	}{
		{
			name:       "a finite window on each",
			body:       `{"credential_audit_retention_days":30,"audit_log_retention_days":365}`,
			wantStatus: http.StatusOK,
			wantCred:   intPtr(30), wantLog: intPtr(365),
		},
		{
			name:       "zero is accepted and means keep forever",
			body:       `{"credential_audit_retention_days":0,"audit_log_retention_days":0}`,
			wantStatus: http.StatusOK,
			wantCred:   intPtr(0), wantLog: intPtr(0),
		},
		{
			name:       "omitted leaves both untouched",
			body:       `{"name":"Renamed Workspace"}`,
			wantStatus: http.StatusOK,
			wantCred:   nil, wantLog: nil,
		},
		{
			name:       "a negative credential window is refused",
			body:       `{"credential_audit_retention_days":-1}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "a negative audit_logs window is refused",
			body:       `{"audit_log_retention_days":-7}`,
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

			// The response must report what was stored — a client that has to
			// re-read to discover its own change cannot tell a rejected value
			// from an applied one.
			var resp workspaceResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v (body=%s)", err, rr.Body.String())
			}
			assertIntPtr(t, "credential_audit_retention_days (response)", resp.CredentialAuditRetentionDays, tc.wantCred)
			assertIntPtr(t, "audit_log_retention_days (response)", resp.AuditLogRetentionDays, tc.wantLog)

			// And it must actually be in the column the sweeper reads.
			var cred, logDays *int
			if err := db.QueryRow(
				`SELECT credential_audit_retention_days, audit_log_retention_days FROM workspaces WHERE id = ?`,
				wsID).Scan(&cred, &logDays); err != nil {
				t.Fatalf("read back: %v", err)
			}
			assertIntPtr(t, "credential_audit_retention_days (column)", cred, tc.wantCred)
			assertIntPtr(t, "audit_log_retention_days (column)", logDays, tc.wantLog)
		})
	}
}

// TestWorkspaceUpdate_ZeroWindowActuallyStopsTheSweep closes the loop: the
// API accepting 0 is only meaningful if the sweeper then honours it. Without
// this, "0 means keep forever" could be true at the edge and false in the
// engine, and no other test would notice.
func TestWorkspaceUpdate_ZeroWindowActuallyStopsTheSweep(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := &WorkspaceHandler{db: db, logger: slog.Default()}

	seedAuditRows(t, db, wsID, 400, 3, "zerowin")

	rr := patchWorkspaceRetention(t, h, wsID, `{"credential_audit_retention_days":0}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", rr.Code, rr.Body.String())
	}

	if err := SweepAllWorkspacesAuditRetention(context.Background(), db, auditLogger()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := auditRetentionCount(t, db, "credential_audit"); got != 3 {
		t.Errorf("credential_audit = %d rows, want 3 — the operator set 0, which means keep forever, and 400-day-old rows were deleted anyway", got)
	}
}

func intPtr(v int) *int { return &v }

func assertIntPtr(t *testing.T, label string, got, want *int) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %s, want null (untouched)", label, fmtIntPtr(got))
	case want != nil && got == nil:
		t.Errorf("%s = null, want %d", label, *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("%s = %d, want %d", label, *got, *want)
	}
}

func fmtIntPtr(p *int) string {
	if p == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *p)
}
