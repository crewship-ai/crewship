package api

import (
	"errors"
	"testing"
)

// A run that a human message bound to artifacts (artifact_run_scope) learns
// nothing from them: the server refuses its memory writes, whatever the agent
// was offered. Learning from a Sheet stays off until its owner decides it.
func TestChatRunWithArtifactsWritesNoMemory(t *testing.T) {
	for name, attach := range map[string]bool{"with a Sheet": true, "without artifacts": false} {
		t.Run(name, func(t *testing.T) {
			s := newChatRunScenario(t, true)
			s.send("what is in the sheet", attach)
			run := s.resolver.runID
			h := NewMemoryMutationHandler(s.f.rig.db, storageDir(t), storageDir(t), newTestLogger(), s.f.pages.artifactMasterToken)
			generation, err := h.attemptGeneration(t.Context(), run)
			if err != nil {
				t.Fatalf("the chat run has no memory attempt: %v", err)
			}
			err = h.authorizeRun(s.f.a.WorkspaceID, s.f.a.AgentID, run, generation)(t.Context())
			if attach && !errors.Is(err, errMemoryArtifactRun) {
				t.Fatalf("a run bound to a Sheet may write memory: %v", err)
			}
			if !attach && err != nil {
				t.Fatalf("an ordinary chat run is refused memory: %v", err)
			}
		})
	}
}

// Nor does it feed learning when it ends: UpdateRun asks artifactScopedRun
// before the outcome verdict (whose summary the consolidator reads) and the
// post-run consolidation, and skips both for such a run.
func TestChatRunWithArtifactsFeedsNoLearningWhenItEnds(t *testing.T) {
	for name, attach := range map[string]bool{"with a Sheet": true, "without artifacts": false} {
		t.Run(name, func(t *testing.T) {
			s := newChatRunScenario(t, true)
			s.send("what is in the sheet", attach)
			scoped, err := artifactScopedRun(t.Context(), s.f.rig.db, s.resolver.runID)
			if err != nil || scoped != attach {
				t.Fatalf("scoped %v %v, attached %v", scoped, err, attach)
			}
		})
	}
	// An unknown run is not scoped; a closed database is an error, never "not scoped".
	s := newChatRunScenario(t, true)
	if scoped, err := artifactScopedRun(t.Context(), s.f.rig.db, "no-such-run"); err != nil || scoped {
		t.Fatalf("unknown run: %v %v", scoped, err)
	}
}
