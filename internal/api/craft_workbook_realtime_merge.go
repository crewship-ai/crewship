package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

var errCraftRealtimeUnproved = errors.New("workbook changes could not be proved")

const craftRealtimeCellLimit = 2000

type craftRealtimeCell struct {
	sheet    string
	col, row int
}

func (c craftRealtimeCell) address() string {
	return "'" + strings.ReplaceAll(c.sheet, "'", "''") + "'!" + craftUndoA1(c.col, c.row)
}

func (c craftRealtimeCell) wireRange() craftSheetRange {
	return craftSheetRange{Sheet: c.sheet, A1: craftUndoA1(c.col, c.row)}
}

// craftRealtimeInputs extracts only cells in the declared edit scope. The
// caller separately verifies the projection of everything outside that scope.
func craftRealtimeInputs(raw []byte, edits craftWorkbookEdits) (map[craftRealtimeCell][32]byte, error) {
	z, parts, ok := craftWorkbookPartFiles(raw)
	if !ok {
		return nil, errCraftRealtimeUnproved
	}
	sheets, err := craftsession.WorkbookWorksheetParts(z)
	if err != nil {
		return nil, err
	}
	inputs := make(map[craftRealtimeCell][32]byte)
	edits.observe = func(sheet string, col, row int, input [32]byte) error {
		if input == ([32]byte{}) {
			return nil
		}
		if len(inputs) >= craftRealtimeCellLimit {
			return errCraftRealtimeUnproved
		}
		inputs[craftRealtimeCell{sheet, col, row}] = input
		return nil
	}
	for name, sheet := range sheets {
		if len(edits.cells[strings.ToLower(sheet)]) == 0 {
			continue
		}
		if _, err := craftWorksheetProjection(parts[name], sheet, edits); err != nil {
			return nil, err
		}
	}
	return inputs, nil
}

func craftRealtimeChangedCells(base, submitted []byte, edits craftWorkbookEdits) ([]craftRealtimeCell, error) {
	if edits.allowScalarValues {
		return nil, errCraftRealtimeUnproved
	}
	original, err := craftRealtimeInputs(base, edits)
	if err != nil {
		return nil, err
	}
	candidate, err := craftRealtimeInputs(submitted, edits)
	if err != nil {
		return nil, err
	}
	changed := make([]craftRealtimeCell, 0, len(candidate))
	for cell, value := range candidate {
		if original[cell] != value {
			changed = append(changed, cell)
		}
		delete(original, cell)
	}
	for cell := range original {
		changed = append(changed, cell)
	}
	if len(changed) > craftRealtimeCellLimit {
		return nil, errCraftRealtimeUnproved
	}
	sort.Slice(changed, func(i, j int) bool {
		a, b := changed[i], changed[j]
		if a.sheet != b.sheet {
			return a.sheet < b.sheet
		}
		if a.row != b.row {
			return a.row < b.row
		}
		return a.col < b.col
	})
	return changed, nil
}

type craftRealtimeChanges struct {
	agent       craftWorkbookEdits
	human       craftWorkbookEdits
	agentRanges []craftSheetRange
}

// Check that the journal covers the whole interval. A missing journal, unknown
// write, or change by another human cannot establish the authority to rebase.
func craftReadRealtimeChanges(ctx context.Context, db *sql.DB, scope mooring.EditLeaseScope, user, pin string, base, head int64) (craftRealtimeChanges, error) {
	out := craftRealtimeChanges{agentRanges: []craftSheetRange{}}
	rows, err := db.QueryContext(ctx, `SELECT version,base_version,actor_kind,actor_id,engine_pin,operation_id,replayable,ops
		FROM craft_version_ops WHERE workspace_id=? AND page_id=? AND version>? AND version<=? ORDER BY version LIMIT 129`, scope.WorkspaceID, scope.ArtifactID, base, head)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	next := base
	for rows.Next() {
		var version, from int64
		var kind, actor, engine, id, ops string
		var replayable bool
		if err := rows.Scan(&version, &from, &kind, &actor, &engine, &id, &replayable, &ops); err != nil {
			return out, err
		}
		if from != next || version != from+1 || engine != pin || head-base > 128 {
			return out, errCraftRealtimeUnproved
		}
		next = version
		edits := &out.agent
		if kind == "human" && actor == user && strings.HasPrefix(id, "realtime:") {
			edits = &out.human
		} else if kind != "agent" || !replayable {
			return out, errCraftRealtimeUnproved
		}
		reads, ok := craftUndoReads(json.RawMessage(ops))
		if !ok {
			return out, errCraftRealtimeUnproved
		}
		for _, read := range reads {
			if !edits.add(read.address) {
				return out, errCraftRealtimeUnproved
			}
			if kind == "agent" {
				out.agentRanges = append(out.agentRanges, craftSheetRange{Sheet: read.sheet, A1: read.rng})
			}
		}
		if len(out.agentRanges) > craftRealtimeCellLimit {
			return out, errCraftRealtimeUnproved
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if next != head {
		return out, errCraftRealtimeUnproved
	}
	return out, nil
}

func craftRealtimeOps(cells []craftRealtimeCell) json.RawMessage {
	ops := make([]craftUndoOp, 0, len(cells))
	for _, cell := range cells {
		ops = append(ops, craftUndoOp{Command: "touched", Params: map[string]json.RawMessage{"range": json.RawMessage(strconv.Quote(cell.address()))}})
	}
	raw, _ := json.Marshal(ops)
	return raw
}

func craftRealtimeReplay(ctx context.Context, agents *craftNativeAgentManager, p CraftNativeDocumentProfile, head, submitted []byte, cells []craftRealtimeCell) ([]byte, error) {
	if len(cells) == 0 {
		return head, nil
	}
	ctx = craftNativeUndoContext(ctx)
	ops := craftRealtimeOps(cells)
	before, ok := craftUndoReadValues(ctx, agents.config.AgentInspect, p, submitted, ops, true)
	if !ok {
		return nil, errCraftRealtimeUnproved
	}
	commands := make([]json.RawMessage, 0, len(cells))
	var edits craftWorkbookEdits
	for i, cell := range cells {
		if len(before[i]) != 1 || len(before[i][0]) != 1 {
			return nil, errCraftRealtimeUnproved
		}
		edits.add(cell.address())
		value := before[i][0][0]
		cmd := "range.setValues"
		params := map[string]any{"sheet": cell.sheet, "range": craftUndoA1(cell.col, cell.row)}
		if value == nil || value == "" {
			cmd = "range.clear"
		} else {
			params["values"] = [][]any{{value}}
		}
		raw, err := json.Marshal(map[string]any{"command": cmd, "params": params})
		if err != nil {
			return nil, err
		}
		commands = append(commands, raw)
	}
	candidate := head
	for offset := 0; offset < len(commands); offset += craftUndoBatch {
		batch, _ := json.Marshal(commands[offset:min(offset+craftUndoBatch, len(commands))])
		var err error
		candidate, err = agents.config.AgentApply(ctx, p, candidate, batch)
		if err != nil {
			return nil, err
		}
	}
	if lost := craftWorkbookPartsLost(head, candidate, edits); lost != nil {
		return nil, &craftsession.FidelityLossError{Parts: lost}
	}
	after, ok := craftUndoReadValues(ctx, agents.config.AgentInspect, p, candidate, ops, true)
	if !ok {
		return nil, errCraftRealtimeUnproved
	}
	for i := range cells {
		if len(after[i]) != 1 || len(after[i][0]) != 1 || !craftUndoSameInput(after[i][0][0], before[i][0][0]) {
			return nil, errCraftRealtimeUnproved
		}
	}
	return candidate, nil
}
