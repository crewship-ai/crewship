package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
)

// Private source seams do not change the frozen card/answer wire contract.
// Scan returns bounded IDs only, closing its rows before Read is called. Public
// pagination counts only successful authorized Read projections, so unsupported
// or hidden candidates cannot announce existence through page boundaries.
type decisionSourceRow struct {
	ID       string
	Position int64
}

type decisionSourceAdapter struct {
	SourceType string
	Read       func(context.Context, chataudience.Receipt, string) (ChatCardProjection, error)
	Upper      func(context.Context, chataudience.Receipt) (int64, error)
	// Scan arguments after the audience are after-position, upper bound, limit.
	Scan func(context.Context, chataudience.Receipt, int64, int64, int) ([]decisionSourceRow, error)
	// Resolve shares the source's canonical transaction/CAS and postcommit doors.
	// The actor/body-bound operation is created by the common strict dispatcher.
	Resolve func(*http.Request, *decisionOperation, chatentities.DecisionAnswer) error
}

var errDecisionInput = errors.New("invalid decision answer")
var errDecisionDenied = errTextDecisionDenied

func (r *Router) decisionSourceAdapters() []decisionSourceAdapter {
	return []decisionSourceAdapter{r.escalationDecisionAdapter(), r.waitpointDecisionAdapter(), r.runApprovalDecisionAdapter()}
}
func (r *Router) decisionSourceAdapterFor(kind string) (decisionSourceAdapter, bool) {
	for _, adapter := range r.decisionSourceAdapters() {
		if adapter.SourceType == kind {
			return adapter, true
		}
	}
	return decisionSourceAdapter{}, false
}
