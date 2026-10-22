package appengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func cells(sheet string, addresses ...string) []CellInput {
	out := make([]CellInput, 0, len(addresses))
	for _, a := range addresses {
		out = append(out, CellInput{Sheet: sheet, Cell: a, Input: "v-" + a})
	}
	return out
}

func TestCellsSetValidation(t *testing.T) {
	many := make([]string, MaxEditCells+1)
	for i := range many {
		many[i] = fmt.Sprintf("A%d", i+1)
	}
	for _, test := range []struct {
		name    string
		command Command
		ok      bool
	}{
		{"one cell", Command{Kind: "cells.set", Cells: cells("Sheet1", "B2")}, true},
		{"limit", Command{Kind: "cells.set", Cells: cells("Sheet1", many[:MaxEditCells]...)}, true},
		{"empty", Command{Kind: "cells.set"}, false},
		{"over limit", Command{Kind: "cells.set", Cells: cells("Sheet1", many...)}, false},
		{"two sheets", Command{Kind: "cells.set", Cells: append(cells("Sheet1", "B2"), cells("Sheet2", "B3")...)}, false},
		{"duplicate", Command{Kind: "cells.set", Cells: cells("Sheet1", "B2", "B2")}, false},
		{"bad address", Command{Kind: "cells.set", Cells: cells("Sheet1", "b2")}, false},
		{"oversized input", Command{Kind: "cells.set", Cells: []CellInput{{"Sheet1", "B2", strings.Repeat("x", 8193)}}}, false},
		{"stray single-cell field", Command{Kind: "cells.set", Cell: "B2", Cells: cells("Sheet1", "B3")}, false},
		{"cells on cell.set", Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Cells: cells("Sheet1", "B3")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.command.validate(); (err == nil) != test.ok {
				t.Fatalf("validate=%v", err)
			}
		})
	}
}

// A multi-cell human mutation is atomic, recorded per cell, and conflicts when
// another writer changed any one of its cells.
func TestCellsSetCommitsAtomicallyAndConflictsPerCell(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	first := Request{MutationID: "s1:1", MandateID: "human-import", SessionID: "s1", BaseRev: 1, Generation: 1, Command: Command{Kind: "cells.set", Cells: cells("Sheet1", "B2", "C3")}}
	receipt, err := s.Execute(t.Context(), k, first, a, e)
	if err != nil || receipt.Rev != 2 || strings.Join(receipt.Effects, ",") != "Sheet1!B2,Sheet1!C3" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	var command string
	if err = db.QueryRow(`SELECT command FROM gridcraft_audit WHERE rev=2`).Scan(&command); err != nil || command != `{"kind":"cells","cells":[{"sheet":"Sheet1","cell":"B2","input":"v-B2"},{"sheet":"Sheet1","cell":"C3","input":"v-C3"}]}` {
		t.Fatalf("command=%s err=%v", command, err)
	}
	second := Request{MutationID: "s2:1", MandateID: "human-import", SessionID: "s2", BaseRev: 1, Generation: 1, Command: Command{Kind: "cells.set", Cells: cells("Sheet1", "D4", "C3")}}
	var precondition *PreconditionError
	if _, err = s.Execute(t.Context(), k, second, a, e); !errors.As(err, &precondition) || len(precondition.Conflicts) != 1 || precondition.Conflicts[0].Cell != "C3" {
		t.Fatalf("conflict %v %+v", err, precondition)
	}
	second.MutationID, second.Command.Cells = "s2:2", cells("Sheet1", "D4", "E5")
	if r, err := s.Execute(t.Context(), k, second, a, e); err != nil || r.Rev != 3 {
		t.Fatalf("independent %+v %v", r, err)
	}
}

// Effects stay bound to the authored targets: a mandate for one cell cannot
// carry a multi-cell command, and an engine candidate changing an authored cell
// outside the targets is refused.
func TestCellsSetEffectsStayWithinTargets(t *testing.T) {
	s, _, owner, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, owner, k, e)
	agent := func(context.Context, *sql.Tx) (Principal, error) {
		return Principal{AuthorityID: "agent:a", MandateID: "m", Targets: []string{"Sheet1!B2"}}, nil
	}
	wide := Request{MutationID: "agent-wide", MandateID: "m", BaseRev: 1, Generation: 1, Command: Command{Kind: "cells.set", Cells: cells("Sheet1", "B2", "C3")}}
	if _, err := s.Execute(t.Context(), k, wide, agent, e); !errors.Is(err, ErrEffects) || e.calls.Load() != 1 {
		t.Fatalf("mandate widened: %v calls=%d", err, e.calls.Load())
	}
	base := json.RawMessage(`{"sheets":[{"name":"Sheet1","cells":{"bands":{},"len":0}}]}`)
	for _, test := range []struct {
		name   string
		result string
		want   error
	}{
		{"both targets", `{"sheets":[{"name":"Sheet1","cells":{"bands":{"0":{"rows":{"1":{"1":{"value":1}},"2":{"2":{"value":2}}}}},"len":2}}]}`, nil},
		{"outside targets", `{"sheets":[{"name":"Sheet1","cells":{"bands":{"0":{"rows":{"8":{"2":{"value":42}}}}},"len":1}}]}`, ErrEffects},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := GridCraft{Runner: runnerFunc(func(context.Context, []byte, Command) (EngineResult, error) {
				return EngineResult{Base: base, Result: json.RawMessage(test.result)}, nil
			})}
			c, err := engine.Prepare(t.Context(), Binding{Engine: engine.Identity()}, State{Snapshot: base}, Command{Kind: "cells.set", Cells: cells("Sheet1", "B2", "C3")})
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
			if err == nil && strings.Join(c.Effects, ",") != "Sheet1!B2,Sheet1!C3" {
				t.Fatalf("effects %v", c.Effects)
			}
		})
	}
}

// The pinned engine applies several cells in one process run and recalculates
// dependent formulas, which a human edit on a formula sheet relies on.
func TestRealGridCraftCellsSetOnFormulaSheet(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY for the pinned engine contract")
	}
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	runner := localFixtureRunner{binary, t.TempDir()}
	setup, err := runner.Run(t.Context(), raw, Command{Kind: "cells.set", Cells: []CellInput{{"Sheet1", "B2", "1"}, {"Sheet1", "C9", "=B2*2"}}})
	if err != nil {
		t.Fatal(err)
	}
	engine := GridCraft{Runner: runner}
	candidate, err := engine.Prepare(t.Context(), Binding{Engine: engine.Identity()}, State{Snapshot: setup.Result}, Command{Kind: "cells.set", Cells: []CellInput{{"Sheet1", "B2", "5"}, {"Sheet1", "D4", "text"}}})
	if err != nil {
		t.Fatal(err)
	}
	workbook, err := decodeWorkbook(candidate.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	values, err := extractCells(workbook)
	if err != nil {
		t.Fatal(err)
	}
	c9, _ := values["Sheet1!C9"].(map[string]any)["value"].(map[string]any)["v"].(json.Number).Float64()
	d4, _ := values["Sheet1!D4"].(map[string]any)["value"].(map[string]any)["v"].(string)
	if c9 != 10 || d4 != "text" || strings.Join(candidate.Effects, ",") != "Sheet1!B2,Sheet1!D4" {
		t.Fatalf("C9=%v D4=%q effects=%v", c9, d4, candidate.Effects)
	}
}
