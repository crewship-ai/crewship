package api

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestReviewSubmissionRefusesForeignMandateDatabaseEvenWithOriginalSQLProof(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	handle := x.attach(t, x.prepare(t))
	stamp, err := collaborationHumanLifecycleStamp(x.fence)
	if err != nil {
		t.Fatal(err)
	}
	h := &collaborationHumanRegistry{lifecycle: stamp, bindingID: "review-db-binding", runtime: &mooring.Runtime{}, pages: x.f.pages, fence: x.fence, clock: x.clock, scope: x.key}
	registry, err := newCollaborationSubmissionRegistry(h)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := h.storeBinding()
	// The exact live mandate was originally created against the real fixture DB.
	// A later host miswire must not let the registry's Tx stand in for its DB.
	foreignDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer foreignDB.Close()
	foreignPages := NewPageHandler(foreignDB, nil, newTestLogger())
	foreignPages.artifactMasterToken = x.f.pages.artifactMasterToken
	x.store.pages = foreignPages
	request := x.request()
	var participant *collaborationAgentSubmissionParticipant
	err = registry.run(request.Context(), binding, func(ctx context.Context) error {
		var err error
		participant, err = registry.admitAgent(ctx, request, x.store, handle)
		return err
	})
	if err == nil || participant != nil || len(registry.agents) != 0 {
		t.Fatal("foreign mandate database admitted against registry SQL proof")
	}
}
