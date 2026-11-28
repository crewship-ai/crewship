package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/crewship-ai/crewship/internal/craftsession"
)

type craftWorkbookReplace struct {
	sheet, address, find, replacement string
	area                              nativeAgentEngineArea
}

// A replacement is a standalone operation. It has to resolve its exact write
// footprint against the same head that anchors the save and its inverse.
func parseCraftWorkbookReplace(commands json.RawMessage) (*craftWorkbookReplace, error) {
	var list []struct {
		Command string          `json:"command"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(commands, &list); err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	for _, op := range list {
		if op.Command != "range.replace" {
			continue
		}
		if len(list) != 1 {
			return nil, ErrCraftNativeAgentEngine
		}
		params, ok := nativeAgentEngineObject(op.Params, "range", "sheet", "find", "replace", "match")
		if !ok || !nativeAgentEngineOptionalSheet(params) {
			return nil, ErrCraftNativeAgentEngine
		}
		var out craftWorkbookReplace
		var mode string
		for name, dst := range map[string]*string{"sheet": &out.sheet, "range": &out.address, "find": &out.find, "replace": &out.replacement, "match": &mode} {
			if len(params[name]) == 0 || params[name][0] != '"' || json.Unmarshal(params[name], dst) != nil ||
				strings.ContainsRune(*dst, 0) || len(*dst) > craftsession.MaxInputBytes {
				return nil, ErrCraftNativeAgentEngine
			}
		}
		var good bool
		out.area, good = nativeAgentEngineRange(params["range"])
		if !good || out.area.cells() > nativeAgentReadCells || out.area.width() > nativeAgentWriteCols || out.sheet == "" ||
			strings.ContainsRune(out.sheet, '\'') || mode != "prefix" || out.find == "" {
			return nil, ErrCraftNativeAgentEngine
		}
		return &out, nil
	}
	return nil, nil
}

func (r craftWorkbookReplace) plan(ctx context.Context, inspect CraftNativeAgentInspect, profile CraftNativeDocumentProfile, workbook []byte) (json.RawMessage, [][][]any, error) {
	query, _ := json.Marshal(map[string]any{"command": "sheet.read", "params": map[string]any{"range": r.address, "sheet": r.sheet, "formulas": true}})
	answer, err := inspect(craftNativeUndoContext(ctx), profile, workbook, query)
	if err != nil {
		return nil, nil, err
	}
	var view struct {
		Result struct {
			Sheet  string  `json:"sheet"`
			Values [][]any `json:"values"`
		} `json:"result"`
	}
	if json.Unmarshal(answer, &view) != nil || !strings.EqualFold(view.Result.Sheet, r.sheet) || len(view.Result.Values) != r.area.height() {
		return nil, nil, ErrCraftNativeAgentEngine
	}
	for _, row := range view.Result.Values {
		if len(row) != r.area.width() {
			return nil, nil, ErrCraftNativeAgentEngine
		}
	}

	type span struct{ first, last int }
	type block struct {
		startRow int
		columns  span
		old, new [][]any
	}
	var blocks []block
	var prior map[span]int
	var writeBytes, inverseBytes int
	// Turn each row's consecutive matches into runs, then extend any
	// identical run from the preceding row without enclosing unmatched cells.
	for row, values := range view.Result.Values {
		current := make(map[span]int)
		for col := 0; col < len(values); {
			first := col
			var old, next []any
			for col < len(values) {
				value, isString := values[col].(string)
				// The typed Undo read prefixes only literal text with an
				// apostrophe. Numbers, booleans, blanks and formulas stay put.
				text, literal := strings.CutPrefix(value, "'")
				tail, matches := strings.CutPrefix(text, r.find)
				if !isString || !literal || !matches || r.find == r.replacement {
					break
				}

				length := 1 + len(r.replacement) + len(tail)
				writeBytes += length
				inverseBytes += len(value)
				if length > craftsession.MaxInputBytes || writeBytes > nativeAgentCommandBytes ||
					writeBytes+inverseBytes > craftVersionOpsMaxBytes {
					return nil, nil, ErrCraftNativeAgentEngine
				}
				next = append(next, "'"+r.replacement+tail)
				old = append(old, value)
				col++
			}
			if len(old) == 0 {
				col++
				continue
			}

			columns := span{first, col - 1}
			index, continues := prior[columns]
			if !continues || len(blocks[index].old) == nativeAgentWriteRows {
				if len(blocks) == nativeAgentMaxCommands {
					return nil, nil, ErrCraftNativeAgentEngine
				}
				index = len(blocks)
				blocks = append(blocks, block{startRow: row, columns: columns})
			}
			blocks[index].old = append(blocks[index].old, old)
			blocks[index].new = append(blocks[index].new, next)
			current[columns] = index
		}
		prior = current
	}
	if len(blocks) == 0 {
		return nil, nil, nil
	}
	commands := make([]json.RawMessage, 0, len(blocks))
	original := make([][][]any, 0, len(blocks))
	for _, block := range blocks {
		address := craftUndoA1(r.area.col1+block.columns.first, r.area.row1+block.startRow)
		if len(block.old) > 1 || block.columns.first != block.columns.last {
			address += ":" + craftUndoA1(r.area.col1+block.columns.last, r.area.row1+block.startRow+len(block.old)-1)
		}
		command, err := json.Marshal(map[string]any{"command": "range.setValues", "params": map[string]any{"range": address, "sheet": r.sheet, "values": block.new}})
		if err != nil {
			return nil, nil, ErrCraftNativeAgentEngine
		}
		commands = append(commands, command)
		original = append(original, block.old)
	}
	writes, err := json.Marshal(commands)
	if err != nil || len(writes) > nativeAgentCommandBytes {
		return nil, nil, ErrCraftNativeAgentEngine
	}
	return writes, original, nil
}
