// Package craftsession keeps a Sheet document open in one engine process, so
// reading a range and changing a cell cost milliseconds and a re-calculation
// of what depends on the change, instead of opening the whole document again
// for every command (#5354).
//
// A Session wraps one worker (craftengine.Worker) running the engine's MCP
// server over stdio. Nothing outside this package can reach the engine's other
// tools: the methods below are the whole allowlist (read_range, write_range,
// set_cell, get_cell and a snapshot). Open/save-workbook, scripts and any
// command that names a file are not reachable. What the engine answers is
// untrusted input: every answer is parsed strictly and bounded.
package craftsession

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Limits on what one call may carry. They are the engine-independent floor;
// a caller adds its own, stricter policy (for example which functions a
// formula may use).
const (
	MaxRangeCells    = 100_000
	MaxWriteCells    = 10_000
	MaxInputBytes    = 32 << 10
	MaxSnapshotBytes = 256 << 20
	maxToolAnswer    = 256 << 20
)

var (
	// ErrInvalid means the request was refused before it reached the engine.
	ErrInvalid = errors.New("craftsession: invalid request")
	// ErrLost means the engine process is gone (crash, kill, deadline). The
	// session is unusable; open it again from the last saved version.
	ErrLost = errors.New("craftsession: engine process lost")
	// ErrDiscarded means a batch failed part way: the session holds a partly changed, never saved
	// workbook and answers nothing more (Close it and open the last saved version).
	ErrDiscarded = errors.New("craftsession: session discarded after a failed batch")
	// ErrPolicy means the input policy refused a cell's content (a formula
	// while formulas are off, or a function outside the allowed list).
	ErrPolicy = errors.New("craftsession: the input policy refused this content")
	// ErrBusy is returned when a snapshot slot did not free up in time.
	ErrSnapshotBusy = errors.New("craftsession: another snapshot is running")
	// ErrRefused is the engine saying no to a valid-looking request.
	ErrRefused = errors.New("craftsession: engine refused the request")
)

// Session is one open document.
type Session struct {
	w    *craftengine.Worker
	mu   sync.Mutex // one call at a time; the engine is single-threaded
	last time.Time
	done sync.Once
	// onClose runs once when the session ends (the manager forgets it).
	onClose func()
	policy  InputPolicy
	broken  atomic.Bool // a batch failed part way (read without mu: Close paths hold it)
	keyMu   sync.RWMutex
	key     string
	snap    chan struct{} // snapshot slots shared by the manager's sessions
	// computed records a snapshot taken in automatic calculation (Manager.Computed).
	computed func([]byte)
	// format is "xlsx" or "xlsm" when the engine opened the Sheet's workbook file.
	format string
}

// InputPolicy decides whether the content of one cell may be entered. It
// sees what the caller typed ("=SUM(A1:A3)", "12", "text").
type InputPolicy func(input string) error

// DefaultInputPolicy allows plain values only: numbers and text. Anything the
// engine could read as a formula (a leading "=", "+", "-" or "@" that is not
// a plain number) is refused until a caller installs a policy that lists the
// functions it allows.
func DefaultInputPolicy(input string) error {
	t := strings.TrimSpace(input)
	if t == "" {
		return nil
	}
	switch t[0] {
	case '=', '@':
		return ErrPolicy
	case '+', '-':
		if _, err := strconv.ParseFloat(t, 64); err != nil {
			return ErrPolicy
		}
	}
	return nil
}

type inputPolicyKey struct{}

// WithInputPolicy makes the writes made with ctx follow p instead of the session's policy. A
// person's undo uses it to put back the formulas the document held before an agent change.
func WithInputPolicy(ctx context.Context, p InputPolicy) context.Context {
	return context.WithValue(ctx, inputPolicyKey{}, p)
}

// PolicyFrom is the policy WithInputPolicy put in ctx, or fallback when there is none.
func PolicyFrom(ctx context.Context, fallback InputPolicy) InputPolicy {
	if p, ok := ctx.Value(inputPolicyKey{}).(InputPolicy); ok && p != nil {
		return p
	}
	return fallback
}

// newSession takes an already started worker.
func newSession(w *craftengine.Worker, onClose func(), policy InputPolicy, snap chan struct{}) *Session {
	if policy == nil {
		policy = DefaultInputPolicy
	}
	return &Session{w: w, last: time.Now(), onClose: onClose, policy: policy, snap: snap}
}

var (
	// "Sheet1!A1:C3", "A1", "'My sheet'!B2:B9": the engine parses it again,
	// this only keeps control characters and huge strings out.
	// A quoted sheet name may hold a doubled apostrophe ('O''Brien'!A1).
	rangeRe = regexp.MustCompile(`^(?:(?:'(?:[^'\x00-\x1f]|'')+'|[^'!:\x00-\x1f\s]{1,64})!)?\$?[A-Z]{1,3}\$?[0-9]{1,7}(?::\$?[A-Z]{1,3}\$?[0-9]{1,7})?$`)
	cellRe  = regexp.MustCompile(`^(?:(?:'(?:[^'\x00-\x1f]|'')+'|[^'!:\x00-\x1f\s]{1,64})!)?[A-Z]{1,3}[1-9][0-9]{0,6}$`)
)

// Open starts the engine on the document; the first call waits for it.
// (Package-internal: Manager is the public way in.)

func (s *Session) call(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callLocked(ctx, tool, args)
}

// callLocked is call for a caller that already holds s.mu (a batch).
func (s *Session) callLocked(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	if s.broken.Load() {
		return nil, ErrDiscarded
	}
	s.last = time.Now()
	res, err := s.w.Call(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		var rpc *craftengine.RPCError
		if errors.As(err, &rpc) {
			return nil, fmt.Errorf("%w: %s", ErrRefused, rpc.Message)
		}
		s.Close()
		return nil, ErrLost
	}
	if len(res) > maxToolAnswer {
		s.Close()
		return nil, ErrLost
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(res, &out) != nil || len(out.Content) != 1 || out.Content[0].Type != "text" {
		s.Close()
		return nil, ErrLost
	}
	if out.IsError {
		return nil, fmt.Errorf("%w: %s", ErrRefused, clip(out.Content[0].Text, 200))
	}
	return json.RawMessage(out.Content[0].Text), nil
}

// Values is the answer to ReadRange: rows of numbers, strings, booleans or
// null, exactly as the engine computed them.
type Values struct {
	Range  string  `json:"range"`
	Sheet  string  `json:"sheet"`
	Values [][]any `json:"values"`
	// HeldValues lists the cells whose value is Excel's, kept and not recalculated (HeldValue).
	HeldValues []string `json:"heldValues,omitempty"`
}

// ReadRange returns the computed values of a range.
func (s *Session) ReadRange(ctx context.Context, rng string) (*Values, error) {
	if !rangeRe.MatchString(rng) || rangeCells(rng) > MaxRangeCells {
		return nil, ErrInvalid
	}
	raw, err := s.call(ctx, "read_range", map[string]any{"range": rng})
	if err != nil {
		return nil, err
	}
	var v Values
	dec := json.NewDecoder(bytes.NewReader(raw))
	if dec.Decode(&v) != nil {
		return nil, ErrRefused
	}
	cells := 0
	for _, row := range v.Values {
		cells += len(row)
	}
	if cells > MaxRangeCells {
		return nil, ErrRefused
	}
	return &v, nil
}

// ReadOptions selects what ReadRangeWith returns.
type ReadOptions struct {
	// Formulas returns the formula text ("=SUM(A1:A3)") of a formula cell
	// instead of its value.
	Formulas bool
	// Formatted returns the text as displayed (number formats applied).
	Formatted bool
}

// ReadRangeWith reads a range through the engine's sheet.read command.
func (s *Session) ReadRangeWith(ctx context.Context, rng string, o ReadOptions) (*Values, error) {
	if !rangeRe.MatchString(rng) || rangeCells(rng) > MaxRangeCells {
		return nil, ErrInvalid
	}
	raw, err := s.call(ctx, "execute_command", map[string]any{"command": "sheet.read", "params": map[string]any{"range": rng, "formulas": o.Formulas, "formatted": o.Formatted}})
	if err != nil {
		return nil, err
	}
	var v Values
	if json.Unmarshal(raw, &v) != nil {
		return nil, ErrRefused
	}
	cells := 0
	for _, row := range v.Values {
		cells += len(row)
	}
	if cells > MaxRangeCells {
		return nil, ErrRefused
	}
	return &v, nil
}

// SheetInfo describes one sheet of the document.
type SheetInfo struct {
	Name string `json:"name"`
	// Used is the used range as the engine reports it ("A1:L14001"), or "".
	Used string          `json:"used"`
	Raw  json.RawMessage `json:"-"`
}

// Sheets lists the sheets (document.inspect).
func (s *Session) Sheets(ctx context.Context) ([]SheetInfo, error) {
	raw, err := s.call(ctx, "execute_command", map[string]any{"command": "document.inspect", "params": map[string]any{}})
	if err != nil {
		return nil, err
	}
	var doc struct {
		Sheets []map[string]json.RawMessage `json:"sheets"`
	}
	if json.Unmarshal(raw, &doc) != nil || len(doc.Sheets) > 1024 {
		return nil, ErrRefused
	}
	out := make([]SheetInfo, 0, len(doc.Sheets))
	for _, sh := range doc.Sheets {
		var info SheetInfo
		_ = json.Unmarshal(sh["name"], &info.Name)
		for _, k := range []string{"used_range", "usedRange", "used", "range"} {
			if v, ok := sh[k]; ok && json.Unmarshal(v, &info.Used) == nil && info.Used != "" {
				break
			}
		}
		if info.Name == "" || len(info.Name) > 255 || !utf8.ValidString(info.Name) {
			return nil, ErrRefused
		}
		b, _ := json.Marshal(sh)
		info.Raw = b
		out = append(out, info)
	}
	return out, nil
}

// CellRef names one cell.
type CellRef struct {
	Sheet string `json:"sheet"`
	Cell  string `json:"cell"`
}

// MaxDependents caps what Dependents returns.
const MaxDependents = 10_000

// Dependents lists the cells whose value depends on cell (the engine's
// dependency trace), across sheets. A longer list is cut at MaxDependents and
// reported truncated.
func (s *Session) Dependents(ctx context.Context, cell string) (refs []CellRef, truncated bool, err error) {
	if !cellRe.MatchString(cell) {
		return nil, false, ErrInvalid
	}
	raw, err := s.call(ctx, "execute_command", map[string]any{"command": "formulas.traceDependents", "params": map[string]any{"cell": cell}})
	if err != nil {
		return nil, false, err
	}
	if json.Unmarshal(raw, &refs) != nil {
		return nil, false, ErrRefused
	}
	if len(refs) > MaxDependents {
		refs, truncated = refs[:MaxDependents], true
	}
	for _, r := range refs {
		if r.Cell == "" || len(r.Cell) > 16 || len(r.Sheet) > 255 || !utf8.ValidString(r.Sheet) {
			return nil, false, ErrRefused
		}
	}
	return refs, truncated, nil
}

// Cell is what the engine holds in one cell.
type Cell struct {
	Cell    string `json:"cell"`
	Input   string `json:"input"`
	Formula string `json:"formula"`
	Value   any    `json:"value"`
	// HeldValue is set when Value is Excel's, kept and not recalculated by the engine.
	HeldValue *HeldValue `json:"heldValue,omitempty"`
}

// GetCell returns the input (the typed text or formula) and the value of one cell.
func (s *Session) GetCell(ctx context.Context, cell string) (*Cell, error) {
	if !cellRe.MatchString(cell) {
		return nil, ErrInvalid
	}
	raw, err := s.call(ctx, "get_cell", map[string]any{"cell": cell})
	if err != nil {
		return nil, err
	}
	var c Cell
	if json.Unmarshal(raw, &c) != nil {
		return nil, ErrRefused
	}
	return &c, nil
}

func validInput(in string) bool {
	return len(in) <= MaxInputBytes && utf8.ValidString(in) && !strings.ContainsRune(in, 0)
}

// SetCell enters text or a formula in one cell and re-calculates what depends on it.
func (s *Session) SetCell(ctx context.Context, cell, input string) error {
	if !cellRe.MatchString(cell) || !validInput(input) {
		return ErrInvalid
	}
	if err := PolicyFrom(ctx, s.policy)(input); err != nil {
		return ErrPolicy
	}
	_, err := s.call(ctx, "set_cell", map[string]any{"cell": cell, "input": input})
	return err
}

// ClearRange clears the values and formulas of rng (edit.clearContents), keeping formats; what
// depends on it is recalculated.
func (s *Session) ClearRange(ctx context.Context, rng string) error {
	if !rangeRe.MatchString(rng) {
		return ErrInvalid
	}
	_, err := s.call(ctx, "execute_command", map[string]any{"command": "edit.clearContents", "params": map[string]any{"range": rng}})
	return err
}

// ColumnsByHeader runs range.columnByHeader: every column under a cell whose text is header,
// {columns:[{sheet, headerCell, range}]}.
func (s *Session) ColumnsByHeader(ctx context.Context, header, sheet string, matchCase bool) (json.RawMessage, error) {
	if header == "" || len(header) > 256 || !validInput(header) {
		return nil, ErrInvalid
	}
	params := map[string]any{"header": header, "matchCase": matchCase}
	if sheet != "" {
		params["sheet"] = sheet
	}
	raw, err := s.call(ctx, "execute_command", map[string]any{"command": "range.columnByHeader", "params": params})
	if err != nil {
		return nil, err
	}
	var answer struct {
		Columns json.RawMessage `json:"columns"`
	}
	if json.Unmarshal(raw, &answer) != nil || len(answer.Columns) == 0 || answer.Columns[0] != '[' {
		return nil, ErrInvalid
	}
	return raw, nil
}

// WriteRange writes a block of values or formulas (strings starting with "=")
// with one re-calculation for the whole block. It returns the number of cells
// the engine wrote.
func (s *Session) WriteRange(ctx context.Context, rng string, values [][]any) (int, error) {
	if !rangeRe.MatchString(rng) || len(values) == 0 {
		return 0, ErrInvalid
	}
	cells := 0
	for _, row := range values {
		cells += len(row)
		for _, v := range row {
			switch x := v.(type) {
			case nil, bool, float64, int, int64, json.Number:
			case string:
				if !validInput(x) {
					return 0, ErrInvalid
				}
				if PolicyFrom(ctx, s.policy)(x) != nil {
					return 0, ErrPolicy
				}
			default:
				return 0, ErrInvalid
			}
		}
	}
	if cells == 0 || cells > MaxWriteCells {
		return 0, ErrInvalid
	}
	raw, err := s.call(ctx, "write_range", map[string]any{"range": rng, "values": values})
	if err != nil {
		return 0, err
	}
	var out struct {
		Cells int `json:"cells"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return 0, ErrRefused
	}
	return out.Cells, nil
}

// WriteOp is one write of a batch: a single cell (Cell, Input) or a block (Range, Values).
type WriteOp struct {
	Cell   string
	Input  string
	Range  string
	Values [][]any
}

// MaxBatchOps and MaxBatchCells bound one Apply.
const (
	MaxBatchOps   = 1024
	MaxBatchCells = 50_000
)

// Apply writes many cells, scattered or in blocks, with ONE recalculation of the workbook.
//
// The engine recalculates after every write (about 1.3 s each on a 194 000 cell workbook,
// so 64 cells took 80 s). Apply switches the workbook to manual calculation for the
// duration of the batch (writes then evaluate only the cells they set), puts automatic
// calculation back at the end (which recalculates the workbook once) and restores it also
// when a write fails. A workbook that was in manual calculation stays in it and is
// recalculated once at the end, so the agent reads correct values.
//
// Every operation is checked (shape, size, input policy) BEFORE the engine is touched. If the
// engine refuses a write part way (or the final recalculation fails), the number applied so far
// and the error are returned and the session is DISCARDED: it holds a partly changed workbook
// that was never saved, so every further call, a snapshot included, answers ErrDiscarded, Alive
// reports false and the caller closes it and opens the last saved version.
func (s *Session) Apply(ctx context.Context, ops []WriteOp) (applied int, err error) {
	if len(ops) == 0 || len(ops) > MaxBatchOps {
		return 0, ErrInvalid
	}
	cells := 0
	for _, op := range ops {
		switch {
		case op.Cell != "" && op.Range == "":
			if !cellRe.MatchString(op.Cell) || !validInput(op.Input) {
				return 0, ErrInvalid
			}
			if PolicyFrom(ctx, s.policy)(op.Input) != nil {
				return 0, ErrPolicy
			}
			cells++
		case op.Range != "" && op.Cell == "":
			n, e := checkBlock(op.Range, op.Values, PolicyFrom(ctx, s.policy))
			if e != nil {
				return 0, e
			}
			cells += n
		default:
			return 0, ErrInvalid
		}
		if cells > MaxBatchCells {
			return 0, ErrInvalid
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mode, err := s.calcModeLocked(ctx)
	if err != nil {
		return 0, err
	}
	// One write (a cell or a block) in an automatic workbook goes straight to the engine, which
	// then recalculates only what depends on it (calc.cells_changed); the switch through manual
	// below would recalculate the whole workbook (client workbook: 28.6 s, against 0.01 s).
	// Measured equal to a full recalculation on every cell (TestIncrementalRecalc).
	if mode == "automatic" && len(ops) == 1 {
		op := ops[0]
		if op.Cell != "" {
			_, err = s.callLocked(ctx, "set_cell", map[string]any{"cell": op.Cell, "input": op.Input})
		} else {
			_, err = s.callLocked(ctx, "write_range", map[string]any{"range": op.Range, "values": op.Values})
		}
		if err != nil {
			s.broken.Store(true)
			return 0, err
		}
		return 1, nil
	}
	// Every mode but manual recalculates after each write: put the workbook in manual for the
	// batch and back to EXACTLY the mode it had.
	deferred := mode != "manual"
	if deferred {
		if _, err = s.callLocked(ctx, "execute_command", map[string]any{"command": "formulas.calculationOptions", "params": map[string]any{"mode": "manual"}}); err != nil {
			s.broken.Store(true)
			return 0, err
		}
	}
	fail := func(e error) (int, error) {
		// Nothing is restored: the workbook is a partly changed copy that was never saved. The
		// session is unusable (Close it and open the saved version); a recalculation here would
		// only hold the session longer.
		s.broken.Store(true)
		return applied, e
	}
	for _, op := range ops {
		var e error
		if op.Cell != "" {
			_, e = s.callLocked(ctx, "set_cell", map[string]any{"cell": op.Cell, "input": op.Input})
		} else {
			_, e = s.callLocked(ctx, "write_range", map[string]any{"range": op.Range, "values": op.Values})
		}
		if e != nil {
			return fail(e)
		}
		applied++
	}
	switch {
	case !deferred: // a workbook kept in manual: recalculated once so the agent reads correct values
		_, err = s.callLocked(ctx, "execute_command", map[string]any{"command": "formulas.calculateNow", "params": map[string]any{}})
	case mode == "automatic": // switching back to automatic recalculates the workbook once
		_, err = s.callLocked(ctx, "execute_command", map[string]any{"command": "formulas.calculationOptions", "params": map[string]any{"mode": "automatic"}})
	default: // another automatic variant: its exact mode back, then the one recalculation
		if _, err = s.callLocked(ctx, "execute_command", map[string]any{"command": "formulas.calculationOptions", "params": map[string]any{"mode": mode}}); err == nil {
			_, err = s.callLocked(ctx, "execute_command", map[string]any{"command": "formulas.calculateNow", "params": map[string]any{}})
		}
	}
	if err != nil {
		return fail(err)
	}
	return applied, nil
}

// calcModeLocked reads the workbook's calculation mode ("automatic", "manual", …).
func (s *Session) calcModeLocked(ctx context.Context) (string, error) {
	raw, err := s.callLocked(ctx, "execute_command", map[string]any{"command": "file.options", "params": map[string]any{}})
	if err != nil {
		return "", err
	}
	var out struct {
		Calculation struct {
			Mode string `json:"mode"`
		} `json:"calculation"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Calculation.Mode == "" {
		return "", ErrRefused
	}
	return out.Calculation.Mode, nil
}

// checkBlock validates one block write and returns its cell count.
func checkBlock(rng string, values [][]any, policy InputPolicy) (int, error) {
	if !rangeRe.MatchString(rng) || len(values) == 0 {
		return 0, ErrInvalid
	}
	cells := 0
	for _, row := range values {
		cells += len(row)
		for _, v := range row {
			switch x := v.(type) {
			case nil, bool, float64, int, int64, json.Number:
			case string:
				if !validInput(x) {
					return 0, ErrInvalid
				}
				if policy(x) != nil {
					return 0, ErrPolicy
				}
			default:
				return 0, ErrInvalid
			}
		}
	}
	if cells == 0 || cells > MaxWriteCells {
		return 0, ErrInvalid
	}
	return cells, nil
}

// Snapshot returns the document as compact native JSON, the form that is
// stored. It is the expensive call (the engine serialises everything and its
// memory peaks): callers take one per batch of changes, not one per cell.
func (s *Session) Snapshot(ctx context.Context) ([]byte, error) {
	select {
	case s.snap <- struct{}{}:
		defer func() { <-s.snap }()
	case <-ctx.Done():
		return nil, ErrSnapshotBusy
	}
	if s.format != "" {
		return s.snapshotWorkbook(ctx)
	}
	raw, err := s.call(ctx, "execute_command", map[string]any{"command": "file.saveBytes", "params": map[string]any{"format": "json"}})
	if err != nil {
		return nil, err
	}
	var out struct {
		Base64 string `json:"base64"`
	}
	if json.Unmarshal(raw, &out) != nil || base64.StdEncoding.DecodedLen(len(out.Base64)) > MaxSnapshotBytes {
		return nil, ErrRefused
	}
	raw = nil
	var compact bytes.Buffer
	if compactJSON(&compact, base64.NewDecoder(base64.StdEncoding, strings.NewReader(out.Base64))) != nil || !json.Valid(compact.Bytes()) {
		return nil, ErrRefused
	}
	if s.computed != nil {
		// Every value is current only when the engine recalculated each edit as it came.
		s.mu.Lock()
		mode, err := s.calcModeLocked(ctx)
		s.mu.Unlock()
		if err == nil && mode == "automatic" {
			s.computed(compact.Bytes())
		}
	}
	return compact.Bytes(), nil
}

// compactJSON copies JSON from src to dst without the whitespace outside
// strings, in one pass and a small buffer (the engine writes indented JSON).
func compactJSON(dst *bytes.Buffer, src io.Reader) error {
	buf := make([]byte, 64<<10)
	inString, escaped := false, false
	for {
		n, err := src.Read(buf)
		for _, c := range buf[:n] {
			switch {
			case inString:
				dst.WriteByte(c)
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					inString = false
				}
			case c == '"':
				inString = true
				dst.WriteByte(c)
			case c == ' ' || c == '\n' || c == '\t' || c == '\r':
			default:
				dst.WriteByte(c)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Key is the manager key the session is currently filed under.
func (s *Session) Key() string {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	return s.key
}

func (s *Session) rekey(k string) {
	s.keyMu.Lock()
	s.key = k
	s.keyMu.Unlock()
}

// LastUsed is when the last call started.
func (s *Session) LastUsed() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Alive reports whether the engine process still runs.
func (s *Session) Alive() bool {
	if s.broken.Load() {
		return false
	}
	select {
	case <-s.w.Done():
		return false
	default:
		return true
	}
}

// Close ends the engine process.
func (s *Session) Close() {
	s.done.Do(func() {
		_ = s.w.Close()
		if s.onClose != nil {
			s.onClose()
		}
	})
}

// Code names an error for callers that must not pass the engine's own text
// on: "invalid", "policy", "refused" (the engine said no, for example to a
// formula it cannot parse), "lost" (the engine process is gone) or
// "snapshot_busy"; "" for nil, "failed" otherwise.
func Code(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInvalid):
		return "invalid"
	case errors.Is(err, ErrPolicy):
		return "policy"
	case errors.Is(err, ErrRefused):
		return "refused"
	case errors.Is(err, ErrLost):
		return "lost"
	case errors.Is(err, ErrDiscarded):
		return "discarded"
	case errors.Is(err, ErrSnapshotBusy):
		return "snapshot_busy"
	}
	return "failed"
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// rangeCells counts the cells a range names; the engine's own limits apply
// again on its side.
func rangeCells(rng string) int {
	if i := strings.LastIndex(rng, "!"); i >= 0 {
		rng = rng[i+1:]
	}
	a, b, found := strings.Cut(rng, ":")
	if !found {
		return 1
	}
	c1, r1 := splitCell(a)
	c2, r2 := splitCell(b)
	dc, dr := abs(c2-c1)+1, abs(r2-r1)+1
	if dc > 1<<20 || dr > 1<<20 {
		return 1 << 40
	}
	return dc * dr
}

func splitCell(s string) (col, row int) {
	for _, ch := range strings.ReplaceAll(s, "$", "") {
		if ch >= 'A' && ch <= 'Z' {
			col = col*26 + int(ch-'A'+1)
		} else if ch >= '0' && ch <= '9' {
			row = row*10 + int(ch-'0')
		}
	}
	return
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Format is "xlsx" or "xlsm" for a Sheet kept as its workbook file, "" for a native document.
func (s *Session) Format() string { return s.format }

// snapshotWorkbook asks the engine for the workbook file with its fidelity check: the bytes when
// nothing would be lost, a *FidelityLossError naming the parts otherwise (the engine keeps the
// document open and unchanged, so the session stays usable).
func (s *Session) snapshotWorkbook(ctx context.Context) ([]byte, error) {
	raw, err := s.call(ctx, "execute_command", map[string]any{"command": "file.saveBytes", "params": map[string]any{"format": s.format, "fidelity": true}})
	if err != nil {
		return nil, err
	}
	var out struct {
		Base64 string          `json:"base64"`
		Format string          `json:"format"`
		Parts  json.RawMessage `json:"parts"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Format != s.format {
		return nil, ErrRefused
	}
	parts, ok := fidelityParts([]byte(`{"parts":` + string(out.Parts) + `}`))
	switch {
	case !ok:
		return nil, ErrRefused
	case len(parts) > 0:
		if out.Base64 != "" {
			return nil, ErrRefused
		}
		return nil, &FidelityLossError{Parts: parts}
	}
	if base64.StdEncoding.DecodedLen(len(out.Base64)) > mooring.MaxWorkbookBytes+3 {
		return nil, ErrRefused
	}
	data, err := base64.StdEncoding.DecodeString(out.Base64)
	if err != nil || len(data) == 0 || len(data) > mooring.MaxWorkbookBytes || !bytes.HasPrefix(data, []byte("PK")) {
		return nil, ErrRefused
	}
	return data, nil
}
