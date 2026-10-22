package appengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Base   json.RawMessage     `json:"base"`
	Result json.RawMessage     `json:"result"`
	Recalc *RecalcVerification `json:"recalc,omitempty"`
}

// RecalcVerification is the worker's independent full recalculation of the
// candidate: a fresh engine process recomputes every formula from scratch and
// compares it with the cached value the incremental edit produced.
type RecalcVerification struct {
	FormulaCells int    `json:"formula_cells"`
	Checked      int    `json:"checked"`
	Mismatched   int    `json:"mismatched"`
	NoOracle     int    `json:"no_oracle"`
	Volatile     int    `json:"volatile"`
	StaleCache   bool   `json:"stale_cache"`
	SHA256       string `json:"sha256"`
}

// Derived records formula values recalculated outside the mandated target. It
// is audit evidence, never a mandated effect.
type Derived struct {
	Count  int                 `json:"count"`
	Cells  []string            `json:"cells"`
	Recalc *RecalcVerification `json:"recalc,omitempty"`
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
	var derived *Derived
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
		target := c.Sheet + "!" + c.Cell
		derived, err = checkWorkbookEffects(base, comparison, target, result.Result, result.Recalc)
		if err != nil {
			return Candidate{}, err
		}
		// The mandate covers the authored target; verified recalculation is its
		// consequence, not a separate effect. A semantic no-op still receives one
		// durable operation receipt.
		effects = []string{target}
	}
	canonical, err := json.Marshal(candidate)
	if err != nil {
		return Candidate{}, err
	}
	// Re-encoding may escape characters; the stored form must still fit.
	if len(canonical) > MaxSnapshotBytes {
		return Candidate{}, ErrInvalid
	}
	prepared := NewCandidate(b, canonical, effects)
	prepared.Derived = derived
	return prepared, nil
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

// verified reports whether a full recalculation confirmed every formula value.
// Volatile or unverifiable formulas cannot be confirmed, so they fail closed.
func (v *RecalcVerification) verified() bool {
	return v != nil && v.FormulaCells > 0 && v.Checked == v.FormulaCells && v.Mismatched == 0 && v.NoOracle == 0 && v.Volatile == 0 && !v.StaleCache
}

// maxDerivedAudit bounds the recalculated addresses kept in an audit record;
// the count is always complete.
const maxDerivedAudit = 100

// checkWorkbookEffects separates authored data (input value, formula, style and
// every other cell or workbook field) from derived data (the computed value of a
// formula cell). Outside the target, authored data must stay identical and no
// cell may appear or disappear. A formula cell's value may change only when the
// cell holds the same formula before and after and both values are plain
// results. Such derived changes, and any formula in the target itself, need the
// worker's full recalculation report for exactly these result bytes.
func checkWorkbookEffects(before, after map[string]any, target string, raw []byte, recalc *RecalcVerification) (*Derived, error) {
	a, err := extractCells(before)
	if err != nil {
		return nil, err
	}
	b, err := extractCells(after)
	if err != nil {
		return nil, err
	}
	// extractCells removes cell stores so every remaining field, including sheet
	// order, styles, names, properties, charts, protection and calc settings
	// must stay identical.
	if !reflect.DeepEqual(before, after) {
		return nil, ErrEffects
	}
	var derived []string
	formulas := 0
	for k, v := range b {
		if isFormulaCell(v) {
			formulas++
		}
		if k == target || reflect.DeepEqual(v, a[k]) {
			continue
		}
		old, ok := a[k]
		if !ok || !recalculated(old, v) {
			return nil, ErrEffects
		}
		derived = append(derived, k)
	}
	for k := range a {
		if _, ok := b[k]; !ok && k != target {
			return nil, ErrEffects
		}
	}
	targetFormula := isFormulaCell(b[target])
	if len(derived) == 0 && !targetFormula {
		return nil, nil
	}
	// The report must describe exactly these bytes and the formulas the host
	// itself counted in them.
	sum := sha256.Sum256(raw)
	if recalc == nil || recalc.SHA256 != hex.EncodeToString(sum[:]) || recalc.FormulaCells != formulas || recalc.Mismatched != 0 {
		return nil, ErrEffects
	}
	if len(derived) == 0 {
		return nil, nil
	}
	// Under iterative calculation a cycle has many fixed points and the oracle
	// accepts any of them, so derived values cannot be confirmed. Settings the
	// host cannot read count as iterative.
	if calc, _ := after["calc"].(map[string]any); calc == nil || calc["iterative"] != false {
		return nil, ErrEffects
	}
	if !recalc.verified() {
		return nil, ErrEffects
	}
	sort.Strings(derived)
	record := &Derived{Count: len(derived), Cells: derived, Recalc: recalc}
	if len(derived) > maxDerivedAudit {
		record.Cells = derived[:maxDerivedAudit]
	}
	return record, nil
}

func isFormulaCell(cell any) bool {
	c, ok := cell.(map[string]any)
	if !ok {
		return false
	}
	_, ok = c["formula"].(map[string]any)
	return ok
}

// recalculated reports whether two cells differ only in the computed value of an
// unchanged formula.
func recalculated(before, after any) bool {
	x, ok := before.(map[string]any)
	if !ok {
		return false
	}
	y, ok := after.(map[string]any)
	if !ok || len(x) != len(y) {
		return false
	}
	if _, ok := x["formula"].(map[string]any); !ok {
		return false
	}
	for k, v := range x {
		w, ok := y[k]
		if !ok {
			return false
		}
		if k == "value" {
			if !plainValue(v) || !plainValue(w) {
				return false
			}
		} else if !reflect.DeepEqual(v, w) {
			return false
		}
	}
	return true
}

func plainValue(raw any) bool {
	v, ok := raw.(map[string]any)
	if !ok || len(v) != 2 {
		return false
	}
	switch v["t"] {
	case "Number":
		_, ok = v["v"].(json.Number)
	case "Text", "Error":
		_, ok = v["v"].(string)
	case "Bool":
		_, ok = v["v"].(bool)
	default:
		ok = false
	}
	return ok
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
			if err != nil || band < 0 || strconv.Itoa(band) != bandKey {
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
				if err != nil || strconv.Itoa(row) != rowKey || row < 0 || row >= 1048576 || row/64 != band {
					return nil, ErrInvalid
				}
				cols, ok := rawRow.(map[string]any)
				if !ok {
					return nil, ErrInvalid
				}
				for colKey, cell := range cols {
					col, err := strconv.Atoi(colKey)
					if err != nil || strconv.Itoa(col) != colKey || col < 0 || col >= 16384 {
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
