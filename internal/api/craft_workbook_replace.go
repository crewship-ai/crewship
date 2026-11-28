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

	var commands []json.RawMessage
	var original [][][]any
	// Merge contiguous matches down a column so a large column replacement
	// needs a single engine write. Never include unmatched cells in a write.
	for col := range r.area.width() {
		for row := 0; row < r.area.height(); {
			first := row
			var old, next [][]any
			for row < r.area.height() && len(old) < nativeAgentWriteRows {
				value, isString := view.Result.Values[row][col].(string)
				// The typed Undo read prefixes only literal text with an
				// apostrophe. Numbers, booleans, blanks and formulas stay put.
				text, literal := strings.CutPrefix(value, "'")
				tail, matches := strings.CutPrefix(text, r.find)
				replacement := "'" + r.replacement + tail
				if !isString || !literal || !matches || replacement == value {
					break
				}
				if len(replacement) > craftsession.MaxInputBytes {
					return nil, nil, ErrCraftNativeAgentEngine
				}
				next = append(next, []any{replacement})
				old = append(old, []any{value})
				row++
			}
			if len(old) == 0 {
				row++
				continue
			}
			if len(commands) == 64 {
				return nil, nil, ErrCraftNativeAgentEngine
			}
			address := craftUndoA1(r.area.col1+col, r.area.row1+first)
			if len(old) > 1 {
				address += ":" + craftUndoA1(r.area.col1+col, r.area.row1+row-1)
			}
			command, _ := json.Marshal(map[string]any{"command": "range.setValues", "params": map[string]any{"range": address, "sheet": r.sheet, "values": next}})
			commands = append(commands, command)
			original = append(original, old)
		}
	}
	if len(commands) == 0 {
		return nil, nil, nil
	}
	writes, err := json.Marshal(commands)
	if err != nil {
		return nil, nil, ErrCraftNativeAgentEngine
	}
	return writes, original, nil
}
