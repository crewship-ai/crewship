package artifacts

import "context"

// RevisionStorage is the host's confirmed-content boundary. Adapters preserve
// their native request schemas, transactional authorization and receipt types;
// the protocol does not force unlike data models into a second truth store.
// Both a native GridCraft adapter and the generic JSON adapter implement it.
type RevisionStorage[K, Request, State, Receipt, Authorize any] interface {
	Read(context.Context, K, Authorize) (State, error)
	Apply(context.Context, K, Request, Authorize) (Receipt, error)
}
