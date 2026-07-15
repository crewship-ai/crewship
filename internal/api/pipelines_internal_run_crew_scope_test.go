package api

// TestInternalRun_CrewBoundToken_RejectsSiblingInvokingCrewID is issue
// #1186: InternalRun (POST /api/v1/internal/pipelines/run) never validated
// invoking_crew_id against the caller's token binding at all — unlike the
// other ~11 handlers assertBoundCrewWorkspaceDB already covers. A crew-bound
// (crwv1) sidecar token for crew A could set invoking_crew_id to a SIBLING
// crew B (same workspace) and have the run — and any waitpoint approval
// card it raises, which shows invoking_crew_id as the "From" line — falsely
// attributed to crew B. assertInternalTokenWorkspace alone doesn't catch
// this: it only pins workspace_id, not the body-carried crew field.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalRun_CrewBoundToken_RejectsSiblingInvokingCrewID(t *testing.T) {
	h, db, _, wsID, ownCrewID := cov2PCRig(t)
	h.runner = pipelineAgentRunnerStub{}
	siblingCrewID := seedCrewRow(t, db, "sibling-crew", wsID, "Sibling", "sibling-crew-slug")
	covPCInsertPipeline(t, db, wsID, "p-run", "p-run", ownCrewID, 0, 1, 0)

	boundCtx := func(crew string) context.Context {
		ctx := context.WithValue(context.Background(), ctxInternalTokenWS, wsID)
		return context.WithValue(ctx, ctxInternalTokenCrew, crew)
	}

	t.Run("own_crew_allowed_to_proceed_past_the_guard", func(t *testing.T) {
		body := `{"workspace_id":"` + wsID + `","slug":"p-run","invoking_crew_id":"` + ownCrewID + `"}`
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body)).WithContext(boundCtx(ownCrewID))
		rr := httptest.NewRecorder()
		h.InternalRun(rr, req)
		if rr.Code == http.StatusForbidden {
			t.Fatalf("own crew must not be rejected; body=%s", rr.Body.String())
		}
	})

	t.Run("sibling_crew_403", func(t *testing.T) {
		body := `{"workspace_id":"` + wsID + `","slug":"p-run","invoking_crew_id":"` + siblingCrewID + `"}`
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body)).WithContext(boundCtx(ownCrewID))
		rr := httptest.NewRecorder()
		h.InternalRun(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (crew-bound token attributing a run to a sibling crew); body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("master_token_any_crew_still_allowed", func(t *testing.T) {
		// No binding in context (host-side/master-token caller) — unaffected,
		// still workspace-wide by design.
		body := `{"workspace_id":"` + wsID + `","slug":"p-run","invoking_crew_id":"` + siblingCrewID + `"}`
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h.InternalRun(rr, req)
		if rr.Code == http.StatusForbidden {
			t.Fatalf("unbound (master-token) caller must stay workspace-wide; body=%s", rr.Body.String())
		}
	})
}
