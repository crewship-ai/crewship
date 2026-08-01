package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/health"
)

// The metric existed and could not be read.
//
// health.Record has been on the credential path since the decision monitor
// landed: every verdict updates a rolling window and an alarm fires into the
// inbox when the picture collapses. What was missing is the other half — an
// operator could be PAGED and could not LOOK. "Is my judge behaving?" had no
// answer short of waiting for it to break, which is a strange shape for a
// feature whose entire purpose is catching silent degradation.
//
// This is that read surface: the same numbers the alarm decides on, on demand.

func healthReq(t *testing.T, ws string) *http.Request {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/admin/keeper/health", nil)
	ctx := context.WithValue(r.Context(), ctxRole, "OWNER")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "u-admin"})
	if ws != "" {
		ctx = context.WithValue(ctx, ctxWorkspaceID, ws)
	}
	return r.WithContext(ctx)
}

func TestAdminKeeperHealth_ReportsTheWindowTheAlarmReads(t *testing.T) {
	health.Default.Reset()
	for i := 0; i < 12; i++ {
		health.Default.Record(health.Verdict{WorkspaceID: "ws1", Decision: "ALLOW"})
	}
	for i := 0; i < 4; i++ {
		health.Default.Record(health.Verdict{WorkspaceID: "ws1", Decision: "DENY"})
	}
	health.Default.Record(health.Verdict{WorkspaceID: "ws1", Decision: "ESCALATE"})

	h := NewAdminKeeperHealthHandler(newTestLogger())
	rr := httptest.NewRecorder()
	h.Get(rr, healthReq(t, "ws1"))

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var out keeperHealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, rr.Body.String())
	}
	if out.Samples != 17 {
		t.Errorf("samples = %d, want 17", out.Samples)
	}
	if out.Allow != 12 || out.Deny != 4 || out.Escalate != 1 {
		t.Errorf("counts = %d/%d/%d, want 12/4/1", out.Allow, out.Deny, out.Escalate)
	}
	// The share the alarm actually reads — granted OR escalated — not the ALLOW
	// share, because an all-L4 workspace escalates by design and runs at an
	// ALLOW rate of zero while perfectly healthy.
	if out.ProgressedRate < 0.7 {
		t.Errorf("progressed_rate = %v, want the allow+escalate share", out.ProgressedRate)
	}
	if out.Alarm != nil {
		t.Errorf("healthy window raised %q", out.Alarm.Kind)
	}
}

// A workspace nobody has asked about yet is not an error, and it must not read
// as a healthy one either — zero samples is its own answer.
func TestAdminKeeperHealth_UntrackedWorkspaceIsEmptyNotHealthy(t *testing.T) {
	health.Default.Reset()

	h := NewAdminKeeperHealthHandler(newTestLogger())
	rr := httptest.NewRecorder()
	h.Get(rr, healthReq(t, "never-seen"))

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rr.Code)
	}
	var out keeperHealthResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out.Samples != 0 {
		t.Errorf("samples = %d, want 0", out.Samples)
	}
	if out.Alarm != nil {
		t.Error("an untracked workspace raised an alarm")
	}
}

// The point of the read surface: when the window IS collapsed, asking shows the
// same thing the page would have said. Twenty consecutive denials is the #1624
// shape — a judge answering unusably while every response looks well-formed.
func TestAdminKeeperHealth_SurfacesTheAlarmWithoutWaitingForIt(t *testing.T) {
	health.Default.Reset()
	for i := 0; i < 20; i++ {
		health.Default.Record(health.Verdict{WorkspaceID: "ws1", Decision: "DENY"})
	}

	h := NewAdminKeeperHealthHandler(newTestLogger())
	rr := httptest.NewRecorder()
	h.Get(rr, healthReq(t, "ws1"))

	var out keeperHealthResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out.Alarm == nil {
		t.Fatal("a workspace denying 20 of 20 reported no alarm — this is the outage the metric exists for")
	}
	if out.Alarm.Summary == "" {
		t.Error("the alarm carries no summary, so the reader learns nothing an exit code would not tell them")
	}
}

func TestAdminKeeperHealth_RequiresAWorkspace(t *testing.T) {
	h := NewAdminKeeperHealthHandler(newTestLogger())
	rr := httptest.NewRecorder()
	h.Get(rr, healthReq(t, ""))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 without a workspace", rr.Code)
	}
}
