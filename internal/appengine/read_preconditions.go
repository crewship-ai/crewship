package appengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
)

// ReadSet records the inputs used to derive a change. Rev is the document
// revision at which a cell or range was read, not the revision of the write.
// An explicitly empty set is appropriate for a write independent of old data.
type ReadSet struct {
	Cells  []CellRead  `json:"cells,omitempty"`
	Ranges []RangeRead `json:"ranges,omitempty"`
}
type CellRead struct {
	Sheet string `json:"sheet"`
	Cell  string `json:"cell"`
	Rev   int64  `json:"rev"`
}

// StructureRev is the import revision returned by Read. The pilot has no
// row/column insertion commands; a new import invalidates every prior range.
type RangeRead struct {
	Sheet        string `json:"sheet"`
	Start        string `json:"start"`
	End          string `json:"end"`
	Rev          int64  `json:"rev"`
	StructureRev int64  `json:"structure_rev"`
}

func (set *ReadSet) validate() error {
	if set == nil {
		return nil
	}
	if len(set.Cells) > 256 || len(set.Ranges) > 32 {
		return ErrInvalid
	}
	for _, cell := range set.Cells {
		if !validCell(cell.Sheet, cell.Cell, "") || cell.Rev < 1 {
			return ErrInvalid
		}
	}
	for _, region := range set.Ranges {
		if !validCell(region.Sheet, region.Start, "") || !validCell(region.Sheet, region.End, "") || region.Rev < 1 || region.StructureRev < 1 || region.StructureRev > region.Rev {
			return ErrInvalid
		}
		x1, y1 := cellPosition(region.Start)
		x2, y2 := cellPosition(region.End)
		if x1 > x2 || y1 > y2 {
			return ErrInvalid
		}
	}
	return nil
}

func cellPosition(address string) (int, int) {
	column, i := 0, 0
	for i < len(address) && address[i] >= 'A' && address[i] <= 'Z' {
		column = column*26 + int(address[i]-'A'+1)
		i++
	}
	row, _ := strconv.Atoi(address[i:])
	return column, row
}
func (region RangeRead) contains(cell CellInput) bool {
	if region.Sheet != cell.Sheet {
		return false
	}
	x, y := cellPosition(cell.Cell)
	x1, y1 := cellPosition(region.Start)
	x2, y2 := cellPosition(region.End)
	return x >= x1 && x <= x2 && y >= y1 && y <= y2
}

func checkReadSet(ctx context.Context, tx *sql.Tx, k Key, r Request, v State, p Principal) error {
	reject := func(reason string) error {
		return &PreconditionError{HeadRev: v.Rev, SnapshotRequired: true, Reason: reason}
	}
	if r.ReadSet == nil {
		// Old agent clients do not identify their input cells. Treat their whole
		// base revision as an input rather than silently accepting stale work.
		if strings.HasPrefix(p.AuthorityID, "agent:") && r.BaseRev != v.Rev {
			return reject("read_revision_changed")
		}
		return nil
	}
	minimum := v.Rev
	validRevision := func(rev int64) bool { return rev >= p.HistoryFloor && rev <= v.Rev && v.Rev-rev <= MaxRebaseWindow }
	for _, cell := range r.ReadSet.Cells {
		if !validRevision(cell.Rev) {
			return reject("read_history_unavailable")
		}
		minimum = min(minimum, cell.Rev)
	}
	for _, region := range r.ReadSet.Ranges {
		if !validRevision(region.Rev) {
			return reject("read_history_unavailable")
		}
		if region.StructureRev != v.StructureRev {
			return reject("read_structure_changed")
		}
		minimum = min(minimum, region.Rev)
	}
	if minimum == v.Rev {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT rev,kind,command,receipt FROM gridcraft_audit WHERE page_id=? AND rev>? AND rev<=? ORDER BY rev`, k.PageID, minimum, v.Rev)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := int64(0)
	for rows.Next() {
		var rev int64
		var kind string
		var raw, receipt []byte
		if err := rows.Scan(&rev, &kind, &raw, &receipt); err != nil {
			return err
		}
		seen++
		if kind != "cells" {
			return reject("read_structure_changed")
		}
		var command commandRecord
		if json.Unmarshal(raw, &command) != nil || command.Kind != "cells" {
			return reject("read_history_unavailable")
		}
		// Recalculation can alter a dependency that was not an authored target.
		// Addresses in the audit are capped; until full dependency tracking exists,
		// any derived change conservatively invalidates observations before it.
		var audit auditRecord
		if json.Unmarshal(receipt, &audit) != nil {
			return reject("read_history_unavailable")
		}
		if audit.Derived != nil && audit.Derived.Count > 0 {
			return reject("read_recalculation_changed")
		}
		for _, written := range command.Cells {
			for _, read := range r.ReadSet.Cells {
				if rev > read.Rev && read.Sheet == written.Sheet && read.Cell == written.Cell {
					return reject("read_cell_changed")
				}
			}
			for _, region := range r.ReadSet.Ranges {
				if rev > region.Rev && region.contains(written) {
					return reject("read_range_changed")
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if seen != v.Rev-minimum {
		return reject("read_history_unavailable")
	}
	return nil
}
