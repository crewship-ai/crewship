package api

import (
	"context"

	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// All actual inputs for the largest agent command batch the Undo engine
// supports. Empty cells in a footprint never need to be materialized.
const craftRealtimeHistoryCellLimit = craftUndoMaxCells * craftUndoBatch

type craftRealtimeEffect struct {
	base, head [32]byte
	lastAgent  bool
}

func (e craftRealtimeEffect) effectiveAgent() bool { return e.lastAgent && e.base != e.head }

// Inspect each authenticated journal step against the two immutable versions.
// Declared ranges bound the proof but never masquerade as actual edits. Keep
// the first and last raw inputs plus the last actor with a real effect.
func craftProveRealtimeHistory(ctx context.Context, s *mooring.NativeStore, scope mooring.EditLeaseScope, steps []craftRealtimeVersion, headVersion int64, base, head []byte) (map[craftRealtimeCell]craftRealtimeEffect, error) {
	effects := make(map[craftRealtimeCell]craftRealtimeEffect)
	before := base
	for _, entry := range steps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		after := head
		if entry.version != headVersion {
			m, _, err := s.ReadVersion(ctx, scope, entry.version)
			if err != nil {
				return nil, errCraftRealtimeUnproved
			}
			file, err := s.DocumentFile(m)
			if err != nil {
				return nil, errCraftRealtimeUnproved
			}
			after = file.Content
		}
		if lost := craftWorkbookPartsLost(ctx, before, after, entry.edits); lost != nil {
			return nil, &craftsession.FidelityLossError{Parts: lost}
		}
		old, err := craftRealtimeInputs(before, entry.edits, craftRealtimeHistoryCellLimit)
		if err != nil {
			return nil, err
		}
		newer, err := craftRealtimeInputs(after, entry.edits, craftRealtimeHistoryCellLimit)
		if err != nil {
			return nil, err
		}
		remember := func(cell craftRealtimeCell, from, to [32]byte) {
			if from == to {
				return
			}
			e, ok := effects[cell]
			if !ok {
				e.base = from
			}
			e.head, e.lastAgent = to, entry.agent
			effects[cell] = e
		}
		for cell, value := range newer {
			remember(cell, old[cell], value)
			delete(old, cell)
		}
		for cell, value := range old {
			remember(cell, value, [32]byte{})
		}
		if len(effects) > craftRealtimeHistoryCellLimit {
			return nil, errCraftRealtimeUnproved
		}
		before = after
	}
	return effects, nil
}
