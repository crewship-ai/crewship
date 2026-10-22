package appengine

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
)

type runnerFunc func(context.Context, []byte, Command) (EngineResult, error)

func (f runnerFunc) Run(c context.Context, b []byte, o Command) (EngineResult, error) {
	return f(c, b, o)
}
func TestGridCraftEffectsRejectUndeclaredCellAndStructuralChanges(t *testing.T) {
	base := json.RawMessage(`{"sheets":[{"name":"Sheet1","cells":{"bands":{},"len":0}}]}`)
	for _, test := range []struct {
		name   string
		result string
		want   error
	}{
		{"one cell", `{"sheets":[{"name":"Sheet1","cells":{"bands":{"0":{"rows":{"1":{"1":{"value":42}}}}},"len":1}}]}`, nil},
		{"another cell", `{"sheets":[{"name":"Sheet1","cells":{"bands":{"0":{"rows":{"8":{"2":{"value":42}}}}},"len":1}}]}`, ErrEffects},
		{"structure", `{"sheets":[{"name":"Renamed","cells":{"bands":{},"len":0}}]}`, ErrEffects},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := GridCraft{Runner: runnerFunc(func(context.Context, []byte, Command) (EngineResult, error) {
				return EngineResult{Base: base, Result: json.RawMessage(test.result)}, nil
			})}
			b := Binding{Engine: engine.Identity()}
			_, err := engine.Prepare(t.Context(), b, State{Snapshot: base}, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"})
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
}
func TestGridCraftDoesNotRunArbitraryCommands(t *testing.T) {
	engine := GridCraft{Runner: runnerFunc(func(context.Context, []byte, Command) (EngineResult, error) {
		t.Fatal("unapproved command executed")
		return EngineResult{}, nil
	})}
	for _, kind := range []string{"file.open", "file.save", "engine.execute", "ui.click", "range.fill"} {
		_, err := engine.Prepare(t.Context(), Binding{}, State{}, Command{Kind: kind})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}

// recalcBook builds a single-sheet workbook from 0-based "row,col" keys.
func recalcBook(t *testing.T, cells map[[2]int]string) json.RawMessage {
	t.Helper()
	bands := map[string]map[string]map[string]map[string]json.RawMessage{}
	for at, cell := range cells {
		band := strconv.Itoa(at[0] / 64)
		if bands[band] == nil {
			bands[band] = map[string]map[string]map[string]json.RawMessage{"rows": {}}
		}
		row := strconv.Itoa(at[0])
		if bands[band]["rows"][row] == nil {
			bands[band]["rows"][row] = map[string]json.RawMessage{}
		}
		bands[band]["rows"][row][strconv.Itoa(at[1])] = json.RawMessage(cell)
	}
	raw, err := json.Marshal(map[string]any{"sheets": []any{map[string]any{"name": "Sheet1", "cells": map[string]any{"bands": bands, "len": len(cells)}}}, "styles": []any{"default"}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func number(v string) string {
	return `{"value":{"t":"Number","v":` + v + `},"formula":null,"style":0}`
}
func formula(text, v string) string {
	return `{"value":{"t":"Number","v":` + v + `},"formula":{"text":"` + text + `","array":null},"style":0}`
}

// Recalculated formula values are derived data: outside the mandated target they
// may change only when the formula is untouched and the worker's independent full
// recalculation confirms every cached value. Authored data stays byte-identical.
func TestGridCraftEffectsSeparateAuthoredFromDerivedRecalculation(t *testing.T) {
	var (
		b2 = [2]int{1, 1}
		c9 = [2]int{8, 2}
		d9 = [2]int{8, 3}
		d1 = [2]int{0, 3}
		e1 = [2]int{0, 4}
	)
	base := map[[2]int]string{b2: number("1"), c9: formula("B2*2", "2"), d9: formula("C9+1", "3"), d1: number("7")}
	with := func(changes map[[2]int]string, removed ...[2]int) map[[2]int]string {
		out := map[[2]int]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range changes {
			out[k] = v
		}
		for _, k := range removed {
			delete(out, k)
		}
		return out
	}
	verified := &RecalcVerification{FormulaCells: 2, Checked: 2}
	for _, test := range []struct {
		name   string
		result map[[2]int]string
		recalc *RecalcVerification
		want   error
	}{
		{"target only needs no verification", with(map[[2]int]string{b2: number("42")}), nil, nil},
		{"dependent recalculated", with(map[[2]int]string{b2: number("42"), c9: formula("B2*2", "84")}), verified, nil},
		{"chain of dependents", with(map[[2]int]string{b2: number("42"), c9: formula("B2*2", "84"), d9: formula("C9+1", "85")}), &RecalcVerification{FormulaCells: 2, Checked: 2}, nil},
		{"error value recalculated", with(map[[2]int]string{b2: number("0"), c9: `{"value":{"t":"Error","v":"Div0"},"formula":{"text":"B2*2","array":null},"style":0}`}), verified, nil},
		{"recalculation without verification", with(map[[2]int]string{b2: number("42"), c9: formula("B2*2", "84")}), nil, ErrEffects},
		{"verification reports mismatch", with(map[[2]int]string{b2: number("42"), c9: formula("B2*2", "85")}), &RecalcVerification{FormulaCells: 2, Checked: 2, Mismatched: 1}, ErrEffects},
		{"verification skipped volatile cells", with(map[[2]int]string{b2: number("42"), c9: formula("B2*2", "84")}), &RecalcVerification{FormulaCells: 2, Checked: 1, Volatile: 1}, ErrEffects},
		{"verification without oracle", with(map[[2]int]string{b2: number("42"), c9: formula("B2*2", "84")}), &RecalcVerification{FormulaCells: 2, Checked: 1, NoOracle: 1}, ErrEffects},
		{"verification stale cache", with(map[[2]int]string{b2: number("42"), c9: formula("B2*2", "84")}), &RecalcVerification{FormulaCells: 2, Checked: 2, StaleCache: true}, ErrEffects},
		{"verification checked nothing", with(map[[2]int]string{b2: number("42"), c9: formula("B2*2", "84")}), &RecalcVerification{}, ErrEffects},
		{"formula changed", with(map[[2]int]string{b2: number("42"), c9: formula("B2*3", "126")}), verified, ErrEffects},
		{"formula array changed", with(map[[2]int]string{b2: number("42"), c9: `{"value":{"t":"Number","v":84},"formula":{"text":"B2*2","array":"C9:C9"},"style":0}`}), verified, ErrEffects},
		{"non-formula changed", with(map[[2]int]string{b2: number("42"), d1: number("8")}), verified, ErrEffects},
		{"formula cell turned into value", with(map[[2]int]string{b2: number("42"), c9: number("84")}), verified, ErrEffects},
		{"value turned into formula", with(map[[2]int]string{b2: number("42"), d1: formula("B2*2", "84")}), verified, ErrEffects},
		{"style changed outside", with(map[[2]int]string{b2: number("42"), c9: `{"value":{"t":"Number","v":84},"formula":{"text":"B2*2","array":null},"style":1}`}), verified, ErrEffects},
		{"extra authored field outside", with(map[[2]int]string{b2: number("42"), c9: `{"value":{"t":"Number","v":84},"formula":{"text":"B2*2","array":null},"style":0,"note":"x"}`}), verified, ErrEffects},
		{"unsupported derived value", with(map[[2]int]string{b2: number("42"), c9: `{"value":{"t":"Array","v":[84]},"formula":{"text":"B2*2","array":null},"style":0}`}), verified, ErrEffects},
		{"derived value with extra field", with(map[[2]int]string{b2: number("42"), c9: `{"value":{"t":"Number","v":84,"x":1},"formula":{"text":"B2*2","array":null},"style":0}`}), verified, ErrEffects},
		{"derived value type mismatch", with(map[[2]int]string{b2: number("42"), c9: `{"value":{"t":"Number","v":"84"},"formula":{"text":"B2*2","array":null},"style":0}`}), verified, ErrEffects},
		{"cell added outside", with(map[[2]int]string{b2: number("42"), e1: number("1")}), verified, ErrEffects},
		{"formula cell added outside", with(map[[2]int]string{b2: number("42"), e1: formula("B2*2", "84")}), verified, ErrEffects},
		{"cell removed outside", with(map[[2]int]string{b2: number("42")}, d1), verified, ErrEffects},
		{"formula cell removed outside", with(map[[2]int]string{b2: number("42")}, d9), verified, ErrEffects},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := recalcBook(t, base)
			engine := GridCraft{Runner: runnerFunc(func(context.Context, []byte, Command) (EngineResult, error) {
				return EngineResult{Base: before, Result: recalcBook(t, test.result), Recalc: test.recalc}, nil
			})}
			candidate, err := engine.Prepare(t.Context(), Binding{Engine: engine.Identity()}, State{Snapshot: before}, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"})
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
			// Derived recalculation is reported by the receipt's mandate scope only
			// through the authored target; it never widens the mandated effects.
			if err == nil && (len(candidate.Effects) != 1 || candidate.Effects[0] != "Sheet1!B2") {
				t.Fatalf("effects widened: %v", candidate.Effects)
			}
		})
	}
}

// The worker transport decodes strictly; the verification must survive it, and an
// older worker that omits it leaves derived changes unverified (fail closed).
func TestEngineResultCarriesRecalcVerificationThroughStrictDecode(t *testing.T) {
	want := EngineResult{Base: json.RawMessage(`{}`), Result: json.RawMessage(`{}`), Recalc: &RecalcVerification{FormulaCells: 2, Checked: 2}}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got EngineResult
	if err = DecodeStrict(raw, &got); err != nil || got.Recalc == nil || *got.Recalc != *want.Recalc || !got.Recalc.verified() {
		t.Fatalf("got %+v %v", got.Recalc, err)
	}
	var old EngineResult
	if err = DecodeStrict([]byte(`{"base":{},"result":{}}`), &old); err != nil || old.Recalc.verified() {
		t.Fatalf("old worker: %+v %v", old.Recalc, err)
	}
}
