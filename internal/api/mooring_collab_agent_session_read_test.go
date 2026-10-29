package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCollaborationAgentSessionReadRejectsUnverifiedState(t *testing.T) {
	for _, cell := range []*collaborationSessionStore{nil, {}} {
		if snapshot, err := cell.agentCheckpoint(nil, &collaborationAgentMandateStore{}, "untrusted", mooring.OwnerLease{}); err == nil || snapshot.Checkpoint.State != "" || snapshot.Targets != nil {
			t.Fatal("unverified agent read returned retained state")
		}
		called := false
		if err := cell.runAgentRead(nil, nil, "", mooring.OwnerLease{}, func(context.Context) error { called = true; return nil }); err == nil || called {
			t.Fatal("unverified agent operation invoked read work")
		}
	}
}
