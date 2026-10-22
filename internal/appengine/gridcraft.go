package appengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

const GridCraftVersion = "0.10.0"
const GridCraftCommit = "2ebdf164c2c9c536122e0a2cffe79078b5c818ac"
const GridCraftBinarySHA256 = "000100860eb14b641da48e5e0d2dcd6dcc5ddcd65a4b267a0ef2fcdfadcdd984"

// EngineResult includes the canonical reloaded base as well as the candidate.
// The host refuses changes introduced by reopening a supposedly confirmed file.
type EngineResult struct {
	Base   json.RawMessage `json:"base"`
	Result json.RawMessage `json:"result"`
}
type Runner interface {
	Run(context.Context, []byte, Command) (EngineResult, error)
}

// GridCraft is a narrow, statically compiled adapter. Only the hardened operator
// runner may be used for deployment; an arbitrary executable is not a sandbox.
type GridCraft struct{ Runner Runner }

func (GridCraft) Identity() string {
	return "org.storytold.gridcraft@" + GridCraftVersion + "+sha256:" + GridCraftBinarySHA256
}
func (g GridCraft) Prepare(ctx context.Context, b Binding, s State, c Command) (Candidate, error) {
	if err := c.validate(); err != nil {
		return Candidate{}, err
	}
	if g.Runner == nil {
		return Candidate{}, ErrDisabled
	}
	input := s.Snapshot
	if c.Kind == "import" {
		input = c.Snapshot
	}
	if len(input) == 0 || len(input) > MaxSnapshotBytes {
		return Candidate{}, ErrInvalid
	}
	result, err := g.Runner.Run(ctx, input, c)
	if err != nil {
		return Candidate{}, err
	}
	if len(result.Base) > MaxSnapshotBytes || len(result.Result) > MaxSnapshotBytes {
		return Candidate{}, ErrInvalid
	}
	base, err := decodeWorkbook(result.Base)
	if err != nil {
		return Candidate{}, err
	}
	candidate, err := decodeWorkbook(result.Result)
	if err != nil {
		return Candidate{}, err
	}
	effects := []string{"*"}
	if c.Kind == "cell.set" {
		original, err := decodeWorkbook(input)
		if err != nil {
			return Candidate{}, err
		}
		if !reflect.DeepEqual(base, original) {
			return Candidate{}, fmt.Errorf("%w: reopening changed committed base", ErrCandidate)
		}
		comparison, err := decodeWorkbook(result.Result)
		if err != nil {
			return Candidate{}, err
		}
		effects, err = workbookEffects(base, comparison)
		if err != nil {
			return Candidate{}, err
		}
		target := c.Sheet + "!" + c.Cell
		for _, effect := range effects {
			if effect != target {
				return Candidate{}, ErrEffects
			}
		}
		// A semantic no-op still receives one durable operation receipt.
		if len(effects) == 0 {
			effects = []string{target}
		}
	}
	canonical, err := json.Marshal(candidate)
	if err != nil {
		return Candidate{}, err
	}
	return NewCandidate(b, canonical, effects), nil
}
func decodeWorkbook(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > MaxSnapshotBytes {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v map[string]any
	if err := d.Decode(&v); err != nil || v == nil {
		return nil, ErrInvalid
	}
	sheets, ok := v["sheets"].([]any)
	if !ok || len(sheets) == 0 || len(sheets) > 32 {
		return nil, ErrInvalid
	}
	names := map[string]bool{}
	for _, sheet := range sheets {
		obj, ok := sheet.(map[string]any)
		if !ok {
			return nil, ErrInvalid
		}
		name, ok := obj["name"].(string)
		if !ok || name == "" || len(name) > 128 || names[name] {
			return nil, ErrInvalid
		}
		names[name] = true
	}
	// Macro-bearing sources are outside this pilot, even though upstream preserves them.
	if v["vba_project"] != nil {
		return nil, ErrInvalid
	}
	return v, nil
}
func workbookEffects(before, after map[string]any) ([]string, error) {
	a, err := extractCells(before)
	if err != nil {
		return nil, err
	}
	b, err := extractCells(after)
	if err != nil {
		return nil, err
	}
	// extractCells removes cell stores so every remaining field, including sheet
	// order, styles, names, properties, charts and protection must stay identical.
	if !reflect.DeepEqual(before, after) {
		return nil, ErrEffects
	}
	changed := map[string]bool{}
	for k, v := range a {
		if !reflect.DeepEqual(v, b[k]) {
			changed[k] = true
		}
	}
	for k, v := range b {
		if !reflect.DeepEqual(v, a[k]) {
			changed[k] = true
		}
	}
	result := make([]string, 0, len(changed))
	for k := range changed {
		result = append(result, k)
	}
	sort.Strings(result)
	return result, nil
}
func extractCells(workbook map[string]any) (map[string]any, error) {
	result := map[string]any{}
	for _, rawSheet := range workbook["sheets"].([]any) {
		sheet := rawSheet.(map[string]any)
		name := sheet["name"].(string)
		cells, ok := sheet["cells"].(map[string]any)
		if !ok || len(cells) != 2 {
			return nil, ErrInvalid
		}
		bands, ok := cells["bands"].(map[string]any)
		if !ok {
			return nil, ErrInvalid
		}
		count := 0
		for bandKey, rawBand := range bands {
			band, err := strconv.Atoi(bandKey)
			if err != nil || band < 0 {
				return nil, ErrInvalid
			}
			obj, ok := rawBand.(map[string]any)
			if !ok || len(obj) != 1 {
				return nil, ErrInvalid
			}
			rows, ok := obj["rows"].(map[string]any)
			if !ok {
				return nil, ErrInvalid
			}
			for rowKey, rawRow := range rows {
				row, err := strconv.Atoi(rowKey)
				if err != nil || row < 0 || row >= 1048576 || row/64 != band {
					return nil, ErrInvalid
				}
				cols, ok := rawRow.(map[string]any)
				if !ok {
					return nil, ErrInvalid
				}
				for colKey, cell := range cols {
					col, err := strconv.Atoi(colKey)
					if err != nil || col < 0 || col >= 16384 {
						return nil, ErrInvalid
					}
					result[name+"!"+columnName(col)+strconv.Itoa(row+1)] = cell
					count++
					if count > 100000 {
						return nil, ErrInvalid
					}
				}
			}
		}
		length, ok := cells["len"].(json.Number)
		if !ok || length.String() != strconv.Itoa(count) {
			return nil, ErrInvalid
		}
		delete(sheet, "cells")
	}
	return result, nil
}
func columnName(col int) string {
	out := ""
	for col++; col > 0; col = (col - 1) / 26 {
		out = string(rune('A'+(col-1)%26)) + out
	}
	return out
}
