package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Reserve against the complete actual native baseline and all live/retired RAM
// reservations. The numeric visitor is not source authority: the original human
// operation, concrete activated store, FULL writer and final fences remain
// mandatory. Failed operations may burn RAM entries, but publish no source.
func (d *collaborationHTTPDocument) reserveHumanOrigin(r *http.Request, admission collaborationHumanAdmission) (string, string, error) {
	if d == nil || r == nil || d.cell == nil || d.activation == nil || d.manager == nil || d.humans == nil {
		return "", "", errCollaborationHTTP
	}
	if err := d.activation.RequireHumanNativePrepare(); err != nil {
		return "", "", err
	}
	var head, origin string
	err := d.humans.runHumanOperation(r, admission.participant, func(ctx context.Context) error {
		snapshot, err := d.cell.store.ReadFullCheckpoint(ctx, d.owner)
		if err != nil {
			return err
		}
		basis := mooring.FullCheckpointBasis{Revision: string(snapshot.Checkpoint.Revision), Digest: string(snapshot.Checkpoint.Digest)}
		return d.cell.store.WithSessionOrigins(ctx, d.activation, basis, func(scoped context.Context, tx *sql.Tx, proof *mooring.SessionOriginInventory) error {
			if err := proof.Visit(scoped, tx, d.cell.store, d.owner, basis, func(allocated uint64, _ uint32) error {
				m := d.manager
				m.mu.Lock()
				defer m.mu.Unlock()
				if m.closed || m.docs[d.key] != d || allocated == 0 || allocated > (1<<53)-1 {
					return errCollaborationHTTP
				}
				used := m.usedOrigins[d.key]
				if used == nil {
					if len(m.usedOrigins) >= collaborationHTTPDocLimit {
						return mooring.ErrBatchCapacity
					}
					used = make(map[uint64]struct{})
					m.usedOrigins[d.key] = used
				}
				if _, present := used[allocated]; !present && len(used) >= 4096 {
					return mooring.ErrBatchCapacity
				}
				used[allocated] = struct{}{}
				return nil
			}); err != nil {
				return err
			}
			var err error
			origin, err = d.manager.origin(d.key)
			if err == nil {
				head = basis.Revision
			}
			return err
		})
	})
	if err != nil || origin == "" {
		return "", "", errCollaborationHTTP
	}
	return head, origin, nil
}
