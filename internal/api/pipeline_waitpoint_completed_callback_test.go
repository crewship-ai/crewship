package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Keep the real decision and workspace lookup paths. A run lookup is the
// callback's first step toward resuming execution and must never be reached
// after the canonical store refuses an already completed decision.
type completedCallbackWaitpoints struct {
	*pipeline.SQLWaitpointStore
	runLookups int
}

func (s *completedCallbackWaitpoints) RunIDForToken(ctx context.Context, token string) (string, error) {
	s.runLookups++
	return s.SQLWaitpointStore.RunIDForToken(ctx, token)
}

func TestCompleteWaitpointToken_AlreadyDecidedDoesNotResume(t *testing.T) {
	for _, approved := range []bool{true, false} {
		name := "denied"
		if approved {
			name = "approved"
		}
		t.Run(name, func(t *testing.T) {
			h, user, workspace := newPipelineHandlerForCRUDTest(t)
			store, token := seedPendingWaitpoint(t, h, workspace)
			if err := store.CompleteApproval(t.Context(), workspace, token, approved, user, `{"winner":"original"}`); err != nil {
				t.Fatal("complete original decision:", err)
			}
			type decision struct {
				status, actor, payload, decidedAt string
			}
			read := func() decision {
				t.Helper()
				var d decision
				if err := h.db.QueryRowContext(t.Context(), `SELECT status,decided_by_user_id,decision_payload,decided_at FROM pipeline_waitpoints WHERE token=?`, token).Scan(&d.status, &d.actor, &d.payload, &d.decidedAt); err != nil {
					t.Fatal("read original decision:", err)
				}
				return d
			}
			before := read()
			if before.status != name || before.actor != user || before.payload != `{"winner":"original"}` || before.decidedAt == "" {
				t.Fatal("original completion did not persist the expected winner")
			}
			observed := &completedCallbackWaitpoints{SQLWaitpointStore: store}
			h.SetWaitpointStore(observed)
			body := fmt.Sprintf(`{"approved":%t,"payload":{"winner":"replacement"}}`, !approved)
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req.SetPathValue("token", token)
			response := httptest.NewRecorder()
			h.CompleteWaitpointToken(response, req)
			if response.Code != http.StatusConflict {
				t.Fatalf("completed callback returned HTTP%d, want 409", response.Code)
			}
			if after := read(); after != before {
				t.Fatal("completed callback changed the original status, winner, payload or decision timestamp")
			}
			if observed.runLookups != 0 {
				t.Fatalf("completed callback entered the resume path %d times", observed.runLookups)
			}
		})
	}
}
