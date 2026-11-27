package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/mooring"
)

var ErrCraftNativeAgentEngine = errors.New("native Craft agent engine operation refused")

// CraftNativeAgentCLIAdapter accepts a small command language, not an engine
// script. Executables and document paths come exclusively from trusted code.
type CraftNativeAgentCLIAdapter struct {
	timeout time.Duration
	rules   nativeAgentEngineRules
	// sheetOpenCheck is read by the session adapter built from these options.
	sheetOpenCheck func(base, snapshot []byte) error
}

// CraftNativeAgentOption changes what the adapter accepts. The default accepts
// no formulas.
type CraftNativeAgentOption func(*CraftNativeAgentCLIAdapter)

// WithCraftNativeAgentFormulas lets Sheet writes carry formulas, but only
// formulas that call functions from nativeAgentFormulaFunctions and reference
// nothing outside this workbook.
func WithCraftNativeAgentFormulas() CraftNativeAgentOption {
	return func(a *CraftNativeAgentCLIAdapter) { a.rules.formulas = true }
}

func NewCraftNativeAgentCLIAdapter(options ...CraftNativeAgentOption) *CraftNativeAgentCLIAdapter {
	a := &CraftNativeAgentCLIAdapter{timeout: 30 * time.Second}
	for _, option := range options {
		option(a)
	}
	return a
}

// nativeAgentEngineRules is what one adapter accepts beyond the fixed surface.
// restore is a person's undo: it puts back what the document held, so a formula is the
// document's own and is not held to the list an agent may write.
type nativeAgentEngineRules struct{ formulas, restore bool }

// text accepts a literal, or with formulas enabled a validated formula.
func (r nativeAgentEngineRules) text(value string) bool {
	if r.restore && strings.HasPrefix(strings.TrimSpace(value), "=") {
		return true
	}
	if r.formulas && strings.HasPrefix(strings.TrimSpace(value), "=") {
		return nativeAgentEngineFormula(value)
	}
	return nativeAgentEngineScalar(value)
}

type nativeAgentEngineCommand struct {
	Command string
	Params  json.RawMessage
}

func nativeAgentEngineObject(raw []byte, keys ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) != len(keys) {
		return nil, false
	}
	for _, key := range keys {
		if fields[key] == nil {
			return nil, false
		}
	}
	return fields, true
}

func nativeAgentEngineCell(raw json.RawMessage) bool {
	var value string
	if json.Unmarshal(raw, &value) != nil || len(value) < 2 || len(value) > 10 {
		return false
	}
	col, i := 0, 0
	for i < len(value) && value[i] >= 'A' && value[i] <= 'Z' {
		if i >= 3 {
			return false
		}
		col = col*26 + int(value[i]-'A'+1)
		i++
	}
	if col < 1 || col > 16384 || i == len(value) || value[i] < '1' || value[i] > '9' {
		return false
	}
	for _, ch := range value[i:] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	row, err := strconv.Atoi(value[i:])
	return err == nil && row >= 1 && row <= 1048576
}

// Only scalar/literal cell entry is enabled. In particular Excel-style leading
// +FUNC/-FUNC also enters formula parsing in the qualified original engine.
func nativeAgentEngineScalar(value string) bool {
	text := strings.TrimSpace(value)
	// "=" starts a formula; "@" is the Lotus-style function prefix the engine also reads as one.
	if strings.HasPrefix(text, "=") || strings.HasPrefix(text, "@") {
		return false
	}
	if strings.HasPrefix(text, "+") || strings.HasPrefix(text, "-") {
		number, err := strconv.ParseFloat(text, 64)
		return err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	}
	return true
}

// Sheet limits for one agent request. The request body is separately capped at
// 256 KiB, and the engine's reply at 1 MiB, so these keep one call predictable.
const (
	nativeAgentWriteRows  = 1000
	nativeAgentWriteCols  = 200
	nativeAgentReadCells  = 10000
	nativeAgentSheetMax   = 31
	nativeAgentMaxColumns = 16384
	nativeAgentMaxRows    = 1048576
)

// nativeAgentEngineParams is nativeAgentEngineObject with optional keys: every
// required key must be present, nothing outside required and optional may be.
func nativeAgentEngineParams(raw []byte, required []string, optional ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, key := range required {
		if fields[key] == nil {
			return nil, false
		}
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return nil, false
		}
	}
	return fields, true
}

// nativeAgentEngineOptionalSheet accepts an absent sheet or a valid worksheet
// name: 1 to 31 characters, none of []:*?/\, no control characters, no
// leading or trailing apostrophe.
func nativeAgentEngineOptionalSheet(p map[string]json.RawMessage) bool {
	raw, present := p["sheet"]
	if !present {
		return true
	}
	var name string
	if json.Unmarshal(raw, &name) != nil || !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > nativeAgentSheetMax || strings.HasPrefix(name, "'") || strings.HasSuffix(name, "'") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`[]:*?/\`, r) {
			return false
		}
	}
	return true
}

func nativeAgentEngineOptionalBool(p map[string]json.RawMessage, key string) bool {
	raw, present := p[key]
	if !present {
		return true
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil
}

type nativeAgentEngineArea struct{ col1, row1, col2, row2 int }

func (a nativeAgentEngineArea) width() int  { return a.col2 - a.col1 + 1 }
func (a nativeAgentEngineArea) height() int { return a.row2 - a.row1 + 1 }
func (a nativeAgentEngineArea) cells() int  { return a.width() * a.height() }

// nativeAgentEngineAddress parses one A1 address (no sheet prefix, no $).
func nativeAgentEngineAddress(value string) (col, row int, ok bool) {
	i := 0
	for i < len(value) && value[i] >= 'A' && value[i] <= 'Z' {
		if i >= 3 {
			return 0, 0, false
		}
		col = col*26 + int(value[i]-'A'+1)
		i++
	}
	if col < 1 || col > nativeAgentMaxColumns || i == len(value) || value[i] < '1' || value[i] > '9' || len(value)-i > 7 {
		return 0, 0, false
	}
	for _, ch := range value[i:] {
		if ch < '0' || ch > '9' {
			return 0, 0, false
		}
	}
	row, err := strconv.Atoi(value[i:])
	return col, row, err == nil && row >= 1 && row <= nativeAgentMaxRows
}

// nativeAgentEngineRange parses "A1" or "A1:C3" with the corners in order.
func nativeAgentEngineRange(raw json.RawMessage) (nativeAgentEngineArea, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil || len(value) < 2 || len(value) > 21 {
		return nativeAgentEngineArea{}, false
	}
	first, second, ranged := strings.Cut(value, ":")
	if !ranged {
		second = first
	}
	c1, r1, ok1 := nativeAgentEngineAddress(first)
	c2, r2, ok2 := nativeAgentEngineAddress(second)
	if !ok1 || !ok2 || c2 < c1 || r2 < r1 {
		return nativeAgentEngineArea{}, false
	}
	return nativeAgentEngineArea{c1, r1, c2, r2}, true
}

// nativeAgentEngineValues validates a bulk write: rows of numbers, booleans,
// null and strings. A multi-cell range must match the size of the values, a
// single address anchors them, and nothing may run past the sheet's last cell.
// Strings go through the same scalar rule as cell.set.
func nativeAgentEngineValues(rangeRaw, valuesRaw json.RawMessage, rules nativeAgentEngineRules) bool {
	area, ok := nativeAgentEngineRange(rangeRaw)
	if !ok {
		return false
	}
	var rows []json.RawMessage
	if json.Unmarshal(valuesRaw, &rows) != nil || len(rows) == 0 || len(rows) > nativeAgentWriteRows {
		return false
	}
	width := 0
	for _, rowRaw := range rows {
		var row []json.RawMessage
		if json.Unmarshal(rowRaw, &row) != nil || len(row) == 0 || len(row) > nativeAgentWriteCols {
			return false
		}
		if len(row) > width {
			width = len(row)
		}
		for _, cellRaw := range row {
			if !nativeAgentEngineValue(cellRaw, rules) {
				return false
			}
		}
	}
	if area.cells() > 1 && (area.height() != len(rows) || area.width() != width) {
		return false
	}
	return area.col1+width-1 <= nativeAgentMaxColumns && area.row1+len(rows)-1 <= nativeAgentMaxRows
}

func nativeAgentEngineValue(raw json.RawMessage, rules nativeAgentEngineRules) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case 'n':
		return string(trimmed) == "null"
	case 't', 'f':
		var value bool
		return json.Unmarshal(trimmed, &value) == nil
	case '"':
		var text string
		return json.Unmarshal(trimmed, &text) == nil && len(text) <= 32<<10 && utf8.ValidString(text) && rules.text(text)
	case '[', '{':
		return false
	}
	var number float64
	return json.Unmarshal(trimmed, &number) == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func nativeAgentEngineCommands(application string, raw json.RawMessage, inspect bool) ([]nativeAgentEngineCommand, error) {
	return nativeAgentEngineCommandsFor(application, raw, inspect, nativeAgentEngineRules{})
}

func nativeAgentEngineCommandsFor(application string, raw json.RawMessage, inspect bool, rules nativeAgentEngineRules) ([]nativeAgentEngineCommand, error) {
	if len(raw) == 0 || len(raw) > 256<<10 || !utf8.Valid(raw) {
		return nil, ErrCraftNativeAgentEngine
	}
	canonical, err := mooring.CanonicalJSON(raw)
	if err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	var entries []json.RawMessage
	if inspect {
		entries = []json.RawMessage{canonical}
	} else if json.Unmarshal(canonical, &entries) != nil || len(entries) == 0 || len(entries) > 64 {
		return nil, ErrCraftNativeAgentEngine
	}
	commands := make([]nativeAgentEngineCommand, 0, len(entries))
	mutates := false
	for _, entry := range entries {
		fields, ok := nativeAgentEngineObject(entry, "command", "params")
		var name string
		if !ok || json.Unmarshal(fields["command"], &name) != nil {
			return nil, ErrCraftNativeAgentEngine
		}
		params := fields["params"]
		if application == "gridcraft" {
			if params, ok = nativeAgentLiftSheet(name, params); !ok {
				return nil, ErrCraftNativeAgentEngine
			}
		}
		switch {
		case application == "wordcraft" && inspect && name == "text":
			_, ok = nativeAgentEngineObject(params)
		case application == "wordcraft" && !inspect && (name == "caret.docStart" || name == "caret.docEnd"):
			_, ok = nativeAgentEngineObject(params)
		case application == "wordcraft" && !inspect && name == "text.insert":
			var text string
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineObject(params, "text")
			ok = ok && json.Unmarshal(p["text"], &text) == nil && len(text) > 0 && len(text) <= 64<<10 && utf8.ValidString(text)
			mutates = true
		case application == "gridcraft" && !inspect && name == "cell.set":
			var input string
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineParams(params, []string{"cell", "input"}, "sheet")
			ok = ok && nativeAgentEngineCell(p["cell"]) && nativeAgentEngineOptionalSheet(p) && len(p["input"]) > 0 && p["input"][0] == '"' && json.Unmarshal(p["input"], &input) == nil && len(input) <= 32<<10 && utf8.ValidString(input) && rules.text(input)
			mutates = true
		case application == "gridcraft" && !inspect && name == "formulas.calculateNow":
			// Every write recalculates already: accepted, an effect of the writes, changes nothing alone.
			_, ok = nativeAgentEngineObject(params)
		case application == "gridcraft" && !inspect && name == "range.clear":
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineParams(params, []string{"range"}, "sheet")
			if ok {
				_, ok = nativeAgentEngineRange(p["range"])
				ok = ok && nativeAgentEngineOptionalSheet(p)
			}
			mutates = true
		case application == "gridcraft" && !inspect && name == "range.setValues":
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineParams(params, []string{"range", "values"}, "sheet")
			ok = ok && nativeAgentEngineOptionalSheet(p) && nativeAgentEngineValues(p["range"], p["values"], rules)
			mutates = true
		case application == "gridcraft" && inspect && name == "cell.get":
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineParams(params, []string{"cell"}, "sheet")
			ok = ok && nativeAgentEngineCell(p["cell"]) && nativeAgentEngineOptionalSheet(p)
		case application == "gridcraft" && inspect && name == "sheet.read":
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineParams(params, []string{"range"}, "sheet", "formulas", "formatted")
			ok = ok && nativeAgentEngineOptionalSheet(p) && nativeAgentEngineOptionalBool(p, "formulas") && nativeAgentEngineOptionalBool(p, "formatted")
			if ok {
				var area nativeAgentEngineArea
				area, ok = nativeAgentEngineRange(p["range"])
				ok = ok && area.cells() <= nativeAgentReadCells
			}
		case application == "gridcraft" && inspect && name == "range.columnByHeader":
			var header string
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineParams(params, []string{"header"}, "sheet", "matchCase")
			ok = ok && json.Unmarshal(p["header"], &header) == nil && header != "" && len(header) <= 256 && utf8.ValidString(header) && nativeAgentEngineOptionalSheet(p) && nativeAgentEngineOptionalBool(p, "matchCase")
		case application == "gridcraft" && inspect && name == "document.inspect":
			_, ok = nativeAgentEngineObject(params)
		default:
			ok = false
		}
		if !ok {
			return nil, ErrCraftNativeAgentEngine
		}
		commands = append(commands, nativeAgentEngineCommand{name, params})
	}
	if !inspect && !mutates {
		return nil, ErrCraftNativeAgentEngine
	}
	return commands, nil
}

type nativeAgentEngineOutput struct {
	bytes.Buffer
	limit int
}

func (b *nativeAgentEngineOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrCraftNativeAgentEngine
	}
	return b.Buffer.Write(p)
}

func (a *CraftNativeAgentCLIAdapter) run(ctx context.Context, p CraftNativeDocumentProfile, raw []byte, commands []nativeAgentEngineCommand, inspect bool) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 32<<20 || !utf8.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, ErrCraftNativeAgentEngine
	}
	if !json.Valid(raw) || a == nil || a.timeout <= 0 || a.timeout > mooring.MaxValidationTime {
		return nil, ErrCraftNativeAgentEngine
	}
	// One run validates the document and one applies the commands: each gets the time a
	// document of this size needs (the configured base, plus more for a larger document).
	each := mooring.ValidationTimeoutFrom(a.timeout, len(raw))
	ctx, cancel := context.WithTimeout(ctx, 2*each)
	defer cancel()
	// Refuse import that silently drops authored fields before editing it. Store
	// validation remains independent and validates the returned candidate again.
	engine, err := craftNativeProfileEngine(p)
	if err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	p.Engine = engine
	v := mooring.NativeCLIValidator{Application: p.Application, Engine: p.Engine, Timeout: each}
	if err := v.Validate(ctx, raw); err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	dir, err := os.MkdirTemp("", "craft-native-agent-")
	if err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "result.json")
	root := p.Engine.Root(dir)
	input, seenOutput := root+"/document.json", root+"/result.json"
	if err = os.WriteFile(filepath.Join(dir, "document.json"), raw, 0600); err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	var args []string
	if p.Application == "wordcraft" {
		args = []string{"run", "--file", input}
		if inspect {
			args = []string{"text", input}
		}
	} else {
		args = []string{"run", "--in", input}
		if !inspect {
			args = append(args, "--quiet")
		}
	}
	if !inspect || p.Application == "gridcraft" {
		for _, command := range commands {
			args = append(args, "--cmd", command.Command+"="+string(command.Params))
		}
	}
	if !inspect {
		flag := "--save"
		if p.Application == "gridcraft" {
			flag = "--out"
		}
		args = append(args, flag, seenOutput)
	}
	stdout := &nativeAgentEngineOutput{limit: 1 << 20}
	stderr := &nativeAgentEngineOutput{limit: 64 << 10}
	if err = p.Engine.Run(ctx, dir, args, stdout, stderr); err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	if inspect {
		if stderr.Len() != 0 || !utf8.Valid(stdout.Bytes()) {
			return nil, ErrCraftNativeAgentEngine
		}
		if p.Application == "wordcraft" {
			return json.Marshal(map[string]string{"text": strings.TrimSuffix(stdout.String(), "\n")})
		}
		result := bytes.TrimSpace(stdout.Bytes())
		fields, ok := nativeAgentEngineObject(result, "command", "result")
		wanted, _ := json.Marshal(commands[0].Command)
		if !json.Valid(result) || !ok || string(fields["command"]) != string(wanted) {
			return nil, ErrCraftNativeAgentEngine
		}
		return append([]byte(nil), result...), nil
	}
	info, err := os.Lstat(output)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 32<<20 {
		return nil, ErrCraftNativeAgentEngine
	}
	expected := "wrote " + seenOutput + "\n"
	if p.Application == "gridcraft" {
		expected = "wrote " + seenOutput + " (" + strconv.FormatInt(info.Size(), 10) + " bytes)\n"
	}
	if stdout.Len() != 0 || stderr.String() != expected {
		return nil, ErrCraftNativeAgentEngine
	}
	result, err := os.ReadFile(output)
	if err != nil || len(result) > 32<<20 || !utf8.Valid(result) || len(bytes.TrimSpace(result)) == 0 || bytes.TrimSpace(result)[0] != '{' {
		return nil, ErrCraftNativeAgentEngine
	}
	if !json.Valid(result) {
		return nil, ErrCraftNativeAgentEngine
	}
	return result, nil
}

func (a *CraftNativeAgentCLIAdapter) Apply(ctx context.Context, p CraftNativeDocumentProfile, raw []byte, commands json.RawMessage) ([]byte, error) {
	rules := a.rules
	if craftNativeUndo(ctx) {
		rules.formulas, rules.restore = true, true
	}
	parsed, err := nativeAgentEngineCommandsFor(p.Application, commands, false, rules)
	if err != nil {
		return nil, err
	}
	return a.run(ctx, p, raw, parsed, false)
}

func (a *CraftNativeAgentCLIAdapter) Inspect(ctx context.Context, p CraftNativeDocumentProfile, raw []byte, query json.RawMessage) (json.RawMessage, error) {
	parsed, err := nativeAgentEngineCommandsFor(p.Application, query, true, a.rules)
	if err != nil {
		return nil, err
	}
	return a.run(ctx, p, raw, parsed, true)
}

// craftNativeProfileEngine returns the profile's engine, or the native
// development override its CLI pin names when no engine was resolved yet.
func craftNativeProfileEngine(p CraftNativeDocumentProfile) (craftengine.Engine, error) {
	if p.Engine != nil {
		if p.Engine.Application() != p.Application {
			return nil, ErrCraftNativeAgentEngine
		}
		return p.Engine, nil
	}
	if p.CLIPath == "" {
		return nil, ErrCraftNativeAgentEngine
	}
	return craftengine.NewNative(p.Application, p.CLIPath, p.CLISHA256)
}

// craftNativeSheetSetArrays names the arrays of a native Sheet document whose
// order carries no meaning: the engine may write them in another order. Every
// other array keeps its order: rows, cells, sheets and their values are
// positional.
var craftNativeSheetSetArrays = map[string]bool{"names": true, "defined_names": true, "definedNames": true, "merges": true, "hyperlinks": true}

// CraftNativeSheetSnapshotKeepsEverything is the open check the server gives a
// session adapter. The engine's serialisation of a version must still hold
// everything the version held: every sheet, cell, formula, defined name,
// table, chart and pivot of the original has to be present, with equal values,
// in the same position. Differences that lose nothing are fine: another key
// order, another spelling of a number (1, 1.0, 1e0), the order of the arrays
// named in craftNativeSheetSetArrays, and fields the engine adds or leaves
// out when they are empty. A lost, moved or changed value is refused, and the
// version is never edited.
func CraftNativeSheetSnapshotKeepsEverything(base, snapshot []byte) error {
	if _, err := CraftNativeSheetFirstLoss(base, snapshot); err != nil {
		return err
	}
	return nil
}

// CraftNativeSheetFirstLoss is the same check naming where it failed: a path
// of keys and indexes (sheets[1].cells[17]), never a value. The path is empty
// when nothing was lost. It exists for diagnostics.
func CraftNativeSheetFirstLoss(base, snapshot []byte) (string, error) {
	var a, b any
	for _, c := range []struct {
		raw []byte
		out *any
	}{{base, &a}, {snapshot, &b}} {
		dec := json.NewDecoder(bytes.NewReader(c.raw))
		dec.UseNumber()
		if dec.Decode(c.out) != nil || dec.More() {
			return "$", ErrCraftNativeAgentEngine
		}
	}
	if path := jsonFirstLoss(a, b, "$", ""); path != "" {
		return path, ErrCraftNativeAgentEngine
	}
	return "", nil
}

func jsonEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == ""
	case json.Number:
		f, err := x.Float64()
		return err == nil && f == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// jsonFirstLoss returns the path of the first thing want holds that got does
// not, or "" when got keeps everything. key is the name of the object field
// that holds want ("" at the top and inside arrays).
func jsonFirstLoss(want, got any, path, key string) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			if jsonEmpty(want) && jsonEmpty(got) {
				return ""
			}
			return path
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			other, present := g[k]
			if !present {
				if !jsonEmpty(w[k]) {
					return path + "." + k
				}
				continue
			}
			if loss := jsonFirstLoss(w[k], other, path+"."+k, k); loss != "" {
				return loss
			}
		}
		return ""
	case []any:
		g, ok := got.([]any)
		if !ok {
			if jsonEmpty(want) && jsonEmpty(got) {
				return ""
			}
			return path
		}
		if len(g) >= len(w) {
			first := ""
			for i := range w {
				if loss := jsonFirstLoss(w[i], g[i], path+"["+strconv.Itoa(i)+"]", ""); loss != "" {
					first = loss
					break
				}
			}
			if first == "" {
				return ""
			}
			if !craftNativeSheetSetArrays[key] {
				return first
			}
		} else if !craftNativeSheetSetArrays[key] {
			return path + "[" + strconv.Itoa(len(g)) + "]"
		}
		// A set: every element of want must appear, equal, in got.
		remaining := make(map[string]int, len(g))
		for _, e := range g {
			remaining[jsonCanonical(e)]++
		}
		for i, e := range w {
			k := jsonCanonical(e)
			if remaining[k] == 0 {
				return path + "[" + strconv.Itoa(i) + "]"
			}
			remaining[k]--
		}
		return ""
	case json.Number:
		g, ok := got.(json.Number)
		if !ok {
			if jsonEmpty(want) && jsonEmpty(got) {
				return ""
			}
			return path
		}
		x, errX := w.Float64()
		y, errY := g.Float64()
		if errX != nil || errY != nil {
			if w.String() == g.String() {
				return ""
			}
			return path
		}
		if math.Abs(x-y) <= math.Max(1, math.Max(math.Abs(x), math.Abs(y)))*1e-12 {
			return ""
		}
		return path
	case string:
		if g, ok := got.(string); ok && g == w || !ok && jsonEmpty(want) && jsonEmpty(got) {
			return ""
		}
		return path
	case bool:
		if g, ok := got.(bool); ok && g == w || !ok && jsonEmpty(want) && jsonEmpty(got) {
			return ""
		}
		return path
	case nil:
		return ""
	}
	return path
}

// jsonCanonical renders a value with sorted keys and numbers by value, for
// comparing the elements of a set array regardless of number spelling.
func jsonCanonical(v any) string {
	switch x := v.(type) {
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return strconv.FormatFloat(f, 'g', 12, 64)
		}
		return x.String()
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for _, k := range keys {
			b.WriteString(strconv.Quote(k))
			b.WriteByte(':')
			b.WriteString(jsonCanonical(x[k]))
			b.WriteByte(',')
		}
		b.WriteByte('}')
		return b.String()
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for _, e := range x {
			b.WriteString(jsonCanonical(e))
			b.WriteByte(',')
		}
		b.WriteByte(']')
		return b.String()
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// nativeAgentLiftSheet moves a sheet named in the address of a write ('L'!B8, L!B8, quotes doubled inside) to
// params.sheet, so the agent may name it either way; an address and a params.sheet naming two
// different sheets are refused.
func nativeAgentLiftSheet(command string, params json.RawMessage) (json.RawMessage, bool) {
	key := ""
	switch command {
	case "cell.set":
		key = "cell"
	case "range.setValues", "range.clear":
		key = "range"
	default:
		return params, true
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(params, &p) != nil {
		return params, true
	}
	var address string
	if json.Unmarshal(p[key], &address) != nil {
		return params, true
	}
	cut := strings.LastIndex(address, "!")
	if cut < 0 {
		return params, true
	}
	name, rest := address[:cut], address[cut+1:]
	if len(name) >= 2 && strings.HasPrefix(name, "'") && strings.HasSuffix(name, "'") {
		name = strings.ReplaceAll(name[1:len(name)-1], "''", "'")
	}
	if name == "" || rest == "" {
		return nil, false
	}
	if raw, ok := p["sheet"]; ok {
		var given string
		if json.Unmarshal(raw, &given) != nil || given != name {
			return nil, false
		}
	}
	p[key], _ = json.Marshal(rest)
	p["sheet"], _ = json.Marshal(name)
	out, err := json.Marshal(p)
	return out, err == nil
}
