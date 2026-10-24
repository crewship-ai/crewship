package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func TestCollaborationSessionCommitterRefusesUnverifiedFactory(t *testing.T) {
	for _, cell := range []*collaborationSessionStore{nil, {}} {
		if s, err := newCollaborationSessionCommitter(t.Context(), cell, &collaborationSubmissionRegistry{}, mooring.OwnerLease{}); err == nil || s != nil {
			t.Fatal("unverified factory acquired commit capability")
		}
	}
	for _, s := range []*collaborationSessionCommitter{nil, {}} {
		if result, err := s.submit(t.Context(), protocol.SessionSubmitBatch{}); err == nil || result.Checkpoint.State != "" || result.Receipt.Changes != nil {
			t.Fatal("unverified handler returned a receipt or checkpoint")
		}
	}
}
