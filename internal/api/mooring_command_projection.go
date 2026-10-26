package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
)

// DeliverProjection exposes only confirmed native document data, never the SQL
// checkpoint (which also contains private participant undo/history). The same
// original ingress and SQL authority remain held through the bounded physical
// send. Calling Projection alone grants no delivery authority.
func (d *commandProductDocument) DeliverProjection(r *http.Request, original collaborationHumanAdmission, send func(context.Context, []byte) error) error {
	if d == nil || d.store == nil || d.port == nil || send == nil {
		return errCommandStore
	}
	s := d.store
	return s.humans.runHumanOperation(r, original.participant, func(ctx context.Context) error {
		_, err := s.transaction(ctx, func(tx *sql.Tx) error {
			if _, err := s.admission(ctx, tx, original, false); err != nil {
				return err
			}
			confirmed, err := s.snapshot(ctx, tx)
			if err != nil {
				return err
			}
			projection, err := d.port.Projection(ctx)
			if err != nil {
				return err
			}
			// A lost Confirm may leave P behind SQL; an in-flight candidate must
			// never turn into a peer update or a misleading Saved indicator.
			if projection.Revision != confirmed.Revision {
				return errCommandUnknown
			}
			raw, err := json.Marshal(projection)
			if err != nil || len(raw) > commandCheckpointLimit {
				return errCommandStore
			}
			if _, err = s.humans.checkCaptured(ctx, tx, original, false, collaborationHTTPSendDeadline); err != nil {
				return err
			}
			sendContext, cancel := context.WithTimeout(ctx, collaborationHTTPSendDeadline)
			defer cancel()
			return send(sendContext, raw)
		})
		return err
	})
}
