package restrictedworkflow

import (
	"context"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// PageActionAvailable checks a host-selected declaration without creating a
// chat or execution authority. Invocation must recheck the same live policies.
func (s *Service) PageActionAvailable(ctx context.Context, user, workspace string, action pipeline.PageActionInvocation) error {
	store := access.Store{DB: s.db}
	membership, err := store.Membership(ctx, user, workspace)
	if err != nil || membership.Mode != "restricted" || pipeline.CheckDeclaredPageAction(ctx, s.db, user, workspace, action) != nil {
		return ErrDenied
	}
	if _, ok := s.executor.(proofExecutor); !ok {
		return ErrDenied
	}
	graph, err := s.compileGraph(ctx, user, workspace, action.PipelineID)
	if err != nil {
		return err
	}
	for _, right := range graph.Rights {
		if store.Check(ctx, user, workspace, right) != nil {
			return ErrDenied
		}
	}
	return nil
}
