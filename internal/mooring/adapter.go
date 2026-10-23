package mooring

import (
	"context"
	"database/sql"
	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Key retains the host's writer-fenced transaction; applications never receive it.
type Key struct {
	Transaction *sql.Tx
	PageID      string
	Manifest    Manifest
}
type Authorize func(context.Context, *sql.Tx) (string, error)
type JSONAdapter struct{}

func (JSONAdapter) Read(ctx context.Context, key Key, authorize Authorize) (State, error) {
	if _, e := authorize(ctx, key.Transaction); e != nil {
		return State{}, e
	}
	return (JSONStorage{}).Read(ctx, key.Transaction, key.PageID)
}
func (JSONAdapter) Apply(ctx context.Context, key Key, request Request, authorize Authorize) (Receipt, error) {
	actor, e := authorize(ctx, key.Transaction)
	if e != nil {
		return Receipt{}, e
	}
	return (JSONStorage{}).Commit(ctx, key.Transaction, key.PageID, actor, key.Manifest, request)
}

var _ artifacts.RevisionStorage[Key, Request, State, Receipt, Authorize] = JSONAdapter{}
