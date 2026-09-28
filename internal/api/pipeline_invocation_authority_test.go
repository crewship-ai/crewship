package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestDeferredRoutineAuthorityCannotBeForgedAndIsRevalidated(t *testing.T) {
	h, _, ws := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "unused"})
	user := seedMemberWithCapabilities(t, h.db, ws, "MEMBER", `["routine.run"]`, "authority-user")
	seedPinnable(t, h, user, ws, "gate-probe")
	rr := httptest.NewRecorder()
	h.Run(rr, runReqAs(t, user, ws, "gate-probe", "MEMBER", `{"delay_seconds":1,"invocation_authority":"","metadata":{"source":"page_action","invocation_authority":""}}`))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("enqueue=%d %s", rr.Code, rr.Body.String())
	}
	pending := pipeline.NewPendingRunStore(h.db)
	due, err := pending.DueRuns(t.Context(), time.Now().Add(time.Minute), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due=%+v err=%v", due, err)
	}
	pr := due[0]
	if pr.InvocationAuthority != pipeline.RoutineRunAuthority || pr.InvokingUserID != user {
		t.Fatalf("wrong server authority: %+v", pr)
	}
	// Keep membership but remove its capability. No cache invalidation: execution
	// must read current policy, including when API admission cached an old grant.
	if _, err := h.db.Exec(`UPDATE workspace_members SET capabilities='[]' WHERE workspace_id=? AND user_id=?`, ws, user); err != nil {
		t.Fatal(err)
	}
	_, err = h.newExecutor().Run(t.Context(), pipeline.RunInput{PipelineID: pr.PipelineID, WorkspaceID: pr.WorkspaceID, InvokingUserID: pr.InvokingUserID, InvocationAuthority: pr.InvocationAuthority, MetadataJSON: pr.MetadataJSON, Mode: pipeline.ModeRun})
	if !errors.Is(err, pipeline.ErrInvocationAuthorityRevoked) {
		t.Fatalf("revoked deferred permission was not denied: %v", err)
	}
}
