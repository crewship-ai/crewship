package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestDeferredReceiptInspectionIncludesTerminalAndInFlight(t *testing.T) {
	h, db, user, ws := scheduleHandlerRig(t)
	seedPipelineRow(t, db, ws, "receipt-recipe", "receipt-recipe")
	store := pipeline.NewPendingRunStore(db)
	for _, status := range []string{"pending", "fired", "failed", "expired", "cancelled"} {
		_, _, err := store.Enqueue(t.Context(), pipeline.PendingRun{ID: status, WorkspaceID: ws, PipelineID: "receipt-recipe", PipelineSlug: "receipt-recipe", FireAt: time.Now().Add(-time.Minute), InputsJSON: `{"message":"safe","api_key":"private"}`})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `UPDATE pending_runs SET status=?,last_error=?,dispatch_attempts=1 WHERE id=?`, status, map[string]string{"failed": "Dispatch unavailable.", "expired": "TTL expired."}[status], status); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{"pending", "fired", "failed", "expired", "cancelled"} {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/pending/"+status, nil), user, ws, "VIEWER")
		req.SetPathValue("pendingId", status)
		rr := httptest.NewRecorder()
		h.GetPendingRun(rr, req)
		if rr.Code != 200 || strings.Contains(rr.Body.String(), "private") {
			t.Fatalf("receipt %s: %d %s", status, rr.Code, rr.Body)
		}
		var dto pendingRunReceipt
		if err := json.Unmarshal(rr.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		if dto.Status != status || dto.CanCancel || dto.RunID != "" {
			t.Fatalf("misclassified empty run ID: %+v", dto)
		}
	}
	managerReq := withWorkspaceUser(httptest.NewRequest("GET", "/pending/pending", nil), user, ws, "MANAGER")
	managerReq.SetPathValue("pendingId", "pending")
	managerReply := httptest.NewRecorder()
	h.GetPendingRun(managerReply, managerReq)
	var managerDTO pendingRunReceipt
	if err := json.Unmarshal(managerReply.Body.Bytes(), &managerDTO); err != nil || !managerDTO.CanCancel {
		t.Fatalf("manager cannot cancel pending: %v %s", err, managerReply.Body)
	}
	for _, tc := range []struct {
		query       string
		count, code int
	}{{"", 1, 200}, {"?status=all", 5, 200}, {"?status=failed", 1, 200}, {"?status=bogus", 0, 400}} {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/pending"+tc.query, nil), user, ws, "VIEWER")
		rr := httptest.NewRecorder()
		h.ListPendingRuns(rr, req)
		if rr.Code != tc.code {
			t.Fatalf("list %s: %d %s", tc.query, rr.Code, rr.Body)
		}
		if rr.Code == 200 {
			var rows []pendingRunReceipt
			if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil || len(rows) != tc.count {
				t.Fatalf("list %s: %d %v", tc.query, len(rows), err)
			}
		}
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/pending/failed", nil), user, "different-workspace", "VIEWER")
	req.SetPathValue("pendingId", "failed")
	rr := httptest.NewRecorder()
	h.GetPendingRun(rr, req)
	if rr.Code != 404 {
		t.Fatalf("cross-workspace receipt visible: %d %s", rr.Code, rr.Body)
	}
}
