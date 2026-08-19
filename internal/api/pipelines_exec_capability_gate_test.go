package api

// The run endpoint's authorization, from both directions.
//
// POST /workspaces/{ws}/pipelines/{slug}/run used to be MANAGER+ and
// nothing else, which made the slash palette's whole premise
// unreachable: an admin could grant a bookkeeper routine.run and the
// middleware would still 403 them before any handler saw the grant.
//
// The gate is now role OR capability. These tests pin both halves, and —
// more importantly — pin that the capability buys nothing except the
// right to ask. Everything the run path refuses an ADMIN it must still
// refuse a member holding routine.run.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runReqAs builds a run request for a caller at a given role. Distinct
// from covPE2Req, which pins every caller at OWNER.
func runReqAs(t *testing.T, userID, wsID, slug, role, body string) *http.Request {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest("POST", "/x", strings.NewReader(body))
		req.ContentLength = int64(len(body))
	} else {
		req = httptest.NewRequest("POST", "/x", nil)
	}
	req.SetPathValue("slug", slug)
	return withWorkspaceUser(req, userID, wsID, role)
}

const gateRunnableDef = `{"dsl_version":"1.0","name":"gate-probe","agentless":true,` +
	`"steps":[{"id":"t","type":"transform","transform":{"input":"true","expression":"."}}]}`

// TestRunEndpoint_CapabilityGate is the table the security decision
// rests on: who may invoke a saved routine.
func TestRunEndpoint_CapabilityGate(t *testing.T) {
	cases := []struct {
		name     string
		role     string
		capsJSON string
		wantRun  bool
	}{
		{
			// The path that existed before routine.run and must be
			// untouched by it.
			name: "admin runs as it always did", role: "ADMIN",
			capsJSON: `["chat","routine.create"]`, wantRun: true,
		},
		{
			name: "owner runs", role: "OWNER",
			capsJSON: `["chat"]`, wantRun: true,
		},
		{
			name: "manager runs", role: "MANAGER",
			capsJSON: `["chat"]`, wantRun: true,
		},
		{
			// The point of the whole change.
			name: "member with routine.run runs", role: "MEMBER",
			capsJSON: `["chat","routine.run"]`, wantRun: true,
		},
		{
			// routine.create authors a routine. It does not invoke one,
			// and a member holding only it must not slip through on the
			// strength of the adjacent name.
			name: "member with routine.create alone is refused", role: "MEMBER",
			capsJSON: `["chat","routine.create"]`, wantRun: false,
		},
		{
			name: "chat-only member is refused", role: "MEMBER",
			capsJSON: `["chat"]`, wantRun: false,
		},
		{
			name: "viewer with routine.run runs", role: "VIEWER",
			capsJSON: `["chat","routine.run"]`, wantRun: true,
		},
		{
			name: "viewer without it is refused", role: "VIEWER",
			capsJSON: `["chat"]`, wantRun: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, wsID := newPipelineHandlerForCRUDTest(t)
			h.SetRunner(&stubRunner{output: "ok"})
			seedPipelineRowDef(t, h.db, wsID, "pipe-gate", "gate-probe", gateRunnableDef)
			userID := seedMemberWithCapabilities(t, h.db, wsID, c.role, c.capsJSON,
				"rungate-"+strings.ReplaceAll(c.name, " ", "-"))
			InvalidateCapabilityCache(wsID, userID)

			rr := httptest.NewRecorder()
			h.Run(rr, runReqAs(t, userID, wsID, "gate-probe", c.role, `{"inputs":{}}`))

			if c.wantRun {
				// 200, not merely "not 403". An admitted caller runs the
				// routine to completion; asserting only the absence of a
				// 403 would keep passing if the gate were replaced by
				// something that failed the run for a different reason.
				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (admitted and ran); body=%s", rr.Code, rr.Body.String())
				}
				if !strings.Contains(rr.Body.String(), `"status":"COMPLETED"`) {
					t.Errorf("run did not complete; body=%s", rr.Body.String())
				}
				return
			}
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

// A capability admits a caller to the gate. It does not admit them past
// governance: a routine awaiting approval, or one an admin has pulled,
// is refused for the member exactly as it is for the ADMIN.
//
// This is the failure mode worth a test of its own — a layered gate is
// easy to add in the wrong place, and "above the status check" and
// "below it" look identical until somebody runs a proposed routine.
func TestRunEndpoint_CapabilityDoesNotBypassGovernance(t *testing.T) {
	for _, status := range []string{"proposed", "disabled"} {
		t.Run(status, func(t *testing.T) {
			h, _, wsID := newPipelineHandlerForCRUDTest(t)
			h.SetRunner(&stubRunner{output: "ok"})
			seedPipelineRowDef(t, h.db, wsID, "pipe-gov", "gov-probe", gateRunnableDef)
			if _, err := h.db.Exec(`UPDATE pipelines SET status = ? WHERE id = 'pipe-gov'`, status); err != nil {
				t.Fatalf("set status: %v", err)
			}

			member := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER",
				`["chat","routine.run"]`, "govgate-member-"+status)
			InvalidateCapabilityCache(wsID, member)
			rr := httptest.NewRecorder()
			h.Run(rr, runReqAs(t, member, wsID, "gov-probe", "MEMBER", `{"inputs":{}}`))
			if rr.Code != http.StatusConflict {
				t.Fatalf("member with routine.run got %d for a %s routine, want 409; body=%s",
					rr.Code, status, rr.Body.String())
			}

			// The same refusal an ADMIN gets — the capability changed who
			// may ask, not what the answer is.
			admin := seedMemberWithCapabilities(t, h.db, wsID, "ADMIN",
				`["chat","routine.create"]`, "govgate-admin-"+status)
			InvalidateCapabilityCache(wsID, admin)
			rrAdmin := httptest.NewRecorder()
			h.Run(rrAdmin, runReqAs(t, admin, wsID, "gov-probe", "ADMIN", `{"inputs":{}}`))
			if rrAdmin.Code != rr.Code {
				t.Errorf("admin got %d and capability-member got %d for the same %s routine — the two must be refused identically",
					rrAdmin.Code, rr.Code, status)
			}
		})
	}
}

// A caller with no membership row in the workspace is not admitted by a
// capability lookup that finds nothing. Belt and braces: RequireWorkspace
// fences this in production, and the handler must not be the reason it
// held.
func TestRunEndpoint_NonMemberRefused(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedPipelineRowDef(t, h.db, wsID, "pipe-nm", "nm-probe", gateRunnableDef)
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('run-outsider','ro@x','Out')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	InvalidateCapabilityCache(wsID, "run-outsider")

	rr := httptest.NewRecorder()
	h.Run(rr, runReqAs(t, "run-outsider", wsID, "nm-probe", "MEMBER", `{"inputs":{}}`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-member; body=%s", rr.Code, rr.Body.String())
	}
}

// run_batch is deliberately NOT reachable on routine.run: the capability
// is scoped to invoking a routine, and a 50-item fan-out is a different
// amount of spend to hand a member. The route keeps roleCreate, so the
// refusal happens in the middleware — which is what this asserts, via
// the recorded route table rather than by driving the handler.
func TestRunBatchRouteStaysRoleGated(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	want := map[string]string{
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run":       roleInline,
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run_batch": roleCreate,
	}
	seen := map[string]string{}
	for _, mr := range r.mutationRoutes {
		key := mr.Method + " " + mr.Pattern
		if _, ok := want[key]; ok {
			seen[key] = mr.Role
		}
	}
	for key, wantRole := range want {
		got, ok := seen[key]
		if !ok {
			t.Errorf("route %q is not registered", key)
			continue
		}
		if got != wantRole {
			t.Errorf("route %q declares role %q, want %q", key, got, wantRole)
		}
	}
}
