package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type fakeSheetSession struct {
	calls    []string
	writes   [][]any
	snapshot []byte
	failOn   string
	applies  int
	columns  json.RawMessage
}

func (s *fakeSheetSession) note(call string) error {
	s.calls = append(s.calls, call)
	if s.failOn == call {
		return errors.New("engine refused")
	}
	return nil
}
func (s *fakeSheetSession) ReadRange(_ context.Context, rng string, formulas bool) (CraftNativeSheetValues, error) {
	suffix := ""
	if formulas {
		suffix = "+f"
	}
	return CraftNativeSheetValues{Range: rng, Sheet: "S", Values: [][]any{{1, "x"}}}, s.note("read " + rng + suffix)
}
func (s *fakeSheetSession) GetCell(_ context.Context, cell string) (CraftNativeSheetCell, error) {
	return CraftNativeSheetCell{Cell: cell, Input: "=A1+1", Value: 2}, s.note("get " + cell)
}
func (s *fakeSheetSession) SetCell(_ context.Context, cell, input string) error {
	return s.note("set " + cell + "=" + input)
}
func (s *fakeSheetSession) WriteRange(_ context.Context, rng string, values [][]any) (int, error) {
	s.writes = append(s.writes, values...)
	return len(values), s.note("write " + rng)
}
func (s *fakeSheetSession) Apply(_ context.Context, ops []CraftNativeSheetWrite) (int, error) {
	s.applies++
	for i, op := range ops {
		call := "write " + op.Range
		if op.Cell != "" {
			call = "set " + op.Cell + "=" + op.Input
		} else {
			s.writes = append(s.writes, op.Values...)
		}
		if err := s.note(call); err != nil {
			return i, err
		}
	}
	return len(ops), nil
}
func (s *fakeSheetSession) ClearRange(_ context.Context, rng string) error {
	return s.note("clear " + rng)
}
func (s *fakeSheetSession) ColumnsByHeader(_ context.Context, header, sheet string, matchCase bool) (json.RawMessage, error) {
	return s.columns, s.note(fmt.Sprintf("header %s sheet=%s case=%v", header, sheet, matchCase))
}
func (s *fakeSheetSession) Sheets(context.Context) ([]CraftNativeSheetInfo, error) {
	return []CraftNativeSheetInfo{{Name: "Data", Used: "A1:C9"}}, s.note("sheets")
}
func (s *fakeSheetSession) Snapshot(context.Context) ([]byte, error) {
	return s.snapshot, s.note("snapshot")
}

type fakeSheetEngine struct {
	session   *fakeSheetSession
	opened    int
	released  int
	retained  []byte
	openErr   error
	discarded int
}

func (e *fakeSheetEngine) Open(context.Context, []byte) (CraftNativeSheetSession, func(), error) {
	if e.openErr != nil {
		return nil, nil, e.openErr
	}
	e.opened++
	return e.session, func() { e.released++ }, nil
}
func (e *fakeSheetEngine) Retain(_ CraftNativeSheetSession, snapshot []byte) { e.retained = snapshot }
func (e *fakeSheetEngine) Discard(CraftNativeSheetSession)                   { e.discarded++ }

func sheetFixture() (*CraftNativeSheetAgentAdapter, *fakeSheetEngine, CraftNativeDocumentProfile) {
	engine := &fakeSheetEngine{session: &fakeSheetSession{snapshot: []byte(`{"sheets":[]}`)}}
	return NewCraftNativeSheetAgentAdapter(engine), engine, CraftNativeDocumentProfile{Application: "gridcraft"}
}

func TestSheetSessionApplyTranslatesCommandsAndRetainsTheSnapshot(t *testing.T) {
	a, engine, p := sheetFixture()
	commands := json.RawMessage(`[{"command":"cell.set","params":{"cell":"B2","input":"41","sheet":"Q1 Sales"}},{"command":"range.setValues","params":{"range":"A1","values":[[1,"two"],[3,4]]}}]`)
	out, err := a.Apply(context.Background(), p, []byte(`{"base":1}`), commands)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"sheets":[]}` || string(engine.retained) != string(out) {
		t.Fatalf("snapshot %s retained %s", out, engine.retained)
	}
	// Text keeps the single-cell path; the numbers around it are two runs.
	want := []string{"set 'Q1 Sales'!B2=41", "set B1=two", "write A1", "write A2", "snapshot"}
	if strings.Join(engine.session.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v, want %v", engine.session.calls, want)
	}
	if engine.opened != 1 || engine.released != 1 {
		t.Fatalf("opened %d released %d", engine.opened, engine.released)
	}
}

func TestSheetSessionInspectAnswersInTheEngineShape(t *testing.T) {
	a, engine, p := sheetFixture()
	for query, want := range map[string]string{
		`{"command":"cell.get","params":{"cell":"A1","sheet":"Data"}}`:                        "get 'Data'!A1",
		`{"command":"sheet.read","params":{"range":"A1:B2","formulas":true}}`:                 "read A1:B2+f",
		`{"command":"document.inspect","params":{}}`:                                          "sheets",
		`{"command":"sheet.read","params":{"range":"A1:B2","sheet":"Data","formulas":false}}`: "read 'Data'!A1:B2",
	} {
		engine.session.calls = nil
		out, err := a.Inspect(context.Background(), p, []byte(`{}`), json.RawMessage(query))
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		var shaped struct {
			Command string          `json:"command"`
			Result  json.RawMessage `json:"result"`
		}
		if json.Unmarshal(out, &shaped) != nil || shaped.Command == "" || len(shaped.Result) == 0 {
			t.Fatalf("answer shape: %s", out)
		}
		if len(engine.session.calls) != 1 || engine.session.calls[0] != want {
			t.Fatalf("%s: calls %v, want %s", query, engine.session.calls, want)
		}
	}
}

// Validation happens first: a refused command never opens a session.
func TestSheetSessionRefusesBeforeOpeningAnything(t *testing.T) {
	a, engine, p := sheetFixture()
	for _, raw := range []string{
		`[{"command":"cell.set","params":{"cell":"A1","input":"=WEBSERVICE(\"u\")"}}]`,
		`[{"command":"cell.set","params":{"cell":"A1","input":"=1+1"}}]`,
		`[{"command":"file.save","params":{"path":"/tmp/x"}}]`,
		`[{"command":"range.setValues","params":{"range":"A1","values":[[{"a":1}]]}}]`,
		`[{"command":"cell.set","params":{"cell":"A1","input":"x","sheet":"it's"}}]`,
	} {
		if _, err := a.Apply(context.Background(), p, []byte(`{}`), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted: %s", raw)
		}
	}
	if _, err := a.Inspect(context.Background(), p, []byte(`{}`), json.RawMessage(`{"command":"history.list","params":{}}`)); err == nil {
		t.Error("unlisted query accepted")
	}
	if engine.opened != 0 {
		t.Fatalf("a refused request opened %d sessions", engine.opened)
	}
}

func TestSheetSessionEngineFailuresAreRefusalsAndRetainNothing(t *testing.T) {
	a, engine, p := sheetFixture()
	engine.session.failOn = "set A1=x"
	if _, err := a.Apply(context.Background(), p, []byte(`{}`), json.RawMessage(`[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]`)); !errors.Is(err, ErrCraftNativeAgentEngine) {
		t.Fatalf("engine refusal gave %v", err)
	}
	if engine.retained != nil || engine.released != 1 || engine.discarded != 1 {
		t.Fatalf("retained %q released %d discarded %d: a session with a half-applied batch must be discarded", engine.retained, engine.released, engine.discarded)
	}
	engine.session.failOn, engine.session.snapshot = "", nil
	if _, err := a.Apply(context.Background(), p, []byte(`{}`), json.RawMessage(`[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]`)); err == nil {
		t.Fatal("an empty snapshot was accepted")
	}
	engine.openErr = ErrCraftNativeSheetBusy
	if _, err := a.Apply(context.Background(), p, []byte(`{}`), json.RawMessage(`[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]`)); !errors.Is(err, ErrCraftNativeSheetBusy) {
		t.Fatalf("a busy engine on apply gave %v", err)
	}
	if _, err := a.Inspect(context.Background(), p, []byte(`{}`), json.RawMessage(`{"command":"document.inspect","params":{}}`)); !errors.Is(err, ErrCraftNativeSheetBusy) {
		t.Fatalf("a busy engine on inspect gave %v", err)
	}
	engine.openErr = errors.New("engine crashed")
	if _, err := a.Inspect(context.Background(), p, []byte(`{}`), json.RawMessage(`{"command":"document.inspect","params":{}}`)); !errors.Is(err, ErrCraftNativeAgentEngine) {
		t.Fatalf("open failure gave %v", err)
	}
}

func TestSheetSessionFormulasOnlyWithTheOption(t *testing.T) {
	engine := &fakeSheetEngine{session: &fakeSheetSession{snapshot: []byte(`{}`)}}
	p := CraftNativeDocumentProfile{Application: "gridcraft"}
	formula := json.RawMessage(`[{"command":"cell.set","params":{"cell":"C2","input":"=SUM(A2:B2)"}}]`)
	if _, err := NewCraftNativeSheetAgentAdapter(engine).Apply(context.Background(), p, []byte(`{}`), formula); err == nil {
		t.Fatal("formula accepted without the option")
	}
	if _, err := NewCraftNativeSheetAgentAdapter(engine, WithCraftNativeAgentFormulas()).Apply(context.Background(), p, []byte(`{}`), formula); err != nil {
		t.Fatalf("allowlisted formula refused: %v", err)
	}
}

func TestSheetSessionKeyIsTheContentHash(t *testing.T) {
	if sheetKey([]byte("a")) == sheetKey([]byte("b")) || sheetKey([]byte("a")) != sheetKey(append([]byte(nil), 'a')) {
		t.Fatal("key is not a stable content hash")
	}
}

func TestCraftNativeAgentInputPolicy(t *testing.T) {
	literal, formulas := CraftNativeAgentInputPolicy(false), CraftNativeAgentInputPolicy(true)
	for _, in := range []string{"", "text", "41", "-4.5", "'=not a formula"} {
		if literal(in) != nil || formulas(in) != nil {
			t.Errorf("literal %q refused", in)
		}
	}
	for _, in := range []string{"=1+1", "=SUM(A1:A3)", "+SUM(A1)", "@A1"} {
		if literal(in) == nil {
			t.Errorf("formula %q accepted without the option", in)
		}
	}
	if formulas("=SUM(A1:A3)") != nil || formulas("=WEBSERVICE(\"u\")") == nil || formulas("=INDIRECT(\"A1\")") == nil {
		t.Error("formula policy wrong with the option")
	}
}

func TestSheetSessionChecksAnOpenedVersionOncePerVersion(t *testing.T) {
	engine := &fakeSheetEngine{session: &fakeSheetSession{snapshot: []byte(`{"sheets":["next"]}`)}}
	checked := 0
	var fail error
	a := NewCraftNativeSheetAgentAdapter(engine, WithCraftNativeSheetOpenCheck(func(base, snapshot []byte) error {
		checked++
		return fail
	}))
	p := CraftNativeDocumentProfile{Application: "gridcraft"}
	set := json.RawMessage(`[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]`)
	for i := 0; i < 3; i++ {
		if _, err := a.Apply(context.Background(), p, []byte(`{"v":1}`), set); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Inspect(context.Background(), p, []byte(`{"v":1}`), json.RawMessage(`{"command":"document.inspect","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	if checked != 1 {
		t.Fatalf("the same version was checked %d times", checked)
	}
	// What the engine itself serialised counts as seen; another version does not.
	if _, err := a.Apply(context.Background(), p, []byte(`{"sheets":["next"]}`), set); err != nil || checked != 1 {
		t.Fatalf("the engine's own snapshot was checked again: %d %v", checked, err)
	}
	if _, err := a.Apply(context.Background(), p, []byte(`{"v":2}`), set); err != nil || checked != 2 {
		t.Fatalf("a new version was not checked: %d %v", checked, err)
	}
	fail, engine.session.calls = errors.New("drops authored fields"), nil
	if _, err := a.Apply(context.Background(), p, []byte(`{"v":3}`), set); err == nil {
		t.Fatal("a version the engine did not keep intact was edited")
	}
	for _, call := range engine.session.calls {
		if strings.HasPrefix(call, "set ") || strings.HasPrefix(call, "write ") {
			t.Fatalf("a refused version was changed: %v", engine.session.calls)
		}
	}
	if engine.released == 0 {
		t.Fatal("a refused session was not given back")
	}
}

func TestSnapshotKeepsEverythingRefusesLossAndAcceptsNormalisation(t *testing.T) {
	base := `{"sheets":[{"name":"A","cells":[{"r":1,"v":1},{"r":2,"v":"x"}],"merges":[],"view":{"zoom":100}},{"name":"B","names":{"n":"A!A1"},"charts":[{"id":1}],"pivots":[{"id":2}]}],"opts":null,"flag":false}`
	ok := map[string]string{
		"identical":             base,
		"other key order":       `{"flag":false,"sheets":[{"view":{"zoom":100},"cells":[{"v":1,"r":1},{"v":"x","r":2}],"name":"A"},{"pivots":[{"id":2}],"charts":[{"id":1}],"names":{"n":"A!A1"},"name":"B"}],"opts":null}`,
		"number spelling":       `{"sheets":[{"name":"A","cells":[{"r":1.0,"v":1e0},{"r":2,"v":"x"}],"merges":[],"view":{"zoom":1e2}},{"name":"B","names":{"n":"A!A1"},"charts":[{"id":1}],"pivots":[{"id":2}]}],"opts":null,"flag":false}`,
		"engine adds fields":    `{"sheets":[{"name":"A","cells":[{"r":1,"v":1,"fmt":"General"},{"r":2,"v":"x"}],"merges":[],"view":{"zoom":100,"grid":true}},{"name":"B","names":{"n":"A!A1"},"charts":[{"id":1}],"pivots":[{"id":2}],"extra":1}],"version":7}`,
		"empty fields left out": `{"sheets":[{"name":"A","cells":[{"r":1,"v":1},{"r":2,"v":"x"}],"view":{"zoom":100}},{"name":"B","names":{"n":"A!A1"},"charts":[{"id":1}],"pivots":[{"id":2}]}]}`,
	}
	for name, snapshot := range ok {
		if err := CraftNativeSheetSnapshotKeepsEverything([]byte(base), []byte(snapshot)); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	lost := map[string]string{
		"a cell lost":         `{"sheets":[{"name":"A","cells":[{"r":1,"v":1}],"merges":[],"view":{"zoom":100}},{"name":"B","names":{"n":"A!A1"},"charts":[{"id":1}],"pivots":[{"id":2}]}]}`,
		"a value changed":     `{"sheets":[{"name":"A","cells":[{"r":1,"v":2},{"r":2,"v":"x"}],"view":{"zoom":100}},{"name":"B","names":{"n":"A!A1"},"charts":[{"id":1}],"pivots":[{"id":2}]}]}`,
		"a sheet lost":        `{"sheets":[{"name":"A","cells":[{"r":1,"v":1},{"r":2,"v":"x"}],"view":{"zoom":100}}]}`,
		"a defined name lost": `{"sheets":[{"name":"A","cells":[{"r":1,"v":1},{"r":2,"v":"x"}],"view":{"zoom":100}},{"name":"B","charts":[{"id":1}],"pivots":[{"id":2}]}]}`,
		"a name changed":      `{"sheets":[{"name":"A","cells":[{"r":1,"v":1},{"r":2,"v":"x"}],"view":{"zoom":100}},{"name":"B","names":{"n":"A!A2"},"charts":[{"id":1}],"pivots":[{"id":2}]}]}`,
		"a chart lost":        `{"sheets":[{"name":"A","cells":[{"r":1,"v":1},{"r":2,"v":"x"}],"view":{"zoom":100}},{"name":"B","names":{"n":"A!A1"},"pivots":[{"id":2}]}]}`,
		"a pivot lost":        `{"sheets":[{"name":"A","cells":[{"r":1,"v":1},{"r":2,"v":"x"}],"view":{"zoom":100}},{"name":"B","names":{"n":"A!A1"},"charts":[{"id":1}],"pivots":[]}]}`,
		"a view setting lost": `{"sheets":[{"name":"A","cells":[{"r":1,"v":1},{"r":2,"v":"x"}]},{"name":"B","names":{"n":"A!A1"},"charts":[{"id":1}],"pivots":[{"id":2}]}]}`,
		"not json":            `not json`,
		"two documents":       base + base,
	}
	for name, snapshot := range lost {
		if CraftNativeSheetSnapshotKeepsEverything([]byte(base), []byte(snapshot)) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestChatRunContextMarkerOnlyOnTheChatRunPath(t *testing.T) {
	if CraftNativeChatRun(context.Background()) {
		t.Fatal("a plain context claimed to be a chat run")
	}
	r := newChatRunToolRig(t)
	var seen []bool
	r.m.config.AgentApply = func(ctx context.Context, _ CraftNativeDocumentProfile, _ []byte, _ json.RawMessage) ([]byte, error) {
		seen = append(seen, CraftNativeChatRun(ctx))
		return []byte(`{"sheets":[{"name":"edited"}]}`), nil
	}
	acquired := r.call(r.token, `{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	if w := r.call(r.token, fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"m","commands":[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]}`, lease.Generation)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if len(seen) != 1 || !seen[0] {
		t.Fatalf("the engine call did not carry the chat run marker: %v", seen)
	}
}

// Only the arrays named as sets may be reordered: defined names and merges.
func TestSnapshotKeepsEverythingAllowsReorderOnlyForSets(t *testing.T) {
	base := `{"sheets":[{"merges":["A1:B2","D1:E1"],"cells":[1,2]}],"definedNames":[{"n":"a"},{"n":"b"}]}`
	same := `{"sheets":[{"merges":["D1:E1","A1:B2"],"cells":[1,2]}],"definedNames":[{"n":"b"},{"n":"a"}]}`
	if err := CraftNativeSheetSnapshotKeepsEverything([]byte(base), []byte(same)); err != nil {
		t.Fatalf("a set in another order refused: %v", err)
	}
	moved := `{"sheets":[{"merges":["A1:B2","D1:E1"],"cells":[2,1]}],"definedNames":[{"n":"a"},{"n":"b"}]}`
	path, err := CraftNativeSheetFirstLoss([]byte(base), []byte(moved))
	if err == nil || path != "$.sheets[0].cells[0]" {
		t.Fatalf("positional array reorder: path=%q err=%v", path, err)
	}
	if path, err = CraftNativeSheetFirstLoss([]byte(base), []byte(base)); err != nil || path != "" {
		t.Fatalf("identical: %q %v", path, err)
	}
}

// A batch costs one re-calculation per rectangle, not per command, and writes
// exactly the cells the commands wrote with the values the last command gave.
func TestCoalesceSheetWritesJoinsCellsIntoFewRectanglesWithoutChangingWhatIsWritten(t *testing.T) {
	var writes []sheetWrite
	for i := 1; i <= 64; i++ { // a column written cell by cell
		writes = append(writes, sheetWrite{address: fmt.Sprintf("'S'!B%d", i), values: [][]any{{fmt.Sprint(i)}}})
	}
	writes = append(writes,
		sheetWrite{address: "'S'!B10", values: [][]any{{"later"}}}, // the last write to a cell wins
		sheetWrite{address: "'S'!C1:D2", values: [][]any{{1.0, 2.0}, {3.0, 4.0}}},
		sheetWrite{address: "'S'!Z100", values: [][]any{{"far"}}}, // a gap is never filled
		sheetWrite{address: "'T'!A1", values: [][]any{{"other sheet"}}},
	)
	rects, ok := coalesceSheetWrites(writes, true)
	if !ok {
		t.Fatal("addresses refused")
	}
	// The column B (64 cells) plus the block C1:D2 are different widths, so
	// three rectangles for sheet S (B1:B64, C1:D2, Z100) and one for T.
	if len(rects) != 4 {
		t.Fatalf("%d rectangles: %+v", len(rects), rects)
	}
	got := map[string]any{}
	for _, r := range rects {
		sheet, start, _ := strings.Cut(r.address, "!")
		m := sheetWriteAddress.FindStringSubmatch(start)
		if m == nil {
			t.Fatalf("bad address %q", r.address)
		}
		row0, _ := strconv.Atoi(m[3])
		col0 := int(m[2][0]-'A') + 1
		for i, row := range r.values {
			for j, v := range row {
				got[fmt.Sprintf("%s!%c%d", sheet, 'A'+col0-1+j, row0+i)] = v
			}
		}
	}
	want := map[string]any{"'S'!B10": "later", "'S'!C1": 1.0, "'S'!D1": 2.0, "'S'!C2": 3.0, "'S'!D2": 4.0, "'S'!Z100": "far", "'T'!A1": "other sheet"}
	for i := 1; i <= 64; i++ {
		if i != 10 {
			want[fmt.Sprintf("'S'!B%d", i)] = fmt.Sprint(i)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("wrote %d cells, the commands wrote %d", len(got), len(want))
	}
	for cell, v := range want {
		if got[cell] != v {
			t.Fatalf("%s: %v, want %v", cell, got[cell], v)
		}
	}
}

func TestCoalesceSheetWritesSplitsBigRectanglesAtTheEngineLimit(t *testing.T) {
	rows := make([][]any, 300)
	for i := range rows {
		rows[i] = make([]any, 100) // 30 000 cells
		for j := range rows[i] {
			rows[i][j] = 1.0
		}
	}
	rects, ok := coalesceSheetWrites([]sheetWrite{{address: "A1", values: rows}}, true)
	if !ok || len(rects) != 3 {
		t.Fatalf("%d rectangles, ok=%v", len(rects), ok)
	}
	for _, r := range rects {
		if len(r.values)*len(r.values[0]) > sheetWriteMaxCells {
			t.Fatalf("a rectangle of %d cells", len(r.values)*len(r.values[0]))
		}
	}
}

func TestSheetSessionApplyWritesABatchWithOneEngineWritePerRectangle(t *testing.T) {
	a, engine, p := sheetFixture()
	var commands []string
	// Numbers, written cell by cell (text keeps its single-cell path).
	for i := 1; i <= 64; i++ {
		commands = append(commands, fmt.Sprintf(`{"command":"range.setValues","params":{"range":"A%d","values":[[%d]]}}`, i, i))
	}
	if _, err := a.Apply(context.Background(), p, []byte(`{}`), json.RawMessage("["+strings.Join(commands, ",")+"]")); err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, call := range engine.session.calls {
		if strings.HasPrefix(call, "set ") || strings.HasPrefix(call, "write ") {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("%d engine writes for 64 adjacent cells: %v", writes, engine.session.calls)
	}
}

// Text is never joined while the block path is not proven equal to typing:
// it is written cell by cell, a later write still wins, and numbers around it
// are joined as before.
func TestCoalesceSheetWritesKeepsTextOnTheSingleCellPath(t *testing.T) {
	rects, ok := coalesceSheetWrites([]sheetWrite{
		{address: "A1", values: [][]any{{1.0, "007", 3.0}}},
		{address: "B2", values: [][]any{{"1/2"}}},
		{address: "A1", values: [][]any{{"now text"}}},
		{address: "C1", values: [][]any{{4.0}}},
		{address: "C1", values: [][]any{{5.0}}}, // a number replaces an earlier number
		{address: "B1", values: [][]any{{9.0}}}, // a number replaces an earlier text
	}, false)
	if !ok {
		t.Fatal("refused")
	}
	var texts, numbers []string
	for _, r := range rects {
		if text, single := r.single(); single {
			texts = append(texts, r.address+"="+text)
			continue
		}
		for i, row := range r.values {
			for j, v := range row {
				numbers = append(numbers, fmt.Sprintf("%s+%d,%d=%v", r.address, i, j, v))
			}
		}
	}
	sort.Strings(texts)
	if strings.Join(texts, "|") != "A1=now text|B2=1/2" {
		t.Fatalf("text cells %v", texts)
	}
	if len(numbers) != 2 { // B1 and C1 are not adjacent runs of the same shape: one rectangle B1:C1
		t.Logf("numbers %v", numbers)
	}
	for _, n := range numbers {
		if strings.Contains(n, "007") || strings.Contains(n, "1/2") {
			t.Fatalf("text inside a rectangle: %v", numbers)
		}
	}
}

// A batch is one engine Apply (one re-calculation), and a batch that fails
// after some writes discards the session instead of returning it to the pool.
func TestSheetSessionApplyIsOneEngineBatchAndAFailurePartWayDiscardsTheSession(t *testing.T) {
	a, engine, p := sheetFixture()
	commands := json.RawMessage(`[{"command":"cell.set","params":{"cell":"A1","input":"one"}},{"command":"range.setValues","params":{"range":"C1","values":[[1,2],[3,4]]}},{"command":"cell.set","params":{"cell":"A9","input":"nine"}}]`)
	if _, err := a.Apply(context.Background(), p, []byte(`{}`), commands); err != nil {
		t.Fatal(err)
	}
	if engine.session.applies != 1 || engine.discarded != 0 {
		t.Fatalf("applies %d discarded %d", engine.session.applies, engine.discarded)
	}
	engine.session.calls, engine.session.failOn = nil, "set A9=nine"
	if _, err := a.Apply(context.Background(), p, []byte(`{}`), commands); !errors.Is(err, ErrCraftNativeAgentEngine) {
		t.Fatalf("a write that failed part way gave %v", err)
	}
	if engine.discarded != 1 {
		t.Fatalf("the half-applied session was not discarded (%d)", engine.discarded)
	}
}
