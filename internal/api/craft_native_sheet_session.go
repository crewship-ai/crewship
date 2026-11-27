package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// CraftNativeSheetSession is the narrow part of a persistent engine session
// the Sheet agent commands use: one document held open in an engine process,
// so a read costs milliseconds and a change re-calculates only what depends on
// it. It deliberately offers no file, script or free-form engine command.
type CraftNativeSheetSession interface {
	ReadRange(ctx context.Context, rng string, formulas bool) (CraftNativeSheetValues, error)
	GetCell(ctx context.Context, cell string) (CraftNativeSheetCell, error)
	SetCell(ctx context.Context, cell, input string) error
	WriteRange(ctx context.Context, rng string, values [][]any) (int, error)
	// Apply writes a batch of single cells and blocks in order with ONE
	// re-calculation at the end. It checks every operation before it touches the
	// document. An error part way returns how many were written: the session then
	// holds a partly changed document that was never saved and must be discarded.
	Apply(ctx context.Context, ops []CraftNativeSheetWrite) (int, error)
	Sheets(ctx context.Context) ([]CraftNativeSheetInfo, error)
	// ClearRange clears values and formulas of a range, keeping formats.
	ClearRange(ctx context.Context, rng string) error
	// ColumnsByHeader answers range.columnByHeader: every column under a header text.
	ColumnsByHeader(ctx context.Context, header, sheet string, matchCase bool) (json.RawMessage, error)
	// Snapshot serialises the whole document; it is the expensive call, taken
	// once per batch of changes.
	Snapshot(ctx context.Context) ([]byte, error)
}

// CraftNativeSheetEngine hands out sessions by document content.
type CraftNativeSheetEngine interface {
	// Open returns the session holding exactly base (opening one from base when
	// none is held). release gives it back; a session in use is never closed.
	Open(ctx context.Context, base []byte) (session CraftNativeSheetSession, release func(), err error)
	// Retain tells the engine the session now holds exactly snapshot, so the
	// next Open(snapshot) finds it instead of opening the document again.
	Retain(session CraftNativeSheetSession, snapshot []byte)
	// Discard closes a session that may hold changes that were never saved, so
	// no later Open of the same content finds it.
	Discard(session CraftNativeSheetSession)
}

// CraftNativeSheetWrite is one write of a batch: a cell (Cell, Input: typed as a
// person types it) or a block (Range, Values).
type CraftNativeSheetWrite struct {
	Cell, Input, Range string
	Values             [][]any
}

type CraftNativeSheetValues struct {
	Range      string   `json:"range"`
	Sheet      string   `json:"sheet"`
	Values     [][]any  `json:"values"`
	HeldValues []string `json:"heldValues,omitempty"`
}

type CraftNativeSheetCell struct {
	Cell    string `json:"cell"`
	Input   string `json:"input"`
	Formula string `json:"formula"`
	Value   any    `json:"value"`
}

type CraftNativeSheetInfo struct {
	Name string `json:"name"`
	Used string `json:"used,omitempty"`
}

// CraftNativeSheetAgentAdapter runs the Sheet agent commands over persistent
// engine sessions. The allowlist, size limits and formula rules are exactly
// the ones of the one-shot adapter: a command is validated before any session
// is opened. Apply and Inspect match CraftNativeAgentApply and
// CraftNativeAgentInspect.
type CraftNativeSheetAgentAdapter struct {
	engine CraftNativeSheetEngine
	rules  nativeAgentEngineRules
	check  func(base, snapshot []byte) error
	mu     sync.Mutex
	seen   map[string]struct{}
}

// WithCraftNativeSheetOpenCheck makes the adapter check, once per document
// version (content hash), that the engine kept everything it was given: after a
// session opens a version, check receives that version's bytes and the
// engine's own serialisation of it and may refuse. It replaces a separate full
// open of the document just to validate it. A document the engine itself
// serialised counts as already checked.
func WithCraftNativeSheetOpenCheck(check func(base, snapshot []byte) error) CraftNativeAgentOption {
	return func(a *CraftNativeAgentCLIAdapter) { a.sheetOpenCheck = check }
}

const craftNativeSheetSeenMax = 64

// checkOpened runs after a session holds raw. The session stays valid for the
// caller; a refusal tells the caller to give it back unused.
func (a *CraftNativeSheetAgentAdapter) checkOpened(ctx context.Context, session CraftNativeSheetSession, raw []byte) error {
	if a.check == nil {
		return nil
	}
	key := sheetKey(raw)
	a.mu.Lock()
	_, ok := a.seen[key]
	a.mu.Unlock()
	if ok {
		return nil
	}
	snapshot, err := session.Snapshot(ctx)
	if err != nil || a.check(raw, snapshot) != nil {
		return ErrCraftNativeAgentEngine
	}
	a.markSeen(key)
	return nil
}

func (a *CraftNativeSheetAgentAdapter) markSeen(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen == nil || len(a.seen) >= craftNativeSheetSeenMax {
		a.seen = map[string]struct{}{}
	}
	a.seen[key] = struct{}{}
}

func NewCraftNativeSheetAgentAdapter(engine CraftNativeSheetEngine, options ...CraftNativeAgentOption) *CraftNativeSheetAgentAdapter {
	probe := &CraftNativeAgentCLIAdapter{}
	for _, option := range options {
		option(probe)
	}
	return &CraftNativeSheetAgentAdapter{engine: engine, rules: probe.rules, check: probe.sheetOpenCheck}
}

func sheetKey(base []byte) string {
	sum := sha256.Sum256(base)
	return hex.EncodeToString(sum[:])
}

// ErrCraftNativeSheetBusy means every session slot of the engine is held by a
// running operation, so this Sheet could not be opened now. Nothing ran and
// nothing was saved: the same request may be sent again.
var ErrCraftNativeSheetBusy = errors.New("craft native sheet engine is busy")

// nativeAgentSheetAddress prefixes a range with its sheet, quoted. A sheet
// name that holds an apostrophe is not representable and is refused.
func nativeAgentSheetAddress(sheet string, params map[string]json.RawMessage, key string) (string, error) {
	var address string
	if err := json.Unmarshal(params[key], &address); err != nil {
		return "", ErrCraftNativeAgentEngine
	}
	if raw, ok := params["sheet"]; ok {
		if err := json.Unmarshal(raw, &sheet); err != nil || strings.Contains(sheet, "'") {
			return "", ErrCraftNativeAgentEngine
		}
	}
	if sheet == "" {
		return address, nil
	}
	return "'" + sheet + "'!" + address, nil
}

func (a *CraftNativeSheetAgentAdapter) Apply(ctx context.Context, p CraftNativeDocumentProfile, raw []byte, commands json.RawMessage) ([]byte, error) {
	if a == nil || a.engine == nil || p.Application != "gridcraft" {
		return nil, ErrCraftNativeAgentEngine
	}
	rules := a.rules
	if craftNativeUndo(ctx) {
		// A person's undo puts back the formulas an agent change replaced, in both checks: the
		// session's own input policy is the second one and would refuse them too.
		rules.formulas, rules.restore = true, true
		ctx = craftsession.WithInputPolicy(ctx, craftsession.InputPolicy(craftNativeAgentInputPolicy(rules)))
	}
	parsed, err := nativeAgentEngineCommandsFor(p.Application, commands, false, rules)
	if err != nil {
		return nil, err
	}
	// Translate every command first: a request that cannot be expressed is
	// refused before any session is opened.
	type step = sheetApplyStep
	steps := make([]step, 0, len(parsed))
	for _, command := range parsed {
		var params map[string]json.RawMessage
		if json.Unmarshal(command.Params, &params) != nil {
			return nil, ErrCraftNativeAgentEngine
		}
		switch command.Command {
		case "cell.set":
			address, err := nativeAgentSheetAddress("", params, "cell")
			var input string
			if err != nil || json.Unmarshal(params["input"], &input) != nil {
				return nil, ErrCraftNativeAgentEngine
			}
			steps = append(steps, step{cell: address, input: input})
		case "range.clear":
			address, err := nativeAgentSheetAddress("", params, "range")
			if err != nil {
				return nil, ErrCraftNativeAgentEngine
			}
			steps = append(steps, step{clear: address})
		case "range.setValues":
			address, err := nativeAgentSheetAddress("", params, "range")
			var values [][]any
			if err != nil || json.Unmarshal(params["values"], &values) != nil {
				return nil, ErrCraftNativeAgentEngine
			}
			steps = append(steps, step{rng: address, values: values})
		case "formulas.calculateNow": // every write already recalculates
		default:
			return nil, ErrCraftNativeAgentEngine
		}
	}
	// In a workbook file every write names its sheet: the file's active sheet on the server is not
	// the sheet the person sees.
	workbook := p.Workbook || craftNativeSheetWorkbook(raw)
	var edits craftWorkbookEdits
	if workbook {
		if _, err := mooring.DetectWorkbook(raw); err != nil {
			return nil, &craftsession.FidelityLossError{Parts: []craftsession.FidelityPart{{Code: "package", Count: 1}}}
		}
		if signed := craftWorkbookEditSignatures(raw); len(signed) != 0 {
			return nil, &craftsession.FidelityLossError{Parts: signed}
		}
		for _, st := range steps {
			if !edits.add(st.cell + st.rng + st.clear) {
				return nil, errCraftNativeSheetNameRequired
			}
		}
	}
	session, release, err := a.engine.Open(ctx, raw)
	if err != nil {
		if errors.Is(err, ErrCraftNativeSheetBusy) {
			return nil, ErrCraftNativeSheetBusy
		}
		if workbook {
			if lost := craftWorkbookPartsLost(ctx, raw, raw, edits); lost != nil {
				return nil, &craftsession.FidelityLossError{Parts: lost}
			}
		}
		return nil, ErrCraftNativeAgentEngine
	}
	defer release()
	// A workbook file is checked by its save (the engine's fidelity check), not by comparing the
	// native JSON the engine would write.
	if !workbook {
		if err = a.checkOpened(ctx, session, raw); err != nil {
			return nil, err
		}
	}
	// The engine re-calculates once per write. The writes between two clears are joined into the
	// fewest rectangles, so a batch costs one re-calculation per rectangle instead of one per
	// command; a clear runs where it stands, so the order of the request is kept.
	for len(steps) > 0 {
		if steps[0].clear != "" {
			if err = session.ClearRange(ctx, steps[0].clear); err != nil {
				a.engine.Discard(session)
				return nil, ErrCraftNativeAgentEngine
			}
			steps = steps[1:]
			continue
		}
		n := 0
		for n < len(steps) && steps[n].clear == "" {
			n++
		}
		if err = a.applyWrites(ctx, session, steps[:n]); err != nil {
			a.engine.Discard(session)
			return nil, ErrCraftNativeAgentEngine
		}
		steps = steps[n:]
	}
	snapshot, err := session.Snapshot(ctx)
	var loss *craftsession.FidelityLossError
	if errors.As(err, &loss) {
		for _, part := range loss.Parts {
			name := part.Code
			if part.Code == "calc_chain" {
				name = craftCalcChainPart
			}
			slog.WarnContext(ctx, "craft workbook writer refused", "part", name, "code", part.Code, "count", part.Count)
		}
		// Nothing was saved; the session still holds the change, so it is not reused.
		a.engine.Discard(session)
		return nil, loss
	}
	limit := 32 << 20
	if workbook {
		limit = mooring.MaxWorkbookBytes
	}
	if err != nil || len(snapshot) == 0 || len(snapshot) > limit {
		return nil, ErrCraftNativeAgentEngine
	}
	// Verify the export independently, using the resolved edit ranges.
	if workbook {
		if lost := craftWorkbookPartsLost(ctx, raw, snapshot, edits); lost != nil {
			a.engine.Discard(session)
			return nil, &craftsession.FidelityLossError{Parts: lost}
		}
	}
	a.engine.Retain(session, snapshot)
	a.markSeen(sheetKey(snapshot))
	return snapshot, nil
}

func (a *CraftNativeSheetAgentAdapter) Inspect(ctx context.Context, p CraftNativeDocumentProfile, raw []byte, query json.RawMessage) (json.RawMessage, error) {
	if a == nil || a.engine == nil || p.Application != "gridcraft" {
		return nil, ErrCraftNativeAgentEngine
	}
	parsed, err := nativeAgentEngineCommandsFor(p.Application, query, true, a.rules)
	if err != nil || len(parsed) != 1 {
		return nil, ErrCraftNativeAgentEngine
	}
	command := parsed[0]
	var params map[string]json.RawMessage
	if json.Unmarshal(command.Params, &params) != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	key := "cell"
	if command.Command == "sheet.read" {
		key = "range"
	}
	address := ""
	if command.Command != "document.inspect" && command.Command != "range.columnByHeader" {
		var err error
		if address, err = nativeAgentSheetAddress("", params, key); err != nil {
			return nil, ErrCraftNativeAgentEngine
		}
	}
	session, release, err := a.engine.Open(ctx, raw)
	if err != nil {
		if errors.Is(err, ErrCraftNativeSheetBusy) {
			return nil, ErrCraftNativeSheetBusy
		}
		return nil, ErrCraftNativeAgentEngine
	}
	defer release()
	// A workbook file is read as it is; the native JSON open check does not apply.
	if !craftNativeSheetWorkbook(raw) {
		if err = a.checkOpened(ctx, session, raw); err != nil {
			return nil, err
		}
	}
	var result any
	switch command.Command {
	case "range.columnByHeader":
		var header, sheet string
		var matchCase bool
		_ = json.Unmarshal(params["header"], &header)
		if raw, ok := params["sheet"]; ok {
			_ = json.Unmarshal(raw, &sheet)
		}
		if raw, ok := params["matchCase"]; ok {
			_ = json.Unmarshal(raw, &matchCase)
		}
		found, err := session.ColumnsByHeader(ctx, header, sheet, matchCase)
		if err != nil {
			return nil, ErrCraftNativeAgentEngine
		}
		result = found
	case "cell.get":
		result, err = session.GetCell(ctx, address)
		if err != nil {
			return nil, ErrCraftNativeAgentEngine
		}
	case "sheet.read":
		var formulas bool
		if raw, ok := params["formulas"]; ok {
			_ = json.Unmarshal(raw, &formulas)
		}
		read, err := session.ReadRange(ctx, address, formulas)
		if err != nil {
			return nil, ErrCraftNativeAgentEngine
		}
		if formulas && craftNativeUndo(ctx) {
			area, _ := nativeAgentEngineRange(params["range"])
			if err := craftUndoEncodeText(ctx, session, area, read); err != nil {
				return nil, err
			}
		}
		result = read
	case "document.inspect":
		sheets, err := session.Sheets(ctx)
		if err != nil {
			return nil, ErrCraftNativeAgentEngine
		}
		result = map[string]any{"sheets": sheets}
	default:
		return nil, ErrCraftNativeAgentEngine
	}
	answer, err := json.Marshal(map[string]any{"command": command.Command, "result": result})
	if err != nil || len(answer) > 1<<20 {
		return nil, errors.Join(ErrCraftNativeAgentEngine)
	}
	return answer, nil
}

// craftUndoEncodeText marks literal strings as forced text in GridCraft input syntax.
// Both the server and the editor replay this syntax. A string that starts with "="
// can be either text or a formula in a formulas-mode range read, so ask the cell.
func craftUndoEncodeText(ctx context.Context, session CraftNativeSheetSession, area nativeAgentEngineArea, read CraftNativeSheetValues) error {
	for r, row := range read.Values {
		for c, value := range row {
			text, ok := value.(string)
			if !ok || text == "" {
				continue
			}
			if strings.HasPrefix(text, "=") {
				addr := sheetCellAddress(read.Sheet, area.row1+r, area.col1+c)
				cell, err := session.GetCell(ctx, addr)
				if err != nil {
					return ErrCraftNativeAgentEngine
				}
				if cell.Formula != "" {
					continue
				}
			}
			row[c] = "'" + text
		}
	}
	return nil
}

// CraftNativeAgentInputPolicy is the rule a persistent engine session applies
// to every string it is asked to write: a literal passes, a formula passes only
// with formulas enabled and only when it calls allowlisted functions and
// reaches nothing outside the workbook. It is the second line behind the
// command validation, for callers that reach the session another way.
func CraftNativeAgentInputPolicy(formulas bool) func(string) error {
	return craftNativeAgentInputPolicy(nativeAgentEngineRules{formulas: formulas})
}

func craftNativeAgentInputPolicy(rules nativeAgentEngineRules) func(string) error {
	return func(input string) error {
		if len(input) > 32<<10 || !rules.text(input) {
			return ErrCraftNativeAgentEngine
		}
		return nil
	}
}

// sheetWrite is one validated command's write: the address of its top-left
// cell (optionally sheet-qualified and, for a range, with a second corner) and
// the values from there.
type sheetWrite struct {
	address string
	values  [][]any
}

// sheetRect is a rectangle of values anchored at address.
type sheetRect struct {
	address string
	values  [][]any
}

// single reports a one-cell rectangle that holds text, which is written the
// way a person types it.
func (r sheetRect) single() (string, bool) {
	if len(r.values) == 1 && len(r.values[0]) == 1 {
		text, ok := r.values[0][0].(string)
		return text, ok
	}
	return "", false
}

var sheetWriteAddress = regexp.MustCompile(`^(?:'((?:[^']|'')+)'!)?\$?([A-Z]{1,3})\$?([0-9]{1,7})(?::.*)?$`)

// sheetWriteMergesText says whether text may be joined into rectangles. A
// block write and a single-cell write may enter the same text differently
// ("007", "1/2", "50%", dates): until the two paths are shown to give the same
// type, value and formula on the real engine, text keeps the single-cell path
// and only numbers, booleans and empty cells are joined.
const sheetWriteMergesText = false

// One engine batch (craftsession.MaxBatchOps, MaxBatchCells).
const (
	sheetApplyMaxOps   = 1024
	sheetApplyMaxCells = 50_000
)

// sheetWriteMaxCells is what one engine write takes (craftsession.MaxWriteCells).
const sheetWriteMaxCells = 10_000

// coalesceSheetWrites joins writes into disjoint rectangles that write exactly
// the same cells with the same values: a later write to a cell wins, a cell
// that is not written is never touched, and no rectangle holds a gap. The
// result is ordered by sheet (first use), row and column. It reports false for
// an address it cannot read.
func coalesceSheetWrites(writes []sheetWrite, mergeText bool) ([]sheetRect, bool) {
	type key struct{ row, col int }
	type sheetCells struct {
		name  string
		cells map[key]any
	}
	var order []*sheetCells
	bySheet := map[string]*sheetCells{}
	var texts []sheetWrite // cells that keep the single-cell path, in order
	for _, w := range writes {
		m := sheetWriteAddress.FindStringSubmatch(w.address)
		if m == nil {
			return nil, false
		}
		row0, _ := strconv.Atoi(m[3])
		col0 := 0
		for _, r := range m[2] {
			col0 = col0*26 + int(r-'A') + 1
		}
		sc := bySheet[m[1]]
		if sc == nil {
			sc = &sheetCells{name: m[1], cells: map[key]any{}}
			bySheet[m[1]] = sc
			order = append(order, sc)
		}
		for i, row := range w.values {
			for j, v := range row {
				if _, isText := v.(string); isText && !mergeText {
					// A later write to the cell replaces an earlier one on either path.
					delete(sc.cells, key{row0 + i, col0 + j})
					texts = append(texts, sheetWrite{address: sheetCellAddress(m[1], row0+i, col0+j), values: [][]any{{v}}})
					continue
				}
				sc.cells[key{row0 + i, col0 + j}] = v
				texts = dropTextAt(texts, sheetCellAddress(m[1], row0+i, col0+j))
			}
		}
	}
	columnLetters := func(n int) string {
		name := ""
		for n > 0 {
			n--
			name = string(rune('A'+n%26)) + name
			n /= 26
		}
		return name
	}
	var out []sheetRect
	for _, w := range texts {
		out = append(out, sheetRect{address: w.address, values: w.values})
	}
	for _, sc := range order {
		keys := make([]key, 0, len(sc.cells))
		for k := range sc.cells {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].row != keys[j].row {
				return keys[i].row < keys[j].row
			}
			return keys[i].col < keys[j].col
		})
		// Runs of consecutive columns on one row.
		type run struct{ row, first, last int }
		var runs []run
		for _, k := range keys {
			if n := len(runs); n > 0 && runs[n-1].row == k.row && runs[n-1].last == k.col-1 {
				runs[n-1].last = k.col
				continue
			}
			runs = append(runs, run{k.row, k.col, k.col})
		}
		// Runs of the same width on consecutive rows are one rectangle.
		used := make([]bool, len(runs))
		for i := range runs {
			if used[i] {
				continue
			}
			rows := []int{runs[i].row}
			used[i] = true
			for j := i + 1; j < len(runs); j++ {
				if used[j] || runs[j].first != runs[i].first || runs[j].last != runs[i].last || runs[j].row != rows[len(rows)-1]+1 {
					continue
				}
				rows = append(rows, runs[j].row)
				used[j] = true
			}
			width := runs[i].last - runs[i].first + 1
			chunk := max(1, sheetWriteMaxCells/width)
			for start := 0; start < len(rows); start += chunk {
				end := min(len(rows), start+chunk)
				values := make([][]any, 0, end-start)
				for _, r := range rows[start:end] {
					line := make([]any, 0, width)
					for c := runs[i].first; c <= runs[i].last; c++ {
						line = append(line, sc.cells[key{r, c}])
					}
					values = append(values, line)
				}
				address := fmt.Sprintf("%s%d", columnLetters(runs[i].first), rows[start])
				if sc.name != "" {
					address = "'" + sc.name + "'!" + address
				}
				out = append(out, sheetRect{address: address, values: values})
			}
		}
	}
	return out, true
}

// sheetCellAddress renders one cell, sheet-qualified when the sheet is named.
func sheetCellAddress(sheet string, row, col int) string {
	name := ""
	for n := col; n > 0; {
		n--
		name = string(rune('A'+n%26)) + name
		n /= 26
	}
	address := fmt.Sprintf("%s%d", name, row)
	if sheet != "" {
		return "'" + sheet + "'!" + address
	}
	return address
}

// dropTextAt removes an earlier text write to the same cell: the later write wins.
func dropTextAt(texts []sheetWrite, address string) []sheetWrite {
	out := texts[:0]
	for _, w := range texts {
		if w.address != address {
			out = append(out, w)
		}
	}
	return out
}

type sheetApplyStep = struct {
	cell, input, rng, clear string
	values                  [][]any
}

// applyWrites writes steps (cells and blocks, no clears) in the fewest engine batches.
func (a *CraftNativeSheetAgentAdapter) applyWrites(ctx context.Context, session CraftNativeSheetSession, steps []sheetApplyStep) error {
	writes := make([]sheetWrite, 0, len(steps))
	for _, st := range steps {
		if st.cell != "" {
			writes = append(writes, sheetWrite{address: st.cell, values: [][]any{{st.input}}})
		} else {
			writes = append(writes, sheetWrite{address: st.rng, values: st.values})
		}
	}
	rects, ok := coalesceSheetWrites(writes, sheetWriteMergesText)
	if !ok {
		return ErrCraftNativeAgentEngine
	}
	// One engine batch is at most 1024 operations and 50 000 cells, with one
	// re-calculation each. A failure part way leaves unsaved changes in the
	// session: it is discarded, never returned to the pool.
	var batch []CraftNativeSheetWrite
	batchCells := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, werr := session.Apply(ctx, batch)
		batch, batchCells = batch[:0], 0
		return werr
	}
	for _, rect := range rects {
		op, cells := CraftNativeSheetWrite{Range: rect.address, Values: rect.values}, 0
		for _, row := range rect.values {
			cells += len(row)
		}
		if text, single := rect.single(); single {
			op = CraftNativeSheetWrite{Cell: rect.address, Input: text}
		}
		if len(batch) >= sheetApplyMaxOps || batchCells+cells > sheetApplyMaxCells {
			if err := flush(); err != nil {
				return err
			}
		}
		batch, batchCells = append(batch, op), batchCells+cells
	}
	return flush()
}

// craftNativeSheetWorkbook: raw is a workbook file (a zip), not the native JSON document.
func craftNativeSheetWorkbook(raw []byte) bool { return bytes.HasPrefix(raw, []byte("PK")) }

// errCraftNativeSheetNameRequired: a write to a workbook Sheet that does not name its sheet.
var errCraftNativeSheetNameRequired = errors.New(`name the sheet in every write: params.sheet (e.g. "L" for 'L'!B8)`)
