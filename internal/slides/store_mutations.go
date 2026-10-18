package slides

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Targeted patches reuse the initialized store's fenced transaction, double
// current-authority check, immutable revision writer, quotas and asset pins.
// A receipt guard may record a result in that transaction, including replay;
// callers must not publish any receipt or result when the operation returns an
// error. Historical object IDs stay reserved after deletion. This native
// candidate path does not add comments, endpoints or a format-selection result.
func insertedIDs(request PatchRequest) map[string]string {
	ids := map[string]string{}
	for _, op := range request.Operations {
		if op.Op == "insert_slide" && op.Slide != nil {
			for id, kind := range ObjectIDs(Deck{Slides: []Slide{*op.Slide}}) {
				ids[id] = kind
			}
		}
		if op.Op == "insert_element" && op.Element != nil {
			ids[op.Element.ID] = "element"
		}
	}
	return ids
}

// PatchReceiptGuard is trusted compiled request-result integration. It runs for
// a new revision and exact receipt replay inside the writer transaction, before
// final live authorization/commit. It cannot replace revision asset hooks.
type PatchReceiptGuard func(context.Context, *sql.Tx, Key, string, Receipt) error

func (s *Store) Patch(ctx context.Context, key Key, request PatchRequest, authorize Authorize) (Receipt, error) {
	return s.PatchWithReceipt(ctx, key, request, authorize, nil)
}
func (s *Store) PatchWithReceipt(ctx context.Context, key Key, request PatchRequest, authorize Authorize, onReceipt PatchReceiptGuard) (result Receipt, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodePatch(raw)
	if err != nil {
		return result, err
	}
	hash, err := requestHash(key, "patch", request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := receipt(ctx, tx, key, request.MutationID, hash, actor, "patch")
		if err != nil {
			return err
		}
		if found {
			result = previous
			if onReceipt != nil {
				return onReceipt(ctx, tx, key, actor, result)
			}
			return nil
		}
		current, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		if current.Revision != request.BaseRev || (request.IfOrderVersion != nil && *request.IfOrderVersion != current.OrderVersion) {
			return ErrConflict
		}
		candidate, err := ApplyPatch(current.Deck, request)
		if err != nil {
			if errors.Is(err, ErrConflict) {
				return err
			}
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		result, err = s.write(ctx, tx, key, current, candidate, request.MutationID, "patch", hash, actor, insertedIDs(request), false)
		if err != nil {
			return err
		}
		if onReceipt != nil {
			return onReceipt(ctx, tx, key, actor, result)
		}
		return nil
	})
	return
}
