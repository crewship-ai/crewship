package api

// Invoking identity of a routine run comes from a verified identity, never
// from a request header or an unchecked body field (AWX/Omarchy iteration 1,
// opponent finding K1).
//
// The run's invoking_crew_id / invoking_agent_id are persisted provenance:
// the "From" line on the approval card a wait(approval) step raises, the
// cross-crew signal in the journal, and — through routineTrust — the crew
// whose autonomy dial decides whether a standing trust grant may fire in
// place of a human. Two surfaces write them:
//
//   - the public /run route (JWT / CLI token). It used to copy
//     X-Crewship-Invoking-Crew / -Agent straight from the request, so any
//     workspace member could attribute their run to any crew and agent id
//     they typed. It now records every run on this route as user-driven.
//   - the internal /pipelines/run route (sidecar's X-Internal-Token). The
//     crew was already pinned to a crew-bound token's binding (#1186); the
//     agent was never checked, and a master-token caller could name a crew
//     from another workspace. Both are now verified against the crews and
//     agents tables.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// runProvenance reads what the executor persisted for the single run of a
// pipeline. Empty strings for NULL columns.
func runProvenance(t *testing.T, h *PipelineHandler, pipelineID string) (runID, crew, agent, user, hash string) {
	t.Helper()
	if err := h.db.QueryRowContext(t.Context(), `
		SELECT id, COALESCE(invoking_crew_id,''), COALESCE(invoking_agent_id,''), COALESCE(invoking_user_id,''), COALESCE(definition_hash,'')
		  FROM pipeline_runs WHERE pipeline_id = ?`, pipelineID).Scan(&runID, &crew, &agent, &user, &hash); err != nil {
		t.Fatalf("read run row for %s: %v", pipelineID, err)
	}
	return
}

// TestRun_InvokingHeadersFromJWTCallerAreIgnored covers the public route:
// a JWT caller with forged headers gets a legitimate run whose provenance
// is the caller, not the headers — and, walked all the way to the trust
// gate, the forged crew's posture cannot fire a grant on a strict author's
// gate.
func TestRun_InvokingHeadersFromJWTCallerAreIgnored(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.SetRunStore(pipeline.NewRunStore(h.db))

	// The routine's author crew is strict; the crew a forger would name
	// is `full` — the posture that lets a standing grant fire.
	strictCrew := seedCrewRow(t, h.db, "crew-strict-author", wsID, "Strict", "strict-author")
	fullCrew := seedCrewRow(t, h.db, "crew-full-sibling", wsID, "Full", "full-sibling")
	for id, dial := range map[string]string{strictCrew: "strict", fullCrew: "full"} {
		if _, err := h.db.Exec(`UPDATE crews SET autonomy_level = ? WHERE id = ?`, dial, id); err != nil {
			t.Fatalf("set autonomy: %v", err)
		}
	}
	seedPipelineRowDef(t, h.db, wsID, "pipe-forge", "forge-probe", gateRunnableDef)
	if _, err := h.db.Exec(`UPDATE pipelines SET author_crew_id = ? WHERE id = 'pipe-forge'`, strictCrew); err != nil {
		t.Fatalf("set author crew: %v", err)
	}
	opID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER", `["chat","routine.run"]`, "forge-op")
	InvalidateCapabilityCache(wsID, opID)

	req := runReqAs(t, opID, wsID, "forge-probe", "MEMBER", `{"inputs":{}}`)
	req.Header.Set("X-Crewship-Invoking-Crew", fullCrew)
	req.Header.Set("X-Crewship-Invoking-Agent", "agent-i-made-up")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("run = %d, want 200 — the run itself is legitimate, only its claimed provenance is not; body=%s", rr.Code, rr.Body.String())
	}

	runID, crew, agent, user, hash := runProvenance(t, h, "pipe-forge")
	if crew != "" || agent != "" {
		t.Errorf("persisted provenance crew=%q agent=%q, want both empty — a JWT caller's headers are not an identity", crew, agent)
	}
	if user != opID {
		t.Errorf("invoking_user_id = %q, want the authenticated caller %q", user, opID)
	}

	// HTTP → persisted row → trust gate. With a standing grant for this
	// gate, a wait(approval) on this run must still park for a human,
	// because the only crew whose posture applies is the strict author.
	grants := pipeline.NewTrustGrantStore(h.db)
	if _, err := grants.Grant(context.Background(), pipeline.GrantInput{
		WorkspaceID: wsID, PipelineID: "pipe-forge", StepID: "publish", DefinitionHash: hash,
		GrantedByUserID: opID, Reason: "test", PriorApprovals: 10,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	wps := pipeline.NewSQLWaitpointStore(h.db)
	defer wps.Close()
	token, err := wps.CreateApproval(context.Background(), pipeline.WaitpointApprovalRequest{
		WorkspaceID: wsID, PipelineRunID: runID, StepID: "publish", Prompt: "Publish?",
	})
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&status); err != nil {
		t.Fatalf("read waitpoint: %v", err)
	}
	if status != "pending" {
		t.Errorf("waitpoint status = %q, want pending — a forged invoking crew must not route a strict author's gate through a trust grant", status)
	}
}

// TestInternalRun_InvokingIdentityIsVerified covers the sidecar route: the
// crew and agent in the body must be real rows in the run's workspace, the
// agent a member of the named crew, whatever token presented them.
func TestInternalRun_InvokingIdentityIsVerified(t *testing.T) {
	h, db, _, wsID, ownCrew := cov2PCRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineRowDef(t, db, wsID, "pipe-internal", "internal-probe", gateRunnableDef)
	if _, err := db.Exec(`UPDATE pipelines SET author_crew_id = ? WHERE id = 'pipe-internal'`, ownCrew); err != nil {
		t.Fatalf("set author crew: %v", err)
	}
	ownAgent := "cov2pc_agent" // seeded by cov2PCRig in ownCrew
	siblingCrew := seedCrewRow(t, db, "crew-sibling", wsID, "Sibling", "sibling")
	siblingAgent := seedAgentRow(t, db, "agent-sibling", wsID, siblingCrew, "Sib", "sib", "WORKER")
	orphanNull := seedAgentRow(t, db, "agent-orphan-null", wsID, ownCrew, "Null", "orphan-null", "WORKER")
	if _, err := db.Exec(`UPDATE agents SET crew_id = NULL WHERE id = ?`, orphanNull); err != nil {
		t.Fatal(err)
	}
	otherWS := "ws-other-tenant"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other-tenant')`, otherWS); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}
	foreignCrew := seedCrewRow(t, db, "crew-foreign", otherWS, "Foreign", "foreign")
	foreignAgent := seedAgentRow(t, db, "agent-foreign", otherWS, foreignCrew, "For", "for", "WORKER")

	boundCtx := func(crew string) context.Context {
		ctx := context.WithValue(context.Background(), ctxInternalTokenWS, wsID)
		if crew != "" {
			ctx = context.WithValue(ctx, ctxInternalTokenCrew, crew)
		}
		return ctx
	}
	masterCtx := context.Background() // no binding at all: host-side / master token

	cases := []struct {
		name       string
		ctx        context.Context
		crew       string
		agent      string
		wantStatus int
	}{
		{"master token, orphan NULL crew", masterCtx, "", orphanNull, http.StatusForbidden},
		{"workspace token, orphan NULL crew", boundCtx(""), "", orphanNull, http.StatusForbidden},
		// Positive: the honest sidecar of ownCrew, acting agent from that crew.
		{"crew-bound token, own crew, own agent", boundCtx(ownCrew), ownCrew, ownAgent, http.StatusOK},
		// Fallback: no agent named — attributed to the crew only.
		{"crew-bound token, own crew, no agent", boundCtx(ownCrew), ownCrew, "", http.StatusOK},
		// #1186 (unchanged): a crew-bound token may not name a sibling crew.
		{"crew-bound token, sibling crew", boundCtx(ownCrew), siblingCrew, "", http.StatusForbidden},
		// New: the agent must belong to the invoking crew.
		{"crew-bound token, own crew, sibling's agent", boundCtx(ownCrew), ownCrew, siblingAgent, http.StatusForbidden},
		{"crew-bound token, own crew, foreign-workspace agent", boundCtx(ownCrew), ownCrew, foreignAgent, http.StatusForbidden},
		{"crew-bound token, own crew, unknown agent", boundCtx(ownCrew), ownCrew, "agent-does-not-exist", http.StatusForbidden},
		// Master token stays workspace-wide (sibling crew allowed) …
		{"master token, sibling crew, sibling agent", masterCtx, siblingCrew, siblingAgent, http.StatusOK},
		// … but not tenant-wide, and the agent/crew pairing still holds.
		{"master token, foreign-workspace crew", masterCtx, foreignCrew, "", http.StatusForbidden},
		{"master token, sibling crew, own agent", masterCtx, siblingCrew, ownAgent, http.StatusForbidden},
		{"master token, unknown crew", masterCtx, "crew-does-not-exist", "", http.StatusForbidden},
		// An agent named without a crew: the crew is completed from the
		// agent's row, so the persisted pair is always consistent.
		{"master token, no crew, sibling agent → crew filled", masterCtx, "", siblingAgent, http.StatusOK},
		// Workspace-bound token without a crew: crew must be in the workspace.
		{"workspace-bound token, foreign-workspace crew", boundCtx(""), foreignCrew, "", http.StatusForbidden},
		{"workspace-bound token, sibling crew, sibling agent", boundCtx(""), siblingCrew, siblingAgent, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`DELETE FROM pipeline_runs WHERE pipeline_id = 'pipe-internal'`); err != nil {
				t.Fatalf("reset runs: %v", err)
			}
			body, _ := json.Marshal(map[string]any{
				"workspace_id": wsID, "slug": "internal-probe", "inputs": map[string]any{},
				"invoking_crew_id": tc.crew, "invoking_agent_id": tc.agent,
			})
			req := httptest.NewRequest("POST", "/x", strings.NewReader(string(body))).WithContext(tc.ctx)
			rr := httptest.NewRecorder()
			h.InternalRun(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id = 'pipe-internal'`).Scan(&n); err != nil {
				t.Fatalf("count runs: %v", err)
			}
			if tc.wantStatus == http.StatusForbidden {
				if n != 0 {
					t.Errorf("a refused request must not leave a run row behind (found %d)", n)
				}
				return
			}
			_, crew, agent, user, _ := runProvenance(t, h, "pipe-internal")
			wantCrew := tc.crew
			if wantCrew == "" && tc.agent != "" {
				wantCrew = siblingCrew // completed from the agent's row
			}
			if crew != wantCrew || agent != tc.agent {
				t.Errorf("persisted provenance crew=%q agent=%q, want crew=%q agent=%q", crew, agent, wantCrew, tc.agent)
			}
			if user != "" {
				t.Errorf("invoking_user_id = %q, want empty on an agent-invoked run", user)
			}
		})
	}
}
