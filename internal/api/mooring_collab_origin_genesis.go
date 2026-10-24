package api

import (
	"context"
	"net/http"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Actual original-human startup purpose and immutable store authority are
// retained. This unexposed variant inserts genesis correlation, not a source.
func (c *collaborationSessionStore) initializeNode(r *http.Request, owner mooring.OwnerLease, checkpoint []byte, node *mooring.OriginNodeData) error {
	return c.runStartup(r, owner, "initialize", func(ctx context.Context) error {
		return c.store.InitializeNode(ctx, owner, checkpoint, node)
	})
}
